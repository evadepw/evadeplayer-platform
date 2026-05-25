package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/evadeplayer/api/internal/model"
	"github.com/evadeplayer/api/internal/repository"
	"github.com/evadeplayer/api/internal/service"
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
			Title:     "Test",
			Status:    model.StatusReady,
			Duration:  &dur,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
	}
	svc := service.NewVideoService(store, testHLSSecret, "http://localhost/hls", service.SpriteConfig{})

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

func TestGetVideo_IncludesVersionsForPrimary(t *testing.T) {
	id := "primary-id"
	dur := 30.0
	store := &fakeVideoStore{
		video: &model.Video{
			ID:        id,
			Title:     "Primary",
			Status:    model.StatusReady,
			Duration:  &dur,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		versions: []*model.VideoVersion{
			{ID: "alt-1", Label: "RU dub", Status: model.StatusReady},
			{ID: "alt-2", Label: "EN dub", Status: model.StatusPending},
		},
	}
	svc := service.NewVideoService(store, testHLSSecret, "http://localhost/hls", service.SpriteConfig{})

	resp, err := svc.GetVideo(context.Background(), id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(resp.Versions))
	}
	if resp.Versions[0].PreviewURL == "" {
		t.Error("ready version must have preview URL")
	}
	if !strings.Contains(resp.Versions[0].PreviewURL, "/preview.jpg") {
		t.Errorf("ready version preview must point to preview.jpg, got %q", resp.Versions[0].PreviewURL)
	}
	if resp.Versions[1].PreviewURL != "" {
		t.Error("pending version must NOT have preview URL")
	}
}

func TestGetVideo_NoVersionsForAlternative(t *testing.T) {
	parentID := "parent-id"
	store := &fakeVideoStore{
		video: &model.Video{
			ID:        "alt-id",
			Title:     "RU dub",
			Status:    model.StatusReady,
			VersionOf: &parentID,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
	}
	svc := service.NewVideoService(store, testHLSSecret, "http://localhost/hls", service.SpriteConfig{})

	resp, err := svc.GetVideo(context.Background(), "alt-id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Versions) != 0 {
		t.Error("alternative version must NOT have nested versions")
	}
}

func TestGetVideo_Pending(t *testing.T) {
	store := &fakeVideoStore{
		video: &model.Video{ID: "v1", Status: model.StatusPending},
	}
	svc := service.NewVideoService(store, testHLSSecret, "http://localhost/hls", service.SpriteConfig{})

	resp, err := svc.GetVideo(context.Background(), "v1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ManifestURL != "" {
		t.Error("pending video must NOT have ManifestURL")
	}
}

// --- in-memory VideoStorer ---

type fakeVideoStore struct {
	video    *model.Video
	videos   []*model.Video
	versions []*model.VideoVersion
}

func (f *fakeVideoStore) CreateWithID(_ context.Context, v *model.Video) error {
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

func (f *fakeVideoStore) List(_ context.Context, limit, offset int) ([]*model.VideoListItem, int, error) {
	var items []*model.VideoListItem
	for i, v := range f.videos {
		if i < offset || len(items) >= limit {
			continue
		}
		items = append(items, &model.VideoListItem{
			ID:     v.ID,
			Title:  v.Title,
			Status: v.Status,
		})
	}
	return items, len(f.videos), nil
}

func (f *fakeVideoStore) FindVersionsByID(_ context.Context, _ string) ([]*model.VideoVersion, error) {
	return f.versions, nil
}

func (f *fakeVideoStore) UpdateStatus(_ context.Context, id string, status model.VideoStatus, errMsg *string) error {
	if f.video != nil && f.video.ID == id {
		f.video.Status = status
		f.video.ErrorMessage = errMsg
	}
	return nil
}
