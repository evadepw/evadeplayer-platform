package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evadepw/evadeplayer-platform/internal/config"
	"github.com/evadepw/evadeplayer-platform/internal/handler"
	"github.com/evadepw/evadeplayer-platform/internal/repository"
	"github.com/evadepw/evadeplayer-platform/internal/service"
	"github.com/evadepw/evadeplayer-platform/internal/storage"
)

func main() {
	config.SetupLogging()

	cfg, err := config.LoadAPI()
	if err != nil {
		fatal("load config", err)
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
	videoSvc := service.NewVideoService(videoRepo, cfg.HLSTokenSecret, cfg.PublicHost, cfg.HLSRequireToken)
	uploadSvc := service.NewUploadService(videoRepo, seaweed)

	videoH := handler.NewVideoHandler(videoSvc)
	uploadH := handler.NewUploadHandler(uploadSvc, cfg.MaxUploadSize)
	hlsAuthH := handler.NewHLSAuthHandler(cfg.HLSTokenSecret, cfg.HLSRequireToken)
	hlsManifestH := handler.NewHLSManifestHandler(cfg.HLSTokenSecret, cfg.SeaweedFSFiler, cfg.HLSRequireToken)

	uploadMW := handler.ServiceKeyMiddleware(cfg.ServiceKey)
	var readMW func(http.Handler) http.Handler
	if cfg.ReadPublic {
		readMW = func(h http.Handler) http.Handler { return h }
		slog.Info("read access: public")
	} else {
		readMW = handler.ServiceKeyMiddleware(cfg.ServiceKey)
		slog.Info("read access: service key required")
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := db.Ping(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unhealthy"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("GET /openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "openapi.yaml")
	})

	mux.HandleFunc("GET /internal/validate-hls", hlsAuthH.ValidateToken)
	mux.HandleFunc("GET /hls-proxy/", hlsManifestH.ServeManifest)

	mux.Handle("POST /videos/upload", uploadMW(http.HandlerFunc(uploadH.Upload)))
	mux.Handle("DELETE /videos/{id}", uploadMW(http.HandlerFunc(uploadH.DeleteVideo)))
	mux.Handle("GET /videos/{id}/download", uploadMW(http.HandlerFunc(uploadH.DownloadOriginal)))
	mux.Handle("POST /videos/tokens", readMW(http.HandlerFunc(videoH.GetTokens)))
	mux.Handle("GET /videos", readMW(http.HandlerFunc(videoH.ListVideos)))
	mux.Handle("GET /videos/{id}", readMW(http.HandlerFunc(videoH.GetVideo)))
	mux.Handle("GET /videos/{id}/status", readMW(http.HandlerFunc(videoH.GetStatus)))
	mux.Handle("GET /videos/{id}/storyboard", readMW(http.HandlerFunc(videoH.GetStoryboard)))
	mux.Handle("GET /videos/{id}/segments", readMW(http.HandlerFunc(videoH.GetSegments)))

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%s", cfg.Port),
		Handler:      handler.CORSMiddleware(cfg.CORSOrigins)(mux),
		ReadTimeout:  15 * time.Minute,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		slog.Info("API listening", "port", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatal("listen", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	slog.Info("shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown", "error", err)
	}
	slog.Info("bye")
}

func fatal(msg string, err error) {
	slog.Error(msg, "error", err)
	os.Exit(1)
}
