package model

import (
	"encoding/json"
	"time"
)

type VideoStatus string

const (
	StatusPending    VideoStatus = "pending"
	StatusProcessing VideoStatus = "processing"
	StatusReady      VideoStatus = "ready"
	StatusFailed     VideoStatus = "failed"
)

type Video struct {
	ID           string          `json:"id"`
	Status       VideoStatus     `json:"status"`
	Progress     int             `json:"progress"`
	OriginalPath string          `json:"-"`
	Duration     *float64        `json:"duration,omitempty"`
	Width        *int            `json:"width,omitempty"`
	Height       *int            `json:"height,omitempty"`
	SizeBytes    int64           `json:"size_bytes"`
	ErrorMessage *string         `json:"error_message,omitempty"`
	Segments     json.RawMessage `json:"segments,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type TranscodeTask struct {
	VideoID      string `json:"video_id"`
	OriginalPath string `json:"original_path"`
}
