package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evadepw/evadeplayer-platform/internal/config"
	"github.com/evadepw/evadeplayer-platform/internal/repository"
	"github.com/evadepw/evadeplayer-platform/internal/storage"
	"github.com/evadepw/evadeplayer-platform/internal/worker"
)

func main() {
	cfg, err := config.LoadTranscoder()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	if err := os.MkdirAll(cfg.TempDir, 0o755); err != nil {
		log.Fatalf("create temp dir: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := pgxpool.New(ctx, cfg.DB.DSN())
	if err != nil {
		log.Fatalf("connect to postgres: %v", err)
	}
	defer db.Close()

	if err := db.Ping(ctx); err != nil {
		log.Fatalf("ping postgres: %v", err)
	}
	log.Println("connected to postgres")

	seaweed := storage.NewSeaweedFS(cfg.SeaweedFSFiler)
	videoRepo := repository.NewVideoRepo(db)

	w := worker.New(videoRepo, seaweed, worker.Config{
		TempDir:           cfg.TempDir,
		Concurrency:       cfg.Workers,
		MaxAttempts:       cfg.MaxAttempts,
		HLSSegmentSeconds: cfg.HLSSegmentSeconds,
		Accel:             cfg.Accel,
		Codecs:            cfg.Codecs,
		Qualities:         cfg.Qualities,
		Thumbnail:         cfg.Thumbnail,
		Encoding:          cfg.Encoding,
	})

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
		<-quit
		log.Println("shutting down transcoder...")
		runCancel()
	}()

	w.Run(runCtx)
	log.Println("transcoder stopped")
}
