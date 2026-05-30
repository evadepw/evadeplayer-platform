package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/evadeplayer/api/internal/service"
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

	if err := r.ParseMultipartForm(32 << 20); err != nil {
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
