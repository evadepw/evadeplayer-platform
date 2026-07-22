package service_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/evadepw/evadeplayer-platform/internal/model"
	"github.com/evadepw/evadeplayer-platform/internal/service"
)

type uploadRecord struct {
	path        string
	contentType string
}

type fakeStorage struct {
	uploadErr error
	uploads   []uploadRecord
	deletes   []string
}

func (f *fakeStorage) Upload(_ context.Context, path string, _ io.Reader, contentType string) error {
	f.uploads = append(f.uploads, uploadRecord{path: path, contentType: contentType})
	return f.uploadErr
}
func (f *fakeStorage) Download(_ context.Context, path string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (f *fakeStorage) Delete(_ context.Context, path string) error {
	f.deletes = append(f.deletes, path)
	return nil
}
func (f *fakeStorage) DeleteDir(_ context.Context, path string) error {
	f.deletes = append(f.deletes, path)
	return nil
}

type fakeProducer struct {
	task *model.TranscodeTask
}

func (f *fakeProducer) Enqueue(_ context.Context, task *model.TranscodeTask) error {
	f.task = task
	return nil
}

func newUploadSvc(videos *fakeVideoStore) *service.UploadService {
	return service.NewUploadService(videos, &fakeStorage{}, &fakeProducer{})
}

func uploadInput(overrides ...func(*service.UploadInput)) *service.UploadInput {
	in := &service.UploadInput{
		FileExt: ".mp4",
		Size:    1024,
		Reader:  strings.NewReader("fake video data"),
	}
	for _, fn := range overrides {
		fn(in)
	}
	return in
}

func TestUploadService_Upload(t *testing.T) {
	svc := newUploadSvc(&fakeVideoStore{})

	v, err := svc.Upload(context.Background(), uploadInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.ID == "" {
		t.Error("video ID must be set")
	}
}

func TestUploadService_Upload_StorageError(t *testing.T) {
	videos := &fakeVideoStore{}
	svc := service.NewUploadService(videos, &fakeStorage{uploadErr: errors.New("disk full")}, &fakeProducer{})

	_, err := svc.Upload(context.Background(), uploadInput())
	if err == nil {
		t.Error("expected error on storage failure")
	}
}

func TestUploadService_Upload_EnqueueError(t *testing.T) {
	videos := &fakeVideoStore{}
	producer := &errorProducer{}
	svc := service.NewUploadService(videos, &fakeStorage{}, producer)

	_, err := svc.Upload(context.Background(), uploadInput())
	if err == nil {
		t.Error("expected error on enqueue failure")
	}
	if videos.video == nil {
		t.Fatal("video record must be created before enqueue attempt")
	}
	if videos.video.Status != model.StatusFailed {
		t.Errorf("expected status failed after enqueue error, got %s", videos.video.Status)
	}
}

type errorProducer struct{}

func (e *errorProducer) Enqueue(_ context.Context, _ *model.TranscodeTask) error {
	return errors.New("queue unavailable")
}
