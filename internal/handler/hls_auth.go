package handler

import (
	"crypto/hmac"
	"net/http"
	"strconv"
	"time"

	"github.com/evadepw/evadeplayer-platform/internal/service"
)

type HLSAuthHandler struct {
	secret       []byte
	requireToken bool
}

func NewHLSAuthHandler(secret string, requireToken bool) *HLSAuthHandler {
	return &HLSAuthHandler{secret: []byte(secret), requireToken: requireToken}
}

func (h *HLSAuthHandler) ValidateToken(w http.ResponseWriter, r *http.Request) {
	if !h.requireToken {
		w.WriteHeader(http.StatusOK)
		return
	}

	q := r.URL.Query()
	videoID := q.Get("video_id")
	token := q.Get("token")
	expiresStr := q.Get("expires")

	if videoID == "" || token == "" || expiresStr == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	expires, err := strconv.ParseInt(expiresStr, 10, 64)
	if err != nil || time.Now().Unix() > expires {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	expected := service.ComputeHLSToken(h.secret, videoID, expiresStr)
	if !hmac.Equal([]byte(token), []byte(expected)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusOK)
}
