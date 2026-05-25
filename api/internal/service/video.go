package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"strconv"
	"time"

	"github.com/evadeplayer/api/internal/model"
	"github.com/evadeplayer/api/internal/repository"
)

var _ VideoStorer = (*repository.VideoRepo)(nil) // compile-time interface check

const hlsTokenTTL = 4 * time.Hour

type SpriteConfig struct {
	IntervalSeconds int
	Width           int
	Height          int
	Columns         int
}

type VideoService struct {
	videoRepo      VideoStorer
	hlsTokenSecret []byte
	publicBaseURL  string // scheme+host, e.g. https://example.com
	sprite         SpriteConfig
}

func NewVideoService(videoRepo VideoStorer, hlsTokenSecret, publicHost string, sprite SpriteConfig) *VideoService {
	return &VideoService{
		videoRepo:      videoRepo,
		hlsTokenSecret: []byte(hlsTokenSecret),
		publicBaseURL:  publicHost,
		sprite:         sprite,
	}
}

type VideoResponse struct {
	*model.Video
	ManifestURL string               `json:"manifest_url,omitempty"`
	PreviewURL  string               `json:"preview_url,omitempty"`
	Versions    []model.VideoVersion `json:"versions,omitempty"`
}

func (s *VideoService) GetVideo(ctx context.Context, id string) (*VideoResponse, error) {
	v, err := s.videoRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	resp := &VideoResponse{Video: v}
	if v.Status == model.StatusReady {
		resp.ManifestURL = s.signedManifestURL(id)
		resp.PreviewURL = s.previewURL(id)
	}
	// Fetch alternative versions only for primary videos (not for versions themselves).
	if v.VersionOf == nil {
		versions, err := s.videoRepo.FindVersionsByID(ctx, id)
		if err != nil {
			log.Printf("get video %s: fetch versions: %v", id, err)
		}
		for _, ver := range versions {
			if ver.Status == model.StatusReady {
				ver.PreviewURL = s.previewURL(ver.ID)
			}
			resp.Versions = append(resp.Versions, *ver)
		}
	}
	return resp, nil
}

func (s *VideoService) ListVideos(ctx context.Context, page, pageSize int) ([]*model.VideoListItem, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize
	items, total, err := s.videoRepo.List(ctx, pageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	for _, item := range items {
		if item.Status == model.StatusReady {
			item.PreviewURL = s.previewURL(item.ID)
		}
	}
	return items, total, nil
}

func (s *VideoService) previewURL(videoID string) string {
	return fmt.Sprintf("%s/thumbnails/%s/preview.jpg", s.publicBaseURL, videoID)
}

func (s *VideoService) GetStatus(ctx context.Context, id string) (*model.Video, error) {
	return s.videoRepo.FindByID(ctx, id)
}

// Token is scoped to the video ID and covers master + quality manifests + chunks.
func (s *VideoService) signedManifestURL(videoID string) string {
	expires := time.Now().Add(hlsTokenTTL).Unix()
	expiresStr := strconv.FormatInt(expires, 10)
	token := ComputeHLSToken(s.hlsTokenSecret, videoID, expiresStr)
	return fmt.Sprintf("%s/hls-proxy/%s/master.m3u8?token=%s&expires=%s",
		s.publicBaseURL, videoID, token, expiresStr)
}

// ComputeHLSToken computes HMAC for a video ID + expiry.
// Exported so both the service and the handler use the same logic.
func ComputeHLSToken(secret []byte, videoID, expires string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(videoID + ":" + expires))
	return hex.EncodeToString(mac.Sum(nil))
}

type StoryboardCue struct {
	URL       string           `json:"url"`
	StartTime float64          `json:"start_time"`
	EndTime   float64          `json:"end_time"`
	Width     int              `json:"width"`
	Height    int              `json:"height"`
	Coords    StoryboardCoords `json:"coords"`
}

type StoryboardCoords struct {
	X int `json:"x"`
	Y int `json:"y"`
}

func (s *VideoService) GetStoryboard(ctx context.Context, id string) ([]StoryboardCue, error) {
	v, err := s.videoRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if v.Status != model.StatusReady || v.Duration == nil {
		return nil, repository.ErrNotFound
	}

	cfg := s.sprite
	if cfg.IntervalSeconds < 1 {
		cfg.IntervalSeconds = 10
	}
	if cfg.Width < 1 {
		cfg.Width = 320
	}
	if cfg.Height < 1 {
		cfg.Height = 180
	}
	if cfg.Columns < 1 {
		cfg.Columns = 10
	}

	duration := *v.Duration
	count := int(math.Ceil(duration / float64(cfg.IntervalSeconds)))
	if count < 1 {
		count = 1
	}

	spriteURL := fmt.Sprintf("%s/thumbnails/%s/sprite.jpg", s.publicBaseURL, id)
	cues := make([]StoryboardCue, count)
	for i := range count {
		start := float64(i * cfg.IntervalSeconds)
		end := float64((i + 1) * cfg.IntervalSeconds)
		if end > duration {
			end = duration
		}
		col := i % cfg.Columns
		row := i / cfg.Columns
		cues[i] = StoryboardCue{
			URL:       spriteURL,
			StartTime: start,
			EndTime:   end,
			Width:     cfg.Width,
			Height:    cfg.Height,
			Coords:    StoryboardCoords{X: col * cfg.Width, Y: row * cfg.Height},
		}
	}
	return cues, nil
}
