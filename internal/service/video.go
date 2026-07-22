package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/evadepw/evadeplayer-platform/internal/model"
	"github.com/evadepw/evadeplayer-platform/internal/repository"
)

var _ VideoStorer = (*repository.VideoRepo)(nil)

const hlsTokenTTL = 4 * time.Hour

type VideoService struct {
	videoRepo      VideoStorer
	hlsTokenSecret []byte
	requireToken   bool
	publicBaseURL  string
}

func NewVideoService(videoRepo VideoStorer, hlsTokenSecret, publicHost string, requireToken bool) *VideoService {
	return &VideoService{
		videoRepo:      videoRepo,
		hlsTokenSecret: []byte(hlsTokenSecret),
		requireToken:   requireToken,
		publicBaseURL:  publicHost,
	}
}

type AudioTrackResponse struct {
	Index       int    `json:"index"`
	Language    string `json:"language,omitempty"`
	Title       string `json:"title,omitempty"`
	ManifestURL string `json:"manifest_url"`
}

type SubtitleTrackResponse struct {
	Index       int    `json:"index"`
	Language    string `json:"language,omitempty"`
	Title       string `json:"title,omitempty"`
	ManifestURL string `json:"manifest_url"`
}

type VideoResponse struct {
	*model.Video
	ManifestURL    string                  `json:"manifest_url,omitempty"`
	PreviewURL     string                  `json:"preview_url,omitempty"`
	AudioTracks    []AudioTrackResponse    `json:"audio_tracks,omitempty"`
	SubtitleTracks []SubtitleTrackResponse `json:"subtitle_tracks,omitempty"`
}

type TokenResponse struct {
	Token       string `json:"token"`
	Expires     int64  `json:"expires"`
	ManifestURL string `json:"manifest_url"`
}

// GetTokens returns a token for each requested ID. Videos that are not found
// or not yet ready have a nil entry in the result map.
func (s *VideoService) GetTokens(ctx context.Context, ids []string) (map[string]*TokenResponse, error) {
	videos, err := s.videoRepo.FindByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	expires := time.Now().Add(hlsTokenTTL).Unix()
	expiresStr := strconv.FormatInt(expires, 10)

	result := make(map[string]*TokenResponse, len(ids))
	for _, id := range ids {
		v, ok := videos[id]
		if !ok || v.Status != model.StatusReady {
			result[id] = nil
			continue
		}
		token := ComputeHLSToken(s.hlsTokenSecret, id, expiresStr)
		result[id] = &TokenResponse{
			Token:       token,
			Expires:     expires,
			ManifestURL: fmt.Sprintf("%s/hls-proxy/%s/master.m3u8?token=%s&expires=%s", s.publicBaseURL, id, token, expiresStr),
		}
	}
	return result, nil
}

func (s *VideoService) GetVideo(ctx context.Context, id string) (*VideoResponse, error) {
	v, err := s.videoRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	resp := &VideoResponse{Video: v}
	if v.Status == model.StatusReady {
		var tokenQuery string
		if s.requireToken {
			expires := time.Now().Add(hlsTokenTTL).Unix()
			expiresStr := strconv.FormatInt(expires, 10)
			token := ComputeHLSToken(s.hlsTokenSecret, id, expiresStr)
			tokenQuery = fmt.Sprintf("?token=%s&expires=%s", token, expiresStr)
		}
		resp.ManifestURL = fmt.Sprintf("%s/hls-proxy/%s/master.m3u8%s", s.publicBaseURL, id, tokenQuery)
		resp.PreviewURL = s.previewURL(id)
		resp.AudioTracks = s.buildAudioTracks(id, v.AudioTracksRaw, tokenQuery)
		resp.SubtitleTracks = s.buildSubtitleTracks(id, v.SubtitleTracksRaw, tokenQuery)
	}
	return resp, nil
}

func (s *VideoService) buildAudioTracks(videoID string, raw json.RawMessage, tokenQuery string) []AudioTrackResponse {
	if len(raw) == 0 {
		return nil
	}
	var tracks []struct {
		Index    int    `json:"index"`
		Language string `json:"language"`
		Title    string `json:"title"`
	}
	if err := json.Unmarshal(raw, &tracks); err != nil {
		return nil
	}
	out := make([]AudioTrackResponse, len(tracks))
	for i, t := range tracks {
		out[i] = AudioTrackResponse{
			Index:       t.Index,
			Language:    t.Language,
			Title:       t.Title,
			ManifestURL: fmt.Sprintf("%s/hls-proxy/%s/audio/%d/index.m3u8%s", s.publicBaseURL, videoID, t.Index, tokenQuery),
		}
	}
	return out
}

func (s *VideoService) buildSubtitleTracks(videoID string, raw json.RawMessage, tokenQuery string) []SubtitleTrackResponse {
	if len(raw) == 0 {
		return nil
	}
	var tracks []struct {
		Index    int    `json:"index"`
		Language string `json:"language"`
		Title    string `json:"title"`
	}
	if err := json.Unmarshal(raw, &tracks); err != nil {
		return nil
	}
	out := make([]SubtitleTrackResponse, len(tracks))
	for i, t := range tracks {
		out[i] = SubtitleTrackResponse{
			Index:       t.Index,
			Language:    t.Language,
			Title:       t.Title,
			ManifestURL: fmt.Sprintf("%s/hls-proxy/%s/subs/%d/index.m3u8%s", s.publicBaseURL, videoID, t.Index, tokenQuery),
		}
	}
	return out
}

func (s *VideoService) ListVideos(ctx context.Context, page, pageSize int) ([]*model.Video, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize
	return s.videoRepo.List(ctx, pageSize, offset)
}

func (s *VideoService) GetStatus(ctx context.Context, id string) (*model.Video, error) {
	return s.videoRepo.FindByID(ctx, id)
}

func (s *VideoService) GetSegments(ctx context.Context, id string) ([]byte, error) {
	v, err := s.videoRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return v.Segments, nil
}

func (s *VideoService) previewURL(videoID string) string {
	return fmt.Sprintf("%s/thumbnails/%s/preview.jpg", s.publicBaseURL, videoID)
}

// ComputeHLSToken computes HMAC-SHA256 for a video ID + expiry.
// Exported so both the service and the HLS handler use the same logic.
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

// GetStoryboard builds scrubbing cues from the sprite metadata the transcoder
// recorded for this exact video, so coordinates always match the real sprite.
// Videos without a stored storyboard (sprite generation failed or the video is
// not ready) return ErrNotFound.
func (s *VideoService) GetStoryboard(ctx context.Context, id string) ([]StoryboardCue, error) {
	v, err := s.videoRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if v.Status != model.StatusReady || v.Duration == nil || len(v.StoryboardRaw) == 0 {
		return nil, repository.ErrNotFound
	}

	var sb model.Storyboard
	if err := json.Unmarshal(v.StoryboardRaw, &sb); err != nil {
		return nil, fmt.Errorf("decode storyboard metadata: %w", err)
	}
	if sb.IntervalSeconds < 1 || sb.Columns < 1 || sb.TileCount < 1 {
		return nil, repository.ErrNotFound
	}

	duration := *v.Duration
	spriteURL := fmt.Sprintf("%s/thumbnails/%s/sprite.jpg", s.publicBaseURL, id)
	cues := make([]StoryboardCue, sb.TileCount)
	for i := range sb.TileCount {
		start := float64(i * sb.IntervalSeconds)
		end := float64((i + 1) * sb.IntervalSeconds)
		if end > duration {
			end = duration
		}
		cues[i] = StoryboardCue{
			URL:       spriteURL,
			StartTime: start,
			EndTime:   end,
			Width:     sb.TileWidth,
			Height:    sb.TileHeight,
			Coords:    StoryboardCoords{X: (i % sb.Columns) * sb.TileWidth, Y: (i / sb.Columns) * sb.TileHeight},
		}
	}
	return cues, nil
}
