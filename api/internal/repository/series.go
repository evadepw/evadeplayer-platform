package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evadeplayer/api/internal/model"
)

type SeriesRepo struct {
	db *pgxpool.Pool
}

func NewSeriesRepo(db *pgxpool.Pool) *SeriesRepo {
	return &SeriesRepo{db: db}
}

func (r *SeriesRepo) Create(ctx context.Context, s *model.Series) error {
	q := `INSERT INTO series (user_id, title, description)
	      VALUES ($1, $2, $3)
	      RETURNING id, created_at, updated_at`
	err := r.db.QueryRow(ctx, q, s.UserID, s.Title, s.Description).
		Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create series: %w", err)
	}
	return nil
}

func (r *SeriesRepo) FindByID(ctx context.Context, id string) (*model.Series, error) {
	s := &model.Series{}
	q := `SELECT s.id, s.user_id, s.title, s.description, s.created_at, s.updated_at,
	             COUNT(v.id) AS episode_count
	      FROM series s
	      LEFT JOIN videos v ON v.series_id = s.id
	      WHERE s.id = $1
	      GROUP BY s.id`
	err := r.db.QueryRow(ctx, q, id).Scan(
		&s.ID, &s.UserID, &s.Title, &s.Description, &s.CreatedAt, &s.UpdatedAt, &s.EpisodeCount,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("find series by id: %w", err)
	}
	return s, nil
}

func (r *SeriesRepo) FindByIDWithEpisodes(ctx context.Context, id string) (*model.SeriesDetail, error) {
	s, err := r.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	// Primary episodes only (version_of IS NULL).
	q := `SELECT id, title, season_number, episode_number, duration, status
	      FROM videos
	      WHERE series_id = $1 AND version_of IS NULL
	      ORDER BY season_number NULLS LAST, episode_number NULLS LAST, created_at`
	rows, err := r.db.Query(ctx, q, id)
	if err != nil {
		return nil, fmt.Errorf("query episodes: %w", err)
	}
	defer rows.Close()

	detail := &model.SeriesDetail{
		Series:   *s,
		Seasons:  []model.Season{},
		Episodes: []model.Episode{},
	}
	seasonIdx := map[int]int{}
	var episodeIDs []string

	for rows.Next() {
		var ep model.Episode
		var seasonNum *int
		if err := rows.Scan(&ep.ID, &ep.Title, &seasonNum, &ep.EpisodeNumber, &ep.Duration, &ep.Status); err != nil {
			return nil, fmt.Errorf("scan episode: %w", err)
		}
		ep.SeasonNumber = seasonNum
		episodeIDs = append(episodeIDs, ep.ID)

		if seasonNum == nil {
			detail.Episodes = append(detail.Episodes, ep)
		} else {
			idx, ok := seasonIdx[*seasonNum]
			if !ok {
				idx = len(detail.Seasons)
				detail.Seasons = append(detail.Seasons, model.Season{SeasonNumber: *seasonNum})
				seasonIdx[*seasonNum] = idx
			}
			detail.Seasons[idx].Episodes = append(detail.Seasons[idx].Episodes, ep)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("episodes rows error: %w", err)
	}

	if len(episodeIDs) > 0 {
		if err := r.attachVersions(ctx, episodeIDs, &detail.Seasons, &detail.Episodes); err != nil {
			return nil, err
		}
	}
	return detail, nil
}

// attachVersions fetches alternative versions for a set of episode IDs and
// attaches them to the matching episodes in seasons and flat episode slices.
func (r *SeriesRepo) attachVersions(ctx context.Context, ids []string, seasons *[]model.Season, episodes *[]model.Episode) error {
	q := `SELECT version_of, id, COALESCE(version_label, ''), COALESCE(version_description, ''), status
	      FROM videos WHERE version_of = ANY($1::uuid[]) ORDER BY created_at`
	rows, err := r.db.Query(ctx, q, ids)
	if err != nil {
		return fmt.Errorf("query versions: %w", err)
	}
	defer rows.Close()

	byParent := map[string][]model.VideoVersion{}
	for rows.Next() {
		var parentID string
		var v model.VideoVersion
		if err := rows.Scan(&parentID, &v.ID, &v.Label, &v.Description, &v.Status); err != nil {
			return fmt.Errorf("scan version: %w", err)
		}
		byParent[parentID] = append(byParent[parentID], v)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("versions rows error: %w", err)
	}
	if len(byParent) == 0 {
		return nil
	}

	for si := range *seasons {
		for ei := range (*seasons)[si].Episodes {
			ep := &(*seasons)[si].Episodes[ei]
			ep.Versions = byParent[ep.ID]
		}
	}
	for ei := range *episodes {
		(*episodes)[ei].Versions = byParent[(*episodes)[ei].ID]
	}
	return nil
}

func (r *SeriesRepo) List(ctx context.Context, limit, offset int) ([]*model.Series, int, error) {
	q := `SELECT s.id, s.user_id, s.title, s.description, s.created_at, s.updated_at,
	             COUNT(v.id) AS episode_count,
	             COUNT(*) OVER() AS total
	      FROM series s
	      LEFT JOIN videos v ON v.series_id = s.id
	      GROUP BY s.id
	      ORDER BY s.created_at DESC
	      LIMIT $1 OFFSET $2`
	rows, err := r.db.Query(ctx, q, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list series: %w", err)
	}
	defer rows.Close()

	var total int
	var items []*model.Series
	for rows.Next() {
		s := &model.Series{}
		if err := rows.Scan(&s.ID, &s.UserID, &s.Title, &s.Description, &s.CreatedAt, &s.UpdatedAt, &s.EpisodeCount, &total); err != nil {
			return nil, 0, fmt.Errorf("scan series row: %w", err)
		}
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("series rows error: %w", err)
	}
	return items, total, nil
}
