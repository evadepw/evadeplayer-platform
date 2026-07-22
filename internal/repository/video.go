package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evadepw/evadeplayer-platform/internal/model"
)

type VideoRepo struct {
	db *pgxpool.Pool
}

func NewVideoRepo(db *pgxpool.Pool) *VideoRepo {
	return &VideoRepo{db: db}
}

const videoColumns = `id, status, progress, original_path,
       duration, width, height, size_bytes, error_message,
       segments, audio_tracks, subtitle_tracks, storyboard, created_at, updated_at`

func scanVideo(row pgx.Row) (*model.Video, error) {
	v := &model.Video{}
	err := row.Scan(
		&v.ID, &v.Status, &v.Progress, &v.OriginalPath,
		&v.Duration, &v.Width, &v.Height, &v.SizeBytes, &v.ErrorMessage,
		&v.Segments, &v.AudioTracksRaw, &v.SubtitleTracksRaw, &v.StoryboardRaw,
		&v.CreatedAt, &v.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return v, nil
}

func (r *VideoRepo) CreateWithID(ctx context.Context, v *model.Video) error {
	q := `INSERT INTO videos (id, original_path, size_bytes, segments)
	      VALUES ($1, $2, $3, $4)
	      RETURNING status, created_at, updated_at`
	var seg any
	if len(v.Segments) > 0 {
		seg = v.Segments
	}
	err := r.db.QueryRow(ctx, q, v.ID, v.OriginalPath, v.SizeBytes, seg).
		Scan(&v.Status, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create video: %w", err)
	}
	return nil
}

func (r *VideoRepo) FindByID(ctx context.Context, id string) (*model.Video, error) {
	q := `SELECT ` + videoColumns + ` FROM videos WHERE id = $1`
	v, err := scanVideo(r.db.QueryRow(ctx, q, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("find video by id: %w", err)
	}
	return v, nil
}

func (r *VideoRepo) FindByIDs(ctx context.Context, ids []string) (map[string]*model.Video, error) {
	if len(ids) == 0 {
		return map[string]*model.Video{}, nil
	}
	q := `SELECT ` + videoColumns + ` FROM videos WHERE id = ANY($1)`
	rows, err := r.db.Query(ctx, q, ids)
	if err != nil {
		return nil, fmt.Errorf("find videos by ids: %w", err)
	}
	defer rows.Close()

	result := make(map[string]*model.Video, len(ids))
	for rows.Next() {
		v, err := scanVideo(rows)
		if err != nil {
			return nil, fmt.Errorf("scan video row: %w", err)
		}
		result[v.ID] = v
	}
	return result, rows.Err()
}

func (r *VideoRepo) DeleteByID(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM videos WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete video: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *VideoRepo) List(ctx context.Context, limit, offset int) ([]*model.Video, int, error) {
	q := `SELECT id, status, progress, duration, width, height, size_bytes, error_message, created_at, updated_at,
	             COUNT(*) OVER() AS total
	      FROM videos ORDER BY created_at DESC LIMIT $1 OFFSET $2`
	rows, err := r.db.Query(ctx, q, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list videos: %w", err)
	}
	defer rows.Close()

	var total int
	var items []*model.Video
	for rows.Next() {
		item := &model.Video{}
		if err := rows.Scan(
			&item.ID, &item.Status, &item.Progress,
			&item.Duration, &item.Width, &item.Height, &item.SizeBytes, &item.ErrorMessage,
			&item.CreatedAt, &item.UpdatedAt,
			&total,
		); err != nil {
			return nil, 0, fmt.Errorf("scan video row: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("video rows error: %w", err)
	}
	return items, total, nil
}

func (r *VideoRepo) UpdateStatus(ctx context.Context, id string, status model.VideoStatus, errMsg *string) error {
	q := `UPDATE videos SET status = $1, error_message = $2 WHERE id = $3`
	if _, err := r.db.Exec(ctx, q, status, errMsg, id); err != nil {
		return fmt.Errorf("update video status: %w", err)
	}
	return nil
}

// --- Transcoding queue --------------------------------------------------------
//
// Rows in status 'pending' form the queue. A worker claims the oldest pending
// row with FOR UPDATE SKIP LOCKED (safe under any number of concurrent
// workers), then proves liveness by bumping heartbeat_at while it transcodes.
// Claims that stop heartbeating are requeued — or failed once the attempt
// budget is spent — by ReclaimStale, which every worker runs periodically.

// Job is a claimed transcoding task.
type Job struct {
	VideoID      string
	OriginalPath string
	Attempts     int
}

// ClaimNextPending atomically claims the oldest pending video for processing.
// Returns ErrNotFound when the queue is empty.
func (r *VideoRepo) ClaimNextPending(ctx context.Context) (*Job, error) {
	q := `UPDATE videos
	      SET status = 'processing', attempts = attempts + 1, progress = 0,
	          error_message = NULL, claimed_at = NOW(), heartbeat_at = NOW()
	      WHERE id = (
	          SELECT id FROM videos WHERE status = 'pending'
	          ORDER BY created_at
	          FOR UPDATE SKIP LOCKED
	          LIMIT 1
	      )
	      RETURNING id, original_path, attempts`
	job := &Job{}
	err := r.db.QueryRow(ctx, q).Scan(&job.VideoID, &job.OriginalPath, &job.Attempts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("claim pending video: %w", err)
	}
	return job, nil
}

// Heartbeat marks a claimed video as still being processed.
func (r *VideoRepo) Heartbeat(ctx context.Context, id string) error {
	q := `UPDATE videos SET heartbeat_at = NOW() WHERE id = $1 AND status = 'processing'`
	if _, err := r.db.Exec(ctx, q, id); err != nil {
		return fmt.Errorf("heartbeat: %w", err)
	}
	return nil
}

// Release returns a claimed video to the queue without spending an attempt
// (used on graceful shutdown, when the interruption is not the video's fault).
func (r *VideoRepo) Release(ctx context.Context, id string) error {
	q := `UPDATE videos
	      SET status = 'pending', attempts = GREATEST(attempts - 1, 0), progress = 0,
	          claimed_at = NULL, heartbeat_at = NULL
	      WHERE id = $1 AND status = 'processing'`
	if _, err := r.db.Exec(ctx, q, id); err != nil {
		return fmt.Errorf("release video: %w", err)
	}
	return nil
}

// Requeue returns a claimed video to the queue after a failed attempt,
// keeping the attempt spent so retries stay bounded.
func (r *VideoRepo) Requeue(ctx context.Context, id string) error {
	q := `UPDATE videos
	      SET status = 'pending', progress = 0, claimed_at = NULL, heartbeat_at = NULL
	      WHERE id = $1 AND status = 'processing'`
	if _, err := r.db.Exec(ctx, q, id); err != nil {
		return fmt.Errorf("requeue video: %w", err)
	}
	return nil
}

// ReclaimStale requeues processing videos whose heartbeat is older than
// staleAfter (their worker died); those already at maxAttempts are marked
// failed instead. Returns the ids that were requeued and failed.
func (r *VideoRepo) ReclaimStale(ctx context.Context, staleAfter time.Duration, maxAttempts int) (requeued, failed []string, err error) {
	collect := func(q string, args ...any) ([]string, error) {
		rows, err := r.db.Query(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		return ids, rows.Err()
	}

	requeued, err = collect(`
		UPDATE videos
		SET status = 'pending', progress = 0, claimed_at = NULL, heartbeat_at = NULL
		WHERE status = 'processing' AND heartbeat_at < NOW() - make_interval(secs => $1) AND attempts < $2
		RETURNING id`, staleAfter.Seconds(), maxAttempts)
	if err != nil {
		return nil, nil, fmt.Errorf("requeue stale videos: %w", err)
	}

	failed, err = collect(`
		UPDATE videos
		SET status = 'failed', claimed_at = NULL, heartbeat_at = NULL,
		    error_message = 'transcoding was interrupted repeatedly and the retry budget is exhausted'
		WHERE status = 'processing' AND heartbeat_at < NOW() - make_interval(secs => $1) AND attempts >= $2
		RETURNING id`, staleAfter.Seconds(), maxAttempts)
	if err != nil {
		return nil, nil, fmt.Errorf("fail stale videos: %w", err)
	}
	return requeued, failed, nil
}

// --- Worker result updates ----------------------------------------------------

func (r *VideoRepo) SetProgress(ctx context.Context, id string, pct int) error {
	if _, err := r.db.Exec(ctx, `UPDATE videos SET progress = $1 WHERE id = $2`, pct, id); err != nil {
		return fmt.Errorf("set progress: %w", err)
	}
	return nil
}

func (r *VideoRepo) SetMetadata(ctx context.Context, id string, duration float64, width, height int) error {
	q := `UPDATE videos SET duration = $1, width = $2, height = $3 WHERE id = $4`
	if _, err := r.db.Exec(ctx, q, duration, width, height, id); err != nil {
		return fmt.Errorf("set metadata: %w", err)
	}
	return nil
}

func (r *VideoRepo) SetTracks(ctx context.Context, id string, audio, subtitles []model.Track) error {
	audioJSON, err := json.Marshal(orEmpty(audio))
	if err != nil {
		return fmt.Errorf("marshal audio tracks: %w", err)
	}
	subJSON, err := json.Marshal(orEmpty(subtitles))
	if err != nil {
		return fmt.Errorf("marshal subtitle tracks: %w", err)
	}
	q := `UPDATE videos SET audio_tracks = $1, subtitle_tracks = $2 WHERE id = $3`
	if _, err := r.db.Exec(ctx, q, audioJSON, subJSON, id); err != nil {
		return fmt.Errorf("set tracks: %w", err)
	}
	return nil
}

func orEmpty(tracks []model.Track) []model.Track {
	if tracks == nil {
		return []model.Track{}
	}
	return tracks
}

func (r *VideoRepo) SetStoryboard(ctx context.Context, id string, sb model.Storyboard) error {
	data, err := json.Marshal(sb)
	if err != nil {
		return fmt.Errorf("marshal storyboard: %w", err)
	}
	if _, err := r.db.Exec(ctx, `UPDATE videos SET storyboard = $1 WHERE id = $2`, data, id); err != nil {
		return fmt.Errorf("set storyboard: %w", err)
	}
	return nil
}
