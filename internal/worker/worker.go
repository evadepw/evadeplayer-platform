// Package worker runs the transcoding loop: claim a pending video from
// PostgreSQL, download the original from storage, transcode it with ffmpeg,
// upload the HLS output and record the result.
//
// Reliability model: a claim moves the video to 'processing' and spends one
// attempt. While transcoding, the worker heartbeats the row; if the worker
// dies, another worker's periodic reclaim pass requeues the video (or fails it
// once the attempt budget is spent). On graceful shutdown in-flight jobs are
// released back to 'pending' without spending the attempt.
package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/evadepw/evadeplayer-platform/internal/ffmpeg"
	"github.com/evadepw/evadeplayer-platform/internal/model"
	"github.com/evadepw/evadeplayer-platform/internal/repository"
	"github.com/evadepw/evadeplayer-platform/internal/storage"
)

const (
	pollInterval      = 3 * time.Second
	reclaimInterval   = time.Minute
	heartbeatInterval = 15 * time.Second
	// staleAfter must comfortably exceed heartbeatInterval so a slow database
	// round-trip is never mistaken for a dead worker.
	staleAfter = 2 * time.Minute
	// dbTimeout bounds bookkeeping queries (status, progress, heartbeat).
	dbTimeout = 10 * time.Second
)

// Store is the queue and result-recording surface the worker needs.
type Store interface {
	ClaimNextPending(ctx context.Context) (*repository.Job, error)
	Heartbeat(ctx context.Context, id string) error
	Release(ctx context.Context, id string) error
	Requeue(ctx context.Context, id string) error
	ReclaimStale(ctx context.Context, staleAfter time.Duration, maxAttempts int) (requeued, failed []string, err error)
	UpdateStatus(ctx context.Context, id string, status model.VideoStatus, errMsg *string) error
	SetProgress(ctx context.Context, id string, pct int) error
	SetMetadata(ctx context.Context, id string, duration float64, width, height int) error
	SetTracks(ctx context.Context, id string, audio, subtitles []model.Track) error
	SetStoryboard(ctx context.Context, id string, sb model.Storyboard) error
}

// Config holds the transcoding parameters for a worker.
type Config struct {
	TempDir           string
	Concurrency       int
	MaxAttempts       int
	HLSSegmentSeconds int
	Accel             string
	Codecs            []string
	Qualities         []ffmpeg.Quality
	Thumbnail         ffmpeg.ThumbnailConfig
	Encoding          ffmpeg.EncodingConfig
}

type Worker struct {
	store   Store
	seaweed *storage.SeaweedFS
	cfg     Config
}

func New(store Store, seaweed *storage.SeaweedFS, cfg Config) *Worker {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 3
	}
	return &Worker{store: store, seaweed: seaweed, cfg: cfg}
}

// Run processes the queue until ctx is cancelled, then waits for in-flight
// jobs to wind down (each is released back to the queue).
func (w *Worker) Run(ctx context.Context) {
	slog.Info("transcoder worker started", "concurrency", w.cfg.Concurrency, "max_attempts", w.cfg.MaxAttempts)

	slots := make(chan struct{}, w.cfg.Concurrency)
	var wg sync.WaitGroup

	w.reclaim(ctx)
	lastReclaim := time.Now()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		if time.Since(lastReclaim) >= reclaimInterval {
			w.reclaim(ctx)
			lastReclaim = time.Now()
		}

		// Fill every free slot with a claimed job, then wait for the next tick.
		for len(slots) < cap(slots) {
			job, err := w.store.ClaimNextPending(ctx)
			if err != nil {
				if !errors.Is(err, repository.ErrNotFound) && ctx.Err() == nil {
					slog.Error("claim pending video", "error", err)
				}
				break
			}
			slots <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-slots }()
				w.runJob(ctx, job)
			}()
		}

		select {
		case <-ctx.Done():
			slog.Info("shutting down, waiting for in-flight jobs to release")
			wg.Wait()
			return
		case <-ticker.C:
		}
	}
}

// reclaim requeues stale claims left behind by dead workers.
func (w *Worker) reclaim(ctx context.Context) {
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbTimeout)
	defer cancel()
	requeued, failed, err := w.store.ReclaimStale(opCtx, staleAfter, w.cfg.MaxAttempts)
	if err != nil {
		slog.Error("reclaim stale videos", "error", err)
		return
	}
	for _, id := range requeued {
		slog.Warn("stale claim requeued", "video_id", id)
	}
	for _, id := range failed {
		slog.Warn("stale claim failed permanently, attempt budget exhausted", "video_id", id)
	}
}

// runJob processes one claimed job and records the outcome. Outcome writes use
// a context that survives cancellation: shutdown must not corrupt bookkeeping.
func (w *Worker) runJob(ctx context.Context, job *repository.Job) {
	hbCtx, stopHeartbeat := context.WithCancel(context.WithoutCancel(ctx))
	go w.heartbeatLoop(hbCtx, job.VideoID)
	err := w.process(ctx, job)
	stopHeartbeat()
	if err == nil {
		return
	}

	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbTimeout)
	defer cancel()

	if ctx.Err() != nil {
		// Shutdown interrupted the job — not the video's fault, give the
		// attempt back and let another worker pick it up.
		slog.Info("job interrupted by shutdown, releasing", "video_id", job.VideoID)
		if err := w.store.Release(opCtx, job.VideoID); err != nil {
			slog.Error("release job", "video_id", job.VideoID, "error", err)
		}
		return
	}

	if job.Attempts < w.cfg.MaxAttempts {
		slog.Warn("attempt failed, requeueing", "video_id", job.VideoID, "attempt", job.Attempts, "max_attempts", w.cfg.MaxAttempts, "error", err)
		if rqErr := w.store.Requeue(opCtx, job.VideoID); rqErr != nil {
			slog.Error("requeue job", "video_id", job.VideoID, "error", rqErr)
		}
		return
	}

	slog.Error("attempt failed permanently", "video_id", job.VideoID, "attempt", job.Attempts, "max_attempts", w.cfg.MaxAttempts, "error", err)
	errMsg := err.Error()
	if dbErr := w.store.UpdateStatus(opCtx, job.VideoID, model.StatusFailed, &errMsg); dbErr != nil {
		slog.Error("set failed status", "video_id", job.VideoID, "error", dbErr)
	}
}

func (w *Worker) heartbeatLoop(ctx context.Context, videoID string) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			opCtx, cancel := context.WithTimeout(ctx, dbTimeout)
			if err := w.store.Heartbeat(opCtx, videoID); err != nil {
				slog.Error("heartbeat", "video_id", videoID, "error", err)
			}
			cancel()
		}
	}
}

func (w *Worker) process(ctx context.Context, job *repository.Job) error {
	videoID := job.VideoID
	slog.Info("processing video", "video_id", videoID, "attempt", job.Attempts, "max_attempts", w.cfg.MaxAttempts)

	workDir := filepath.Join(w.cfg.TempDir, videoID)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(workDir); err != nil {
			slog.Error("cleanup work dir", "dir", workDir, "error", err)
		}
	}()

	localOriginal := filepath.Join(workDir, "original"+filepath.Ext(job.OriginalPath))
	if err := w.downloadFile(ctx, job.OriginalPath, localOriginal); err != nil {
		return fmt.Errorf("download original: %w", err)
	}
	w.setProgress(ctx, videoID, 10)

	probe, err := ffmpeg.Probe(ctx, localOriginal)
	if err != nil {
		return fmt.Errorf("probe video: %w", err)
	}
	slog.Info("probed video", "video_id", videoID, "duration_s", probe.Duration, "width", probe.Width, "height", probe.Height)

	if err := w.setMetadata(ctx, videoID, probe.Duration, probe.Width, probe.Height); err != nil {
		return fmt.Errorf("update metadata: %w", err)
	}
	w.setProgress(ctx, videoID, 15)

	hlsDir := filepath.Join(workDir, "hls")
	var lastProgressUpdate time.Time
	variants, err := ffmpeg.TranscodeHLS(ctx, localOriginal, hlsDir, probe.Width, probe.Height, w.cfg.HLSSegmentSeconds, probe.FrameRate, probe.Duration, w.cfg.Accel, w.cfg.Codecs, w.cfg.Qualities, len(probe.Audio) > 0, w.cfg.Encoding, func(f float64) {
		if time.Since(lastProgressUpdate) < time.Second {
			return
		}
		lastProgressUpdate = time.Now()
		pct := 15 + int(f*50)
		if pct > 64 {
			pct = 64
		}
		w.setProgress(ctx, videoID, pct)
	})
	if err != nil {
		return fmt.Errorf("transcode HLS: %w", err)
	}
	slog.Info("video transcoded", "video_id", videoID, "variants", len(variants))
	w.setProgress(ctx, videoID, 65)

	extractedAudio, _ := ffmpeg.ExtractAudio(ctx, localOriginal, hlsDir, probe.Audio, w.cfg.HLSSegmentSeconds, w.cfg.Encoding)
	slog.Info("audio tracks extracted", "video_id", videoID, "extracted", len(extractedAudio), "total", len(probe.Audio))
	w.setProgress(ctx, videoID, 72)

	extractedSubs, _ := ffmpeg.ExtractSubtitles(ctx, localOriginal, hlsDir, probe.Subtitles, probe.Duration)
	slog.Info("subtitle tracks extracted", "video_id", videoID, "extracted", len(extractedSubs), "total", len(probe.Subtitles))
	w.setProgress(ctx, videoID, 78)

	if err := w.setTracks(ctx, videoID, extractedAudio, extractedSubs); err != nil {
		slog.Warn("update tracks failed (non-fatal)", "video_id", videoID, "error", err)
	}

	thumbDir := filepath.Join(workDir, "thumbnails")
	previewPath := ""
	if generated, err := ffmpeg.GeneratePreviewWithConfig(ctx, localOriginal, thumbDir, probe.Duration, w.cfg.Thumbnail); err != nil {
		slog.Warn("preview generation failed (non-fatal)", "video_id", videoID, "error", err)
	} else {
		previewPath = generated
	}

	spritePath, err := ffmpeg.GenerateSpriteWithConfig(ctx, localOriginal, thumbDir, probe.Duration, w.cfg.Thumbnail)
	if err != nil {
		slog.Warn("sprite generation failed (non-fatal)", "video_id", videoID, "error", err)
		spritePath = ""
	}
	if spritePath != "" {
		if err := ffmpeg.WriteImageStreamManifestWithConfig(hlsDir, spritePath, probe.Duration, w.cfg.Thumbnail); err != nil {
			slog.Warn("image stream generation failed (non-fatal)", "video_id", videoID, "error", err)
		}
	}
	w.setProgress(ctx, videoID, 85)

	if err := ffmpeg.WriteMasterManifestWithConfig(hlsDir, variants, extractedAudio, extractedSubs, w.cfg.Thumbnail); err != nil {
		return fmt.Errorf("write master manifest: %w", err)
	}

	if err := w.uploadHLS(ctx, hlsDir, videoID); err != nil {
		return fmt.Errorf("upload HLS: %w", err)
	}
	w.setProgress(ctx, videoID, 95)

	if previewPath != "" {
		remotePath := fmt.Sprintf("thumbnails/%s/preview.jpg", videoID)
		if err := w.seaweed.UploadFile(ctx, remotePath, previewPath); err != nil {
			slog.Warn("upload preview failed (non-fatal)", "video_id", videoID, "error", err)
		}
	}

	if spritePath != "" {
		remotePath := fmt.Sprintf("thumbnails/%s/sprite.jpg", videoID)
		if err := w.seaweed.UploadFile(ctx, remotePath, spritePath); err != nil {
			slog.Warn("upload sprite failed (non-fatal)", "video_id", videoID, "error", err)
		} else if err := w.setStoryboard(ctx, videoID, probe.Duration); err != nil {
			slog.Warn("set storyboard failed (non-fatal)", "video_id", videoID, "error", err)
		}
	}

	w.setProgress(ctx, videoID, 100)
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbTimeout)
	defer cancel()
	if err := w.store.UpdateStatus(opCtx, videoID, model.StatusReady, nil); err != nil {
		return fmt.Errorf("set ready status: %w", err)
	}

	slog.Info("video processing complete", "video_id", videoID)
	return nil
}

func (w *Worker) uploadHLS(ctx context.Context, hlsDir, videoID string) error {
	type uploadJob struct {
		localPath  string
		remotePath string
	}

	var jobs []uploadJob
	if err := filepath.Walk(hlsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(hlsDir, path)
		jobs = append(jobs, uploadJob{
			localPath:  path,
			remotePath: fmt.Sprintf("hls/%s/%s", videoID, filepath.ToSlash(rel)),
		})
		return nil
	}); err != nil {
		return err
	}

	const concurrency = 20
	sem := make(chan struct{}, concurrency)
	errc := make(chan error, len(jobs))
	var wg sync.WaitGroup

	uploadCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	for _, job := range jobs {
		sem <- struct{}{}
		wg.Add(1)
		go func(j uploadJob) {
			defer wg.Done()
			defer func() { <-sem }()
			if uploadCtx.Err() != nil {
				return
			}
			if err := w.seaweed.UploadFile(uploadCtx, j.remotePath, j.localPath); err != nil {
				errc <- fmt.Errorf("upload %s: %w", j.remotePath, err)
				cancel() // first failure aborts the remaining uploads
			}
		}(job)
	}
	wg.Wait()
	close(errc)

	return <-errc
}

func (w *Worker) downloadFile(ctx context.Context, remotePath, localPath string) error {
	rc, err := w.seaweed.Download(ctx, remotePath)
	if err != nil {
		return err
	}
	defer rc.Close()

	f, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("create local file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, rc); err != nil {
		return fmt.Errorf("write local file: %w", err)
	}
	return f.Close()
}

func (w *Worker) setProgress(ctx context.Context, videoID string, pct int) {
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbTimeout)
	defer cancel()
	if err := w.store.SetProgress(opCtx, videoID, pct); err != nil {
		slog.Error("set progress", "video_id", videoID, "progress", pct, "error", err)
	}
}

func (w *Worker) setMetadata(ctx context.Context, videoID string, duration float64, width, height int) error {
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbTimeout)
	defer cancel()
	return w.store.SetMetadata(opCtx, videoID, duration, width, height)
}

func (w *Worker) setTracks(ctx context.Context, videoID string, audio []ffmpeg.AudioStream, subs []ffmpeg.SubtitleStream) error {
	audioTracks := make([]model.Track, len(audio))
	for i, a := range audio {
		audioTracks[i] = model.Track{Index: a.TypeIndex, Language: a.Language, Title: a.Title}
	}
	subTracks := make([]model.Track, len(subs))
	for i, s := range subs {
		subTracks[i] = model.Track{Index: s.TypeIndex, Language: s.Language, Title: s.Title}
	}
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbTimeout)
	defer cancel()
	return w.store.SetTracks(opCtx, videoID, audioTracks, subTracks)
}

func (w *Worker) setStoryboard(ctx context.Context, videoID string, duration float64) error {
	t := w.cfg.Thumbnail.WithDefaults()
	// Must mirror the tile count used by ffmpeg.GenerateSpriteWithConfig so
	// cue coordinates match the actual sprite grid.
	count := int(math.Ceil(duration / float64(t.SpriteIntervalSeconds)))
	if count < 1 {
		count = 1
	}
	sb := model.Storyboard{
		IntervalSeconds: t.SpriteIntervalSeconds,
		TileWidth:       t.SpriteWidth,
		TileHeight:      t.SpriteHeight,
		Columns:         t.SpriteColumns,
		TileCount:       count,
	}
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbTimeout)
	defer cancel()
	return w.store.SetStoryboard(opCtx, videoID, sb)
}
