package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/media-service/media-platform/internal/config"
	"github.com/media-service/media-platform/internal/handler"
	"github.com/media-service/media-platform/internal/live"
	"github.com/media-service/media-platform/internal/media"
	"github.com/media-service/media-platform/internal/messaging"
	"github.com/media-service/media-platform/internal/repository"
	"github.com/media-service/media-platform/internal/storage"
	"github.com/media-service/media-platform/internal/stun"
	"github.com/media-service/media-platform/internal/telemetry"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// --- OpenTelemetry ---
	otelShutdown, err := telemetry.Init(context.Background())
	if err != nil {
		log.Printf("telemetry init (non-fatal): %v", err)
	} else {
		log.Println("✓ OpenTelemetry initialized")
	}

	// --- Infrastructure ---
	store, err := storage.New(cfg.MinIO)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	log.Println("✓ MinIO connected")

	repo, err := repository.New(cfg.Postgres.DSN())
	if err != nil {
		log.Fatalf("repository: %v", err)
	}
	defer repo.Close()
	log.Println("✓ PostgreSQL connected")

	producer := messaging.NewProducer(cfg.Kafka)
	defer producer.Close()
	log.Println("✓ Kafka producer ready")

	// --- Transcoder worker pool ---
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	transcoder := media.NewTranscoder(2)
	transcoder.Start(ctx)

	// Process transcode results in background
	go processTranscodeResults(ctx, transcoder, store, repo, producer)

	// --- Kafka consumer (transcode trigger) ---
	consumer := messaging.NewConsumer(cfg.Kafka, "transcode-workers")
	go consumer.Consume(ctx, func(evt messaging.Event) error {
		if evt.Type != messaging.EventMediaUploaded {
			return nil
		}
		return handleTranscodeEvent(ctx, evt, store, repo, transcoder)
	})
	defer consumer.Close()
	log.Println("✓ Kafka consumer started")

	// --- HTTP Server ---
	mux := http.NewServeMux()

	h := handler.New(store, repo, producer)
	h.Register(mux)
	h.RegisterWebRTC(mux)

	// --- Live RTSP ingest → WebRTC relay ---
	liveServer := live.NewServer(":8554")
	if err := liveServer.Start(); err != nil {
		log.Fatalf("rtsp: %v", err)
	}
	defer liveServer.Close()

	// --- Embedded STUN server (내부망 WebRTC ICE) ---
	stunClose, err := stun.StartSTUNServer("0.0.0.0:3478")
	if err != nil {
		log.Printf("[stun] failed to start: %v (WebRTC will use host candidates only)", err)
	} else {
		defer stunClose()
	}

	lh := handler.NewLiveHandler(liveServer)
	lh.Register(mux)

	// Serve static web player
	mux.Handle("GET /", http.FileServer(http.Dir("web")))

	srv := &http.Server{
		Addr:         ":" + cfg.AppPort,
		Handler:      otelhttp.NewHandler(mux, "media-platform"),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Printf("✓ Server listening on :%s", cfg.AppPort)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	// --- Graceful shutdown ---
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	log.Printf("Shutting down (signal: %s)...", sig)

	cancel() // stop consumer + transcoder

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("server shutdown error: %v", err)
	}

	if otelShutdown != nil {
		if err := otelShutdown(shutdownCtx); err != nil {
			log.Printf("telemetry shutdown error: %v", err)
		}
	}

	transcoder.Stop()
	log.Println("Shutdown complete")
}

func handleTranscodeEvent(ctx context.Context, evt messaging.Event, store storage.Storage, repo *repository.Repository, transcoder *media.Transcoder) error {
	log.Printf("[transcode] processing media %s", evt.MediaID)

	if err := repo.UpdateStatus(ctx, evt.MediaID, "transcoding"); err != nil {
		return fmt.Errorf("update status: %w", err)
	}

	// Download from S3 to temp file
	obj, err := store.Download(ctx, evt.Key)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer obj.Close()

	tmpDir, err := os.MkdirTemp("", "transcode-"+evt.MediaID+"-")
	if err != nil {
		return fmt.Errorf("tmpdir: %w", err)
	}

	inputPath := filepath.Join(tmpDir, "input.mp4")
	f, err := os.Create(inputPath)
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	if _, err := io.Copy(f, obj); err != nil {
		f.Close()
		return fmt.Errorf("copy to temp: %w", err)
	}
	f.Close()

	transcoder.Submit(media.Job{
		MediaID:   evt.MediaID,
		InputPath: inputPath,
		OutputDir: tmpDir,
	})

	return nil
}

func processTranscodeResults(ctx context.Context, transcoder *media.Transcoder, store storage.Storage, repo *repository.Repository, producer *messaging.Producer) {
	for res := range transcoder.Results() {
		if res.Err != nil {
			log.Printf("[transcode] error for %s: %v", res.MediaID, res.Err)
			repo.UpdateStatus(ctx, res.MediaID, "failed")
			continue
		}

		hlsPrefix := fmt.Sprintf("transcoded/%s/hls/", res.MediaID)
		dashPrefix := fmt.Sprintf("transcoded/%s/dash/", res.MediaID)

		// Upload HLS files to S3
		if err := uploadDir(ctx, store, res.HLSDir, hlsPrefix); err != nil {
			log.Printf("[transcode] upload hls error: %v", err)
			repo.UpdateStatus(ctx, res.MediaID, "failed")
			continue
		}

		// Upload DASH files to S3
		if err := uploadDir(ctx, store, res.DASHDir, dashPrefix); err != nil {
			log.Printf("[transcode] upload dash error: %v", err)
			repo.UpdateStatus(ctx, res.MediaID, "failed")
			continue
		}

		// Update DB
		repo.UpdateTranscodeResult(ctx, res.MediaID, hlsPrefix+"master.m3u8", dashPrefix+"manifest.mpd")

		// Publish completion event
		producer.Publish(ctx, messaging.Event{
			Type:    messaging.EventTranscodeCompleted,
			MediaID: res.MediaID,
		})

		// Cleanup temp dir
		os.RemoveAll(filepath.Dir(res.HLSDir))
		log.Printf("[transcode] completed %s", res.MediaID)
	}
}

func uploadDir(ctx context.Context, store storage.Storage, dir, prefix string) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		ct := "application/octet-stream"
		switch {
		case strings.HasSuffix(rel, ".m3u8"):
			ct = "application/vnd.apple.mpegurl"
		case strings.HasSuffix(rel, ".mpd"):
			ct = "application/dash+xml"
		case strings.HasSuffix(rel, ".m4s"), strings.HasSuffix(rel, ".mp4"):
			ct = "video/iso.segment"
		}

		return store.Upload(ctx, prefix+rel, f, info.Size(), ct)
	})
}
