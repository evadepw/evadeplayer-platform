package repository

import (
	"context"
	"errors"
	"fmt"

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

func (r *VideoRepo) CreateWithID(ctx context.Context, v *model.Video) error {
	q := `INSERT INTO videos (id, original_path, size_bytes, segments)
	      VALUES ($1, $2, $3, $4)
	      RETURNING status, created_at, updated_at`
	var seg interface{}
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
	v := &model.Video{}
	q := `SELECT id, status, progress, original_path,
	             duration, width, height, size_bytes, error_message,
	             segments, audio_tracks, subtitle_tracks, created_at, updated_at
	      FROM videos WHERE id = $1`
	err := r.db.QueryRow(ctx, q, id).Scan(
		&v.ID, &v.Status, &v.Progress, &v.OriginalPath,
		&v.Duration, &v.Width, &v.Height, &v.SizeBytes, &v.ErrorMessage,
		&v.Segments, &v.AudioTracksRaw, &v.SubtitleTracksRaw, &v.CreatedAt, &v.UpdatedAt,
	)
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
	q := `SELECT id, status, progress, original_path,
	             duration, width, height, size_bytes, error_message,
	             segments, audio_tracks, subtitle_tracks, created_at, updated_at
	      FROM videos WHERE id = ANY($1)`
	rows, err := r.db.Query(ctx, q, ids)
	if err != nil {
		return nil, fmt.Errorf("find videos by ids: %w", err)
	}
	defer rows.Close()

	result := make(map[string]*model.Video, len(ids))
	for rows.Next() {
		v := &model.Video{}
		if err := rows.Scan(
			&v.ID, &v.Status, &v.Progress, &v.OriginalPath,
			&v.Duration, &v.Width, &v.Height, &v.SizeBytes, &v.ErrorMessage,
			&v.Segments, &v.AudioTracksRaw, &v.SubtitleTracksRaw, &v.CreatedAt, &v.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan video row: %w", err)
		}
		result[v.ID] = v
	}
	return result, rows.Err()
}

func (r *VideoRepo) DeleteByID(ctx context.Context, id string) error {
	q := `DELETE FROM videos WHERE id = $1`
	tag, err := r.db.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("delete video: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *VideoRepo) SetSegments(ctx context.Context, id string, segments []byte) error {
	q := `UPDATE videos SET segments = $1 WHERE id = $2`
	_, err := r.db.Exec(ctx, q, segments, id)
	if err != nil {
		return fmt.Errorf("set segments: %w", err)
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
	_, err := r.db.Exec(ctx, q, status, errMsg, id)
	if err != nil {
		return fmt.Errorf("update video status: %w", err)
	}
	return nil
}

func (r *VideoRepo) UpdateMetadata(ctx context.Context, id string, duration float64, width, height int) error {
	q := `UPDATE videos SET duration = $1, width = $2, height = $3 WHERE id = $4`
	_, err := r.db.Exec(ctx, q, duration, width, height, id)
	if err != nil {
		return fmt.Errorf("update video metadata: %w", err)
	}
	return nil
}
