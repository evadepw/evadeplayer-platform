package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/evadepw/evadeplayer-platform/internal/handler"
	"github.com/evadepw/evadeplayer-platform/internal/model"
	"github.com/evadepw/evadeplayer-platform/internal/repository"
	"github.com/evadepw/evadeplayer-platform/internal/service"
)

// --- in-memory video store ---

type memVideos struct {
	byID map[string]*model.Video
	list []*model.Video
}

func newMemVideos(items ...*model.Video) *memVideos {
	m := &memVideos{byID: make(map[string]*model.Video)}
	for _, v := range items {
		cp := *v
		m.byID[v.ID] = &cp
		m.list = append(m.list, &cp)
	}
	return m
}

func (m *memVideos) CreateWithID(_ context.Context, v *model.Video) error {
	cp := *v
	cp.CreatedAt = time.Now()
	cp.UpdatedAt = time.Now()
	cp.Status = model.StatusPending
	m.byID[v.ID] = &cp
	m.list = append(m.list, &cp)
	return nil
}

func (m *memVideos) FindByID(_ context.Context, id string) (*model.Video, error) {
	v, ok := m.byID[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (m *memVideos) List(_ context.Context, limit, offset int) ([]*model.Video, int, error) {
	var out []*model.Video
	for i, v := range m.list {
		if i < offset {
			continue
		}
		if len(out) >= limit {
			break
		}
		cp := *v
		out = append(out, &cp)
	}
	return out, len(m.list), nil
}

func (m *memVideos) UpdateStatus(_ context.Context, id string, st model.VideoStatus, msg *string) error {
	if v, ok := m.byID[id]; ok {
		v.Status = st
		v.ErrorMessage = msg
	}
	return nil
}

func (m *memVideos) SetSegments(_ context.Context, id string, segments []byte) error {
	if v, ok := m.byID[id]; ok {
		v.Segments = segments
	}
	return nil
}

func (m *memVideos) FindByIDs(_ context.Context, ids []string) (map[string]*model.Video, error) {
	result := make(map[string]*model.Video, len(ids))
	for _, id := range ids {
		if v, ok := m.byID[id]; ok {
			cp := *v
			result[id] = &cp
		}
	}
	return result, nil
}

func (m *memVideos) DeleteByID(_ context.Context, id string) error {
	delete(m.byID, id)
	return nil
}

func sampleVideo(id string, status model.VideoStatus) *model.Video {
	dur := 30.0
	return &model.Video{
		ID:        id,
		Status:    status,
		Duration:  &dur,
		SizeBytes: 1024,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

func newVideoHandler(videos ...*model.Video) *handler.VideoHandler {
	store := newMemVideos(videos...)
	svc := service.NewVideoService(store, "hls-secret-32-chars-minimum-ok!!", "http://localhost", true, service.SpriteConfig{})
	return handler.NewVideoHandler(svc)
}

// --- GetVideo ---

func TestVideoHandler_GetVideo_Ready(t *testing.T) {
	v := sampleVideo("vid-123", model.StatusReady)
	h := newVideoHandler(v)

	req := httptest.NewRequest(http.MethodGet, "/videos/vid-123", nil)
	req.SetPathValue("id", "vid-123")
	rr := httptest.NewRecorder()
	h.GetVideo(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body)
	}
	var resp map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if resp["manifest_url"] == nil {
		t.Error("ready video must include manifest_url")
	}
	if resp["preview_url"] == nil {
		t.Error("ready video must include preview_url")
	}
}

func TestVideoHandler_GetVideo_Pending(t *testing.T) {
	v := sampleVideo("vid-456", model.StatusPending)
	h := newVideoHandler(v)

	req := httptest.NewRequest(http.MethodGet, "/videos/vid-456", nil)
	req.SetPathValue("id", "vid-456")
	rr := httptest.NewRecorder()
	h.GetVideo(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if resp["manifest_url"] != nil {
		t.Error("pending video must NOT include manifest_url")
	}
}

func TestVideoHandler_GetVideo_NotFound(t *testing.T) {
	h := newVideoHandler()
	req := httptest.NewRequest(http.MethodGet, "/videos/no-such-id", nil)
	req.SetPathValue("id", "no-such-id")
	rr := httptest.NewRecorder()
	h.GetVideo(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

// --- ListVideos ---

func TestVideoHandler_ListVideos(t *testing.T) {
	h := newVideoHandler(
		sampleVideo("v1", model.StatusReady),
		sampleVideo("v2", model.StatusPending),
	)
	req := httptest.NewRequest(http.MethodGet, "/videos?page=1&page_size=10", nil)
	rr := httptest.NewRecorder()
	h.ListVideos(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Total != 2 {
		t.Errorf("expected total=2, got %d", resp.Total)
	}
	if len(resp.Items) != 2 {
		t.Errorf("expected 2 items, got %d", len(resp.Items))
	}
}

func TestVideoHandler_ListVideos_Empty(t *testing.T) {
	h := newVideoHandler()
	req := httptest.NewRequest(http.MethodGet, "/videos", nil)
	rr := httptest.NewRecorder()
	h.ListVideos(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

// --- GetStatus ---

func TestVideoHandler_GetStatus(t *testing.T) {
	v := sampleVideo("vid-789", model.StatusProcessing)
	h := newVideoHandler(v)

	req := httptest.NewRequest(http.MethodGet, "/videos/vid-789/status", nil)
	req.SetPathValue("id", "vid-789")
	rr := httptest.NewRecorder()
	h.GetStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body)
	}
	var resp map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if resp["status"] != string(model.StatusProcessing) {
		t.Errorf("expected status=processing, got %v", resp["status"])
	}
}
