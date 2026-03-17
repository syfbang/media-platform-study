package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/media-service/media-platform/internal/messaging"
	"github.com/media-service/media-platform/internal/repository"
	"github.com/media-service/media-platform/internal/storage"
	"github.com/media-service/media-platform/internal/telemetry"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

type Handler struct {
	store    storage.Storage
	repo     *repository.Repository
	producer *messaging.Producer
}

func New(store storage.Storage, repo *repository.Repository, producer *messaging.Producer) *Handler {
	return &Handler{store: store, repo: repo, producer: producer}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", h.health)
	mux.HandleFunc("POST /api/upload", h.upload)
	mux.HandleFunc("GET /api/media", h.listMedia)
	mux.HandleFunc("GET /api/media/{id}", h.getMedia)
	mux.HandleFunc("GET /api/media/{id}/hls/{file...}", h.serveHLS)
	mux.HandleFunc("GET /api/media/{id}/dash/{file...}", h.serveDASH)
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	ctx, span := telemetry.Tracer.Start(r.Context(), "handler.upload")
	defer span.End()

	// Max 500MB
	r.Body = http.MaxBytesReader(w, r.Body, 500<<20)
	file, header, err := r.FormFile("file")
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file required: " + err.Error()})
		return
	}
	defer file.Close()

	span.SetAttributes(
		attribute.String("media.filename", header.Filename),
		attribute.Int64("media.size_bytes", header.Size),
	)

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext != ".mp4" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "only .mp4 files accepted"})
		return
	}

	id := uuid.New().String()
	s3Key := fmt.Sprintf("originals/%s%s", id, ext)

	// Upload to S3
	if err := h.store.Upload(ctx, s3Key, file, header.Size, "video/mp4"); err != nil {
		span.SetStatus(codes.Error, err.Error())
		log.Printf("[upload] s3 error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "upload failed"})
		return
	}

	// Save metadata
	media := &repository.Media{
		ID:          id,
		Filename:    header.Filename,
		S3Key:       s3Key,
		ContentType: "video/mp4",
		Size:        header.Size,
		Status:      "uploaded",
	}
	if err := h.repo.Create(ctx, media); err != nil {
		log.Printf("[upload] db error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save metadata failed"})
		return
	}

	// Publish event
	if err := h.producer.Publish(ctx, messaging.Event{
		Type:    messaging.EventMediaUploaded,
		MediaID: id,
		Key:     s3Key,
	}); err != nil {
		log.Printf("[upload] kafka error: %v", err)
		// non-fatal: file is uploaded, can retry later
	}

	writeJSON(w, http.StatusCreated, media)
}

func (h *Handler) listMedia(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	list, err := h.repo.List(r.Context(), limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) getMedia(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	media, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, media)
}

func (h *Handler) serveHLS(w http.ResponseWriter, r *http.Request) {
	h.serveMediaFile(w, r, "hls")
}

func (h *Handler) serveDASH(w http.ResponseWriter, r *http.Request) {
	h.serveMediaFile(w, r, "dash")
}

func (h *Handler) serveMediaFile(w http.ResponseWriter, r *http.Request, format string) {
	ctx, span := telemetry.Tracer.Start(r.Context(), "handler.serve_"+format)
	defer span.End()

	id := r.PathValue("id")
	file := r.PathValue("file")
	span.SetAttributes(attribute.String("media.id", id), attribute.String("media.format", format))

	s3Key := fmt.Sprintf("transcoded/%s/%s/%s", id, format, file)

	obj, err := h.store.Download(ctx, s3Key)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer obj.Close()

	// Set content type based on extension
	switch filepath.Ext(file) {
	case ".m3u8":
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	case ".mpd":
		w.Header().Set("Content-Type", "application/dash+xml")
	case ".m4s":
		w.Header().Set("Content-Type", "video/iso.segment")
	case ".mp4":
		w.Header().Set("Content-Type", "video/mp4")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")
	io.Copy(w, obj)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
