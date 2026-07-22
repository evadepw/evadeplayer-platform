package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/evadepw/evadeplayer-platform/internal/service"
)

const authTestSecret = "hls-test-secret-32-chars-minimum"

func validToken(videoID string) (token, expires string) {
	exp := time.Now().Add(time.Hour).Unix()
	expires = strconv.FormatInt(exp, 10)
	token = service.ComputeHLSToken([]byte(authTestSecret), videoID, expires)
	return
}

func doValidateRequest(h *HLSAuthHandler, videoID, token, expires string) *httptest.ResponseRecorder {
	url := fmt.Sprintf("/internal/validate-hls?video_id=%s&token=%s&expires=%s", videoID, token, expires)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rr := httptest.NewRecorder()
	h.ValidateToken(rr, req)
	return rr
}

func TestHLSAuthHandler_ValidToken(t *testing.T) {
	h := NewHLSAuthHandler(authTestSecret, true)
	tok, exp := validToken("vid-abc")
	rr := doValidateRequest(h, "vid-abc", tok, exp)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestHLSAuthHandler_MissingVideoID(t *testing.T) {
	h := NewHLSAuthHandler(authTestSecret, true)
	tok, exp := validToken("vid-abc")
	rr := doValidateRequest(h, "", tok, exp)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestHLSAuthHandler_MissingToken(t *testing.T) {
	h := NewHLSAuthHandler(authTestSecret, true)
	_, exp := validToken("vid-abc")
	rr := doValidateRequest(h, "vid-abc", "", exp)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestHLSAuthHandler_MissingExpires(t *testing.T) {
	h := NewHLSAuthHandler(authTestSecret, true)
	tok, _ := validToken("vid-abc")
	rr := doValidateRequest(h, "vid-abc", tok, "")
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestHLSAuthHandler_ExpiredToken(t *testing.T) {
	h := NewHLSAuthHandler(authTestSecret, true)
	exp := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
	tok := service.ComputeHLSToken([]byte(authTestSecret), "vid-abc", exp)
	rr := doValidateRequest(h, "vid-abc", tok, exp)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for expired token, got %d", rr.Code)
	}
}

func TestHLSAuthHandler_WrongToken(t *testing.T) {
	h := NewHLSAuthHandler(authTestSecret, true)
	_, exp := validToken("vid-abc")
	rr := doValidateRequest(h, "vid-abc", "deadbeefdeadbeef", exp)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong token, got %d", rr.Code)
	}
}

func TestHLSAuthHandler_WrongVideoID(t *testing.T) {
	h := NewHLSAuthHandler(authTestSecret, true)
	tok, exp := validToken("vid-abc")
	// token for vid-abc but request claims vid-xyz
	rr := doValidateRequest(h, "vid-xyz", tok, exp)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for mismatched video ID, got %d", rr.Code)
	}
}
