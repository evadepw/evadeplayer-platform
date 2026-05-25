package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/evadeplayer/transcoder/internal/ffmpeg"
	"github.com/evadeplayer/transcoder/internal/storage"
)

type TranscodeTask struct {
	VideoID         string `json:"video_id"`
	OriginalPath    string `json:"original_path"`
	PreviewOverride bool   `json:"preview_override,omitempty"`
}

type Worker struct {
	rdb               *redis.Client
	queueKey          string
	db                *pgxpool.Pool
	seaweed           *storage.SeaweedFS
	tempDir           string
	concurrency       int
	hlsSegmentSeconds int
	accel             string
	codecs            []string
	qualities         []ffmpeg.Quality
	thumbnailConfig   ffmpeg.ThumbnailConfig
	encodingConfig    ffmpeg.EncodingConfig
}

func New(
	rdb *redis.Client,
	queueKey string,
	db *pgxpool.Pool,
	seaweed *storage.SeaweedFS,
	tempDir string,
	concurrency int,
	hlsSegmentSeconds int,
	accel string,
	codecs []string,
	qualities []ffmpeg.Quality,
	thumbnailConfig ffmpeg.ThumbnailConfig,
	encodingConfig ffmpeg.EncodingConfig,
) *Worker {
	return &Worker{
		rdb:               rdb,
		queueKey:          queueKey,
		db:                db,
		seaweed:           seaweed,
		tempDir:           tempDir,
		concurrency:       concurrency,
		hlsSegmentSeconds: hlsSegmentSeconds,
		accel:             accel,
		codecs:            codecs,
		qualities:         qualities,
		thumbnailConfig:   thumbnailConfig,
		encodingConfig:    encodingConfig,
	}
}

func (w *Worker) Run(ctx context.Context) {
	sem := make(chan struct{}, w.concurrency)
	var wg sync.WaitGroup

	log.Printf("transcoder worker started, concurrency=%d", w.concurrency)
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		default:
		}

		result, err := w.rdb.BRPop(ctx, 5*time.Second, w.queueKey).Result()
		if err != nil {
			if err == redis.Nil || ctx.Err() != nil {
				continue
			}
			log.Printf("redis BRPop error: %v", err)
			time.Sleep(time.Second)
			continue
		}

		var task TranscodeTask
		if err := json.Unmarshal([]byte(result[1]), &task); err != nil {
			log.Printf("unmarshal task error: %v", err)
			continue
		}

		sem <- struct{}{}
		wg.Add(1)
		go func(t TranscodeTask) {
			defer wg.Done()
			defer func() { <-sem }()

			if err := w.process(ctx, t); err != nil {
				log.Printf("transcode failed for %s: %v", t.VideoID, err)
				errMsg := err.Error()
				if dbErr := w.setStatus(ctx, t.VideoID, "failed", &errMsg); dbErr != nil {
					log.Printf("set failed status: %v", dbErr)
				}
			}
		}(task)
	}
}

func (w *Worker) process(ctx context.Context, task TranscodeTask) error {
	log.Printf("processing video %s", task.VideoID)

	if err := w.setStatus(ctx, task.VideoID, "processing", nil); err != nil {
		return fmt.Errorf("set processing status: %w", err)
	}

	workDir := filepath.Join(w.tempDir, task.VideoID)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(workDir); err != nil {
			log.Printf("cleanup work dir %s: %v", workDir, err)
		}
	}()

	localOriginal := filepath.Join(workDir, "original"+filepath.Ext(task.OriginalPath))
	if err := w.downloadFile(ctx, task.OriginalPath, localOriginal); err != nil {
		return fmt.Errorf("download original: %w", err)
	}
	w.setProgress(ctx, task.VideoID, 10)

	probe, err := ffmpeg.Probe(ctx, localOriginal)
	if err != nil {
		return fmt.Errorf("probe video: %w", err)
	}
	log.Printf("video %s: duration=%.1fs %dx%d", task.VideoID, probe.Duration, probe.Width, probe.Height)

	if err := w.updateMetadata(ctx, task.VideoID, probe.Duration, probe.Width, probe.Height); err != nil {
		return fmt.Errorf("update metadata: %w", err)
	}
	w.setProgress(ctx, task.VideoID, 15)

	hlsDir := filepath.Join(workDir, "hls")
	variants, err := ffmpeg.TranscodeHLS(ctx, localOriginal, hlsDir, probe.Width, probe.Height, w.hlsSegmentSeconds, probe.FrameRate, w.accel, w.codecs, w.qualities, len(probe.Audio) > 0, w.encodingConfig)
	if err != nil {
		return fmt.Errorf("transcode HLS: %w", err)
	}
	log.Printf("video %s transcoded: variants=%d", task.VideoID, len(variants))
	w.setProgress(ctx, task.VideoID, 65)

	extractedAudio, _ := ffmpeg.ExtractAudio(ctx, localOriginal, hlsDir, probe.Audio, w.hlsSegmentSeconds, w.encodingConfig)
	log.Printf("video %s audio tracks: extracted=%d/%d", task.VideoID, len(extractedAudio), len(probe.Audio))
	w.setProgress(ctx, task.VideoID, 72)

	extractedSubs, _ := ffmpeg.ExtractSubtitles(ctx, localOriginal, hlsDir, probe.Subtitles, probe.Duration)
	log.Printf("video %s subtitle tracks: extracted=%d/%d", task.VideoID, len(extractedSubs), len(probe.Subtitles))
	w.setProgress(ctx, task.VideoID, 78)

	thumbDir := filepath.Join(workDir, "thumbnails")
	previewPath := ""
	if task.PreviewOverride {
		log.Printf("video %s: using uploaded preview override", task.VideoID)
	} else {
		generatedPreview, err := ffmpeg.GeneratePreviewWithConfig(ctx, localOriginal, thumbDir, probe.Duration, w.thumbnailConfig)
		if err != nil {
			log.Printf("preview generation failed for %s: %v (non-fatal)", task.VideoID, err)
		} else {
			previewPath = generatedPreview
		}
	}

	spritePath, err := ffmpeg.GenerateSpriteWithConfig(ctx, localOriginal, thumbDir, probe.Duration, w.thumbnailConfig)
	if err != nil {
		log.Printf("sprite generation failed for %s: %v (non-fatal)", task.VideoID, err)
		spritePath = ""
	}
	if spritePath != "" {
		if err := ffmpeg.WriteImageStreamManifestWithConfig(hlsDir, spritePath, probe.Duration, w.thumbnailConfig); err != nil {
			log.Printf("image stream generation failed for %s: %v (non-fatal)", task.VideoID, err)
		}
	}
	w.setProgress(ctx, task.VideoID, 85)

	if err := ffmpeg.WriteMasterManifestWithConfig(hlsDir, variants, extractedAudio, extractedSubs, w.thumbnailConfig); err != nil {
		return fmt.Errorf("write master manifest: %w", err)
	}

	if err := w.uploadHLS(ctx, hlsDir, task.VideoID); err != nil {
		return fmt.Errorf("upload HLS: %w", err)
	}
	w.setProgress(ctx, task.VideoID, 95)

	if previewPath != "" {
		remotePath := fmt.Sprintf("thumbnails/%s/preview.jpg", task.VideoID)
		if err := w.seaweed.UploadFile(ctx, remotePath, previewPath); err != nil {
			log.Printf("upload preview for %s: %v (non-fatal)", task.VideoID, err)
		}
	}

	if spritePath != "" {
		remotePath := fmt.Sprintf("thumbnails/%s/sprite.jpg", task.VideoID)
		if err := w.seaweed.UploadFile(ctx, remotePath, spritePath); err != nil {
			log.Printf("upload sprite for %s: %v (non-fatal)", task.VideoID, err)
		}
	}

	w.setProgress(ctx, task.VideoID, 100)
	if err := w.setStatus(ctx, task.VideoID, "ready", nil); err != nil {
		return fmt.Errorf("set ready status: %w", err)
	}

	log.Printf("video %s processing complete", task.VideoID)
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

	for _, job := range jobs {
		sem <- struct{}{}
		wg.Add(1)
		go func(j uploadJob) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := w.seaweed.UploadFile(ctx, j.remotePath, j.localPath); err != nil {
				errc <- fmt.Errorf("upload %s: %w", j.remotePath, err)
			}
		}(job)
	}
	wg.Wait()
	close(errc)

	for err := range errc {
		return err
	}
	return nil
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
	return nil
}

func (w *Worker) setStatus(ctx context.Context, videoID, status string, errMsg *string) error {
	q := `UPDATE videos SET status = $1, error_message = $2 WHERE id = $3`
	_, err := w.db.Exec(ctx, q, status, errMsg, videoID)
	if err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	return nil
}

func (w *Worker) setProgress(ctx context.Context, videoID string, pct int) {
	if _, err := w.db.Exec(ctx, `UPDATE videos SET progress = $1 WHERE id = $2`, pct, videoID); err != nil {
		log.Printf("set progress %d%% for %s: %v", pct, videoID, err)
	}
}

func (w *Worker) updateMetadata(ctx context.Context, videoID string, duration float64, width, height int) error {
	q := `UPDATE videos SET duration = $1, width = $2, height = $3 WHERE id = $4`
	_, err := w.db.Exec(ctx, q, duration, width, height, videoID)
	if err != nil {
		return fmt.Errorf("update metadata: %w", err)
	}
	return nil
}
