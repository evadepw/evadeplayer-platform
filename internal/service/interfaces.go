package service

import (
	"context"
	"io"

	"github.com/evadepw/evadeplayer-platform/internal/model"
)

type VideoStorer interface {
	CreateWithID(ctx context.Context, v *model.Video) error
	FindByID(ctx context.Context, id string) (*model.Video, error)
	FindByIDs(ctx context.Context, ids []string) (map[string]*model.Video, error)
	List(ctx context.Context, limit, offset int) ([]*model.Video, int, error)
	DeleteByID(ctx context.Context, id string) error
}

type BlobStorage interface {
	Upload(ctx context.Context, filePath string, r io.Reader, contentType string) error
	Download(ctx context.Context, filePath string) (io.ReadCloser, error)
	Delete(ctx context.Context, filePath string) error
	DeleteDir(ctx context.Context, dirPath string) error
}
