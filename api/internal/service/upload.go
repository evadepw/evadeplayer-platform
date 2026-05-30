package service

import (
	"context"
	"fmt"
	"io"

	"github.com/google/uuid"

	"github.com/evadeplayer/api/internal/model"
)

type UploadService struct {
	videoRepo VideoStorer
	storage   BlobStorage
	producer  TaskEnqueuer
}

func NewUploadService(videoRepo VideoStorer, storage BlobStorage, producer TaskEnqueuer) *UploadService {
	return &UploadService{videoRepo: videoRepo, storage: storage, producer: producer}
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
	if err := s.videoRepo.CreateWithID(ctx, v); err != nil {
		_ = s.storage.Delete(context.Background(), originalPath)
		return nil, fmt.Errorf("create video record: %w", err)
	}

	task := &model.TranscodeTask{
		VideoID:      v.ID,
		OriginalPath: originalPath,
	}
	if err := s.producer.Enqueue(ctx, task); err != nil {
		errMsg := err.Error()
		_ = s.videoRepo.UpdateStatus(ctx, v.ID, model.StatusFailed, &errMsg)
		return nil, fmt.Errorf("enqueue transcode task: %w", err)
	}

	return v, nil
}
