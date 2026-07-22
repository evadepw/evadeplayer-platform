package service

import (
	"context"
	"fmt"
	"io"
	"log"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/evadepw/evadeplayer-platform/internal/model"
)

type UploadService struct {
	videoRepo VideoStorer
	storage   BlobStorage
}

func NewUploadService(videoRepo VideoStorer, storage BlobStorage) *UploadService {
	return &UploadService{videoRepo: videoRepo, storage: storage}
}

type UploadInput struct {
	FileExt  string
	Size     int64
	Reader   io.Reader
	Segments []byte // optional JSON
}

func (s *UploadService) Upload(ctx context.Context, in *UploadInput) (*model.Video, error) {
	videoID := uuid.New().String()
	originalPath := fmt.Sprintf("originals/%s/original%s", videoID, in.FileExt)

	if err := s.storage.Upload(ctx, originalPath, in.Reader, "application/octet-stream"); err != nil {
		return nil, fmt.Errorf("upload original to storage: %w", err)
	}

	v := &model.Video{
		ID:           videoID,
		OriginalPath: originalPath,
		SizeBytes:    in.Size,
		Segments:     in.Segments,
	}
	// Creating the row in status 'pending' is what enqueues the video:
	// workers poll PostgreSQL for pending rows. If the insert fails, remove
	// the uploaded blob so storage is not left with an orphan.
	if err := s.videoRepo.CreateWithID(ctx, v); err != nil {
		_ = s.storage.Delete(context.WithoutCancel(ctx), originalPath)
		return nil, fmt.Errorf("create video record: %w", err)
	}

	return v, nil
}

type DownloadResult struct {
	Body     io.ReadCloser
	Size     int64
	Filename string
}

func (s *UploadService) DownloadOriginal(ctx context.Context, id string) (*DownloadResult, error) {
	v, err := s.videoRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	body, err := s.storage.Download(ctx, v.OriginalPath)
	if err != nil {
		return nil, fmt.Errorf("download original: %w", err)
	}
	ext := filepath.Ext(v.OriginalPath)
	return &DownloadResult{
		Body:     body,
		Size:     v.SizeBytes,
		Filename: "original" + ext,
	}, nil
}

func (s *UploadService) DeleteVideo(ctx context.Context, id string) error {
	if err := s.videoRepo.DeleteByID(ctx, id); err != nil {
		return err
	}
	// Best-effort: log failures but don't fail the request.
	for _, dir := range []string{
		"originals/" + id,
		"hls/" + id,
		"thumbnails/" + id,
	} {
		if err := s.storage.DeleteDir(ctx, dir); err != nil {
			log.Printf("[delete] failed to remove %s: %v", dir, err)
		}
	}
	return nil
}
