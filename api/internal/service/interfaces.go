package service

import (
	"context"
	"io"

	"github.com/evadeplayer/api/internal/model"
)

type VideoStorer interface {
	CreateWithID(ctx context.Context, v *model.Video) error
	FindByID(ctx context.Context, id string) (*model.Video, error)
	List(ctx context.Context, limit, offset int) ([]*model.Video, int, error)
	UpdateStatus(ctx context.Context, id string, status model.VideoStatus, errMsg *string) error
	SetSegments(ctx context.Context, id string, segments []byte) error
}

type TaskEnqueuer interface {
	Enqueue(ctx context.Context, task *model.TranscodeTask) error
}

type BlobStorage interface {
	Upload(ctx context.Context, filePath string, r io.Reader, contentType string) error
	Delete(ctx context.Context, filePath string) error
}
