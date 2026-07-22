package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/evadepw/evadeplayer-platform/internal/repository"
	"github.com/evadepw/evadeplayer-platform/internal/service"
)

var allowedExtensions = map[string]bool{
	".mp4":  true,
	".mkv":  true,
	".mov":  true,
	".avi":  true,
	".webm": true,
	".m4v":  true,
}

type UploadHandler struct {
	svc           *service.UploadService
	maxUploadSize int64
}

func NewUploadHandler(svc *service.UploadService, maxUploadSize int64) *UploadHandler {
	return &UploadHandler{svc: svc, maxUploadSize: maxUploadSize}
}

func (h *UploadHandler) Upload(w http.ResponseWriter, r *http.Request) {
	log.Printf("[upload] request received from %s, Content-Length=%d", r.RemoteAddr, r.ContentLength)

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	// Enforce the size limit while the body streams in, not after: a
	// too-large upload is cut off at the limit instead of being written to
	// the multipart temp file in full first. The slack covers form overhead.
	r.Body = http.MaxBytesReader(w, r.Body, h.maxUploadSize+(1<<20))

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("file too large, max %d GB", h.maxUploadSize>>30))
			return
		}
		log.Printf("[upload] ParseMultipartForm error: %v", err)
		writeError(w, http.StatusBadRequest, "failed to parse multipart form")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file is required")
		return
	}
	defer file.Close()

	if header.Size > h.maxUploadSize {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("file too large, max %d GB", h.maxUploadSize>>30))
		return
	}

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if !allowedExtensions[ext] {
		writeError(w, http.StatusBadRequest, "unsupported file format")
		return
	}

	var segments []byte
	if sf, _, serr := r.FormFile("segments"); serr == nil {
		defer sf.Close()
		if data, serr := io.ReadAll(sf); serr == nil && json.Valid(data) {
			segments = data
		} else if serr == nil {
			writeError(w, http.StatusBadRequest, "segments: invalid JSON")
			return
		}
	} else if s := r.FormValue("segments"); s != "" {
		if !json.Valid([]byte(s)) {
			writeError(w, http.StatusBadRequest, "segments: invalid JSON")
			return
		}
		segments = []byte(s)
	}

	log.Printf("[upload] starting storage upload, file size=%d ext=%s", header.Size, ext)
	video, err := h.svc.Upload(r.Context(), &service.UploadInput{
		FileExt:  ext,
		Size:     header.Size,
		Reader:   file,
		Segments: segments,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "upload failed")
		return
	}

	log.Printf("[upload] done, video id=%s", video.ID)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"id":     video.ID,
		"status": video.Status,
	})
}

func (h *UploadHandler) DownloadOriginal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing video id")
		return
	}

	result, err := h.svc.DownloadOriginal(r.Context(), id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "video not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer result.Body.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, result.Filename))
	if result.Size > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", result.Size))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, result.Body)
}

func (h *UploadHandler) DeleteVideo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing video id")
		return
	}

	if err := h.svc.DeleteVideo(r.Context(), id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "video not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
