package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/evadepw/evadeplayer-platform/internal/model"
	"github.com/evadepw/evadeplayer-platform/internal/repository"
	"github.com/evadepw/evadeplayer-platform/internal/service"
)

const testHLSSecret = "hls-secret-32-chars-minimum-ok!!"

// --- ComputeHLSToken ---

func TestComputeHLSToken_Deterministic(t *testing.T) {
	s := []byte(testHLSSecret)
	if service.ComputeHLSToken(s, "vid", "100") != service.ComputeHLSToken(s, "vid", "100") {
		t.Error("must be deterministic")
	}
}

func TestComputeHLSToken_DifferentVideoIDs(t *testing.T) {
	s := []byte(testHLSSecret)
	if service.ComputeHLSToken(s, "aaa", "100") == service.ComputeHLSToken(s, "bbb", "100") {
		t.Error("tokens for different video IDs must differ")
	}
}

func TestComputeHLSToken_DifferentExpiry(t *testing.T) {
	s := []byte(testHLSSecret)
	if service.ComputeHLSToken(s, "vid", "100") == service.ComputeHLSToken(s, "vid", "200") {
		t.Error("tokens for different expiry must differ")
	}
}

func TestComputeHLSToken_DifferentSecrets(t *testing.T) {
	t1 := service.ComputeHLSToken([]byte("secret-a"), "vid", "100")
	t2 := service.ComputeHLSToken([]byte("secret-b"), "vid", "100")
	if t1 == t2 {
		t.Error("tokens for different secrets must differ")
	}
}

func TestComputeHLSToken_IsHex64(t *testing.T) {
	tok := service.ComputeHLSToken([]byte(testHLSSecret), "v", "1")
	const hexChars = "0123456789abcdef"
	for _, c := range tok {
		if !strings.ContainsRune(hexChars, c) {
			t.Errorf("non-hex char in token: %c", c)
		}
	}
	if len(tok) != 64 {
		t.Errorf("expected 64-char SHA-256 hex, got %d", len(tok))
	}
}

// --- VideoService.GetVideo ---

func TestGetVideo_Ready(t *testing.T) {
	id := "test-video-id"
	dur := 30.0
	store := &fakeVideoStore{
		video: &model.Video{
			ID:        id,
			Status:    model.StatusReady,
			Duration:  &dur,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
	}
	svc := service.NewVideoService(store, testHLSSecret, "http://localhost", true)

	resp, err := svc.GetVideo(context.Background(), id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ManifestURL == "" {
		t.Error("ready video must have ManifestURL")
	}
	if !strings.Contains(resp.ManifestURL, id) {
		t.Error("ManifestURL must contain video ID")
	}
	if !strings.Contains(resp.ManifestURL, "token=") {
		t.Error("ManifestURL must contain token param")
	}
	if !strings.Contains(resp.ManifestURL, "expires=") {
		t.Error("ManifestURL must contain expires param")
	}
	if !strings.Contains(resp.PreviewURL, "/preview.jpg") {
		t.Errorf("PreviewURL must point to preview.jpg, got %q", resp.PreviewURL)
	}
}

func TestGetVideo_Pending(t *testing.T) {
	store := &fakeVideoStore{
		video: &model.Video{ID: "v1", Status: model.StatusPending},
	}
	svc := service.NewVideoService(store, testHLSSecret, "http://localhost", true)

	resp, err := svc.GetVideo(context.Background(), "v1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ManifestURL != "" {
		t.Error("pending video must NOT have ManifestURL")
	}
	if resp.PreviewURL != "" {
		t.Error("pending video must NOT have PreviewURL")
	}
}

func TestGetVideo_NotFound(t *testing.T) {
	store := &fakeVideoStore{}
	svc := service.NewVideoService(store, testHLSSecret, "http://localhost", true)

	_, err := svc.GetVideo(context.Background(), "no-such-id")
	if err == nil {
		t.Error("expected error for missing video")
	}
}

// --- VideoService.GetStoryboard ---

func TestGetStoryboard_FromStoredMetadata(t *testing.T) {
	id := "vid-storyboard"
	dur := 25.0
	store := &fakeVideoStore{
		video: &model.Video{
			ID:            id,
			Status:        model.StatusReady,
			Duration:      &dur,
			StoryboardRaw: []byte(`{"interval_seconds":10,"tile_width":320,"tile_height":180,"columns":2,"tile_count":3}`),
		},
	}
	svc := service.NewVideoService(store, testHLSSecret, "http://localhost", true)

	cues, err := svc.GetStoryboard(context.Background(), id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cues) != 3 {
		t.Fatalf("expected 3 cues, got %d", len(cues))
	}
	// Third tile wraps to the second row of a 2-column sprite.
	if cues[2].Coords.X != 0 || cues[2].Coords.Y != 180 {
		t.Errorf("cue 2 coords = (%d,%d), want (0,180)", cues[2].Coords.X, cues[2].Coords.Y)
	}
	// Last cue is clamped to the video duration.
	if cues[2].EndTime != dur {
		t.Errorf("last cue end = %v, want %v", cues[2].EndTime, dur)
	}
	if !strings.Contains(cues[0].URL, "/thumbnails/"+id+"/sprite.jpg") {
		t.Errorf("cue URL must point at the sprite, got %q", cues[0].URL)
	}
}

func TestGetStoryboard_NoMetadata(t *testing.T) {
	id := "vid-no-storyboard"
	dur := 25.0
	store := &fakeVideoStore{
		video: &model.Video{ID: id, Status: model.StatusReady, Duration: &dur},
	}
	svc := service.NewVideoService(store, testHLSSecret, "http://localhost", true)

	if _, err := svc.GetStoryboard(context.Background(), id); err == nil {
		t.Error("expected not-found error when no storyboard metadata is stored")
	}
}

// --- in-memory VideoStorer ---

type fakeVideoStore struct {
	video     *model.Video
	videos    []*model.Video
	createErr error
}

func (f *fakeVideoStore) CreateWithID(_ context.Context, v *model.Video) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.video = v
	return nil
}

func (f *fakeVideoStore) FindByID(_ context.Context, id string) (*model.Video, error) {
	if f.video != nil && f.video.ID == id {
		cp := *f.video
		return &cp, nil
	}
	return nil, repository.ErrNotFound
}

func (f *fakeVideoStore) List(_ context.Context, limit, offset int) ([]*model.Video, int, error) {
	var items []*model.Video
	for i, v := range f.videos {
		if i < offset || len(items) >= limit {
			continue
		}
		cp := *v
		items = append(items, &cp)
	}
	return items, len(f.videos), nil
}

func (f *fakeVideoStore) FindByIDs(_ context.Context, ids []string) (map[string]*model.Video, error) {
	result := make(map[string]*model.Video, len(ids))
	for _, id := range ids {
		if f.video != nil && f.video.ID == id {
			cp := *f.video
			result[id] = &cp
		}
	}
	return result, nil
}

func (f *fakeVideoStore) DeleteByID(_ context.Context, id string) error {
	if f.video != nil && f.video.ID == id {
		f.video = nil
	}
	return nil
}
