package queue

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/evadepw/evadeplayer-platform/internal/model"
)

type Producer struct {
	rdb      *redis.Client
	queueKey string
}

func NewProducer(rdb *redis.Client, queueKey string) *Producer {
	return &Producer{rdb: rdb, queueKey: queueKey}
}

func (p *Producer) Enqueue(ctx context.Context, task *model.TranscodeTask) error {
	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal task: %w", err)
	}
	if err := p.rdb.LPush(ctx, p.queueKey, data).Err(); err != nil {
		return fmt.Errorf("enqueue task: %w", err)
	}
	return nil
}
