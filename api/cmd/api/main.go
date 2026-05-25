package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/evadeplayer/api/internal/config"
	"github.com/evadeplayer/api/internal/handler"
	"github.com/evadeplayer/api/internal/queue"
	"github.com/evadeplayer/api/internal/repository"
	"github.com/evadeplayer/api/internal/service"
	"github.com/evadeplayer/api/internal/storage"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := pgxpool.New(ctx, cfg.DSN())
	if err != nil {
		log.Fatalf("connect to postgres: %v", err)
	}
	defer db.Close()

	if err := db.Ping(ctx); err != nil {
		log.Fatalf("ping postgres: %v", err)
	}
	log.Println("connected to postgres")

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("connect to redis: %v", err)
	}
	log.Println("connected to redis")

	seaweed := storage.NewSeaweedFS(cfg.SeaweedFSFiler)

	videoRepo := repository.NewVideoRepo(db)
	seriesRepo := repository.NewSeriesRepo(db)
	producer := queue.NewProducer(rdb, cfg.RedisQueueKey)

	videoSvc := service.NewVideoService(videoRepo, cfg.HLSTokenSecret, cfg.PublicHost, service.SpriteConfig{
		IntervalSeconds: cfg.SpriteIntervalSeconds,
		Width:           cfg.SpriteWidth,
		Height:          cfg.SpriteHeight,
		Columns:         cfg.SpriteColumns,
	})
	seriesSvc := service.NewSeriesService(seriesRepo, cfg.PublicHost)
	uploadSvc := service.NewUploadService(videoRepo, seriesRepo, seaweed, producer)

	videoH := handler.NewVideoHandler(videoSvc)
	uploadH := handler.NewUploadHandler(uploadSvc, cfg.MaxUploadSize)
	seriesH := handler.NewSeriesHandler(seriesSvc)
	hlsAuthH := handler.NewHLSAuthHandler(cfg.HLSTokenSecret)
	hlsManifestH := handler.NewHLSManifestHandler(cfg.HLSTokenSecret, cfg.SeaweedFSFiler)

	mux := http.NewServeMux()

	// Upload and read auth middleware depend on the auth mode.
	var uploadAuth, readAuth func(http.Handler) http.Handler

	switch cfg.AuthMode {
	case "backend":
		log.Println("auth mode: backend (BFF service key)")
		uploadAuth = handler.BFFMiddleware(cfg.ServiceKey)
		readAuth = func(h http.Handler) http.Handler { return h }

	default: // "standalone"
		log.Println("auth mode: standalone (JWT)")
		userRepo := repository.NewUserRepo(db)
		authSvc := service.NewAuthService(userRepo, cfg.JWTSecret)
		authH := handler.NewAuthHandler(authSvc)

		switch cfg.UploadAuth {
		case "service_key":
			uploadAuth = handler.ServiceKeyMiddleware(cfg.ServiceKey)
		case "any":
			uploadAuth = handler.AnyAuthMiddleware(authSvc, cfg.ServiceKey)
		default: // "jwt"
			uploadAuth = handler.AuthMiddleware(authSvc)
		}
		readAuth = handler.OptionalAuthMiddleware(authSvc, cfg.AuthRequired)

		if cfg.AllowRegistration {
			mux.HandleFunc("POST /auth/register", authH.Register)
		}
		mux.HandleFunc("POST /auth/login", authH.Login)
	}

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := db.Ping(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"status":"unhealthy"}`))
			return
		}
		w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("GET /openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "openapi.yaml")
	})

	mux.HandleFunc("GET /internal/validate-hls", hlsAuthH.ValidateToken)
	mux.HandleFunc("GET /hls-proxy/", hlsManifestH.ServeManifest)

	mux.Handle("POST /series", uploadAuth(http.HandlerFunc(seriesH.CreateSeries)))
	mux.Handle("GET /series", readAuth(http.HandlerFunc(seriesH.ListSeries)))
	mux.Handle("GET /series/{id}", readAuth(http.HandlerFunc(seriesH.GetSeries)))

	mux.Handle("POST /videos/upload", uploadAuth(http.HandlerFunc(uploadH.Upload)))
	mux.Handle("GET /videos", readAuth(http.HandlerFunc(videoH.ListVideos)))

	mux.Handle("GET /videos/{id}/status", readAuth(http.HandlerFunc(videoH.GetStatus)))
	mux.Handle("GET /videos/{id}/storyboard", readAuth(http.HandlerFunc(videoH.GetStoryboard)))
	mux.Handle("GET /videos/{id}", readAuth(http.HandlerFunc(videoH.GetVideo)))

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%s", cfg.APIPort),
		Handler:      handler.CORSMiddleware(cfg.CORSOrigins)(mux),
		ReadTimeout:  15 * time.Minute, // large uploads
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("API listening on :%s (mode: %s)", cfg.APIPort, cfg.AuthMode)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	log.Println("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown error: %v", err)
	}
	log.Println("bye")
}
