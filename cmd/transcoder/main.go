package main

import (
	"context"
	"log/slog"
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
	config.SetupLogging()

	cfg, err := config.LoadTranscoder()
	if err != nil {
		fatal("load config", err)
	}

	if err := os.MkdirAll(cfg.TempDir, 0o755); err != nil {
		fatal("create temp dir", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := pgxpool.New(ctx, cfg.DB.DSN())
	if err != nil {
		fatal("connect to postgres", err)
	}
	defer db.Close()

	if err := db.Ping(ctx); err != nil {
		fatal("ping postgres", err)
	}
	slog.Info("connected to postgres")

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
		slog.Info("shutting down transcoder")
		runCancel()
	}()

	w.Run(runCtx)
	slog.Info("transcoder stopped")
}

func fatal(msg string, err error) {
	slog.Error(msg, "error", err)
	os.Exit(1)
}
