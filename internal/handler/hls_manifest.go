package handler

import (
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/evadepw/evadeplayer-platform/internal/service"
)

var errUpstreamNotFound = errors.New("manifest not found")

type HLSManifestHandler struct {
	secret       []byte
	filerURL     string
	client       *http.Client
	requireToken bool
}

func NewHLSManifestHandler(secret, filerURL string, requireToken bool) *HLSManifestHandler {
	return &HLSManifestHandler{
		secret:       []byte(secret),
		filerURL:     strings.TrimRight(filerURL, "/"),
		client:       &http.Client{Timeout: 10 * time.Second},
		requireToken: requireToken,
	}
}

func (h *HLSManifestHandler) ServeManifest(w http.ResponseWriter, r *http.Request) {
	// path: /hls-proxy/<videoID>/<rest>
	trimmed := strings.TrimPrefix(r.URL.Path, "/hls-proxy/")
	idx := strings.Index(trimmed, "/")
	if idx < 0 {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}
	videoID := trimmed[:idx]
	rest := trimmed[idx+1:] // e.g. "master.m3u8" or "360p/index.m3u8"

	// The proxy serves only HLS playlists that belong to a single video.
	// Everything else (segments, images, VTT) goes through /hls/ directly,
	// so any other shape is either a mistake or an attempt to make the API
	// fetch arbitrary filer paths.
	if _, err := uuid.Parse(videoID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid video id")
		return
	}
	if !validManifestPath(rest) {
		writeError(w, http.StatusBadRequest, "invalid manifest path")
		return
	}

	token := r.URL.Query().Get("token")
	expiresStr := r.URL.Query().Get("expires")

	if h.requireToken && !h.validateToken(videoID, token, expiresStr) {
		writeError(w, http.StatusUnauthorized, "invalid or expired token")
		return
	}

	body, err := h.fetchFromSeaweedFS(r.Context(), videoID, rest)
	if err != nil {
		if errors.Is(err, errUpstreamNotFound) {
			writeError(w, http.StatusNotFound, "manifest not found")
			return
		}
		writeError(w, http.StatusBadGateway, "failed to fetch manifest")
		return
	}

	raw := body
	if strings.HasSuffix(rest, "master.m3u8") {
		q := r.URL.Query()
		codec := q.Get("codec")
		audio := q.Get("audio")
		subs := q.Get("subs")
		if codec != "" {
			raw = filterMasterByCodec(raw, codec)
		}
		if audio != "" || subs != "" {
			raw = filterMasterByTracks(raw, audio, subs)
		}
	}

	var tokenQuery string
	if token != "" {
		tokenQuery = "?token=" + url.QueryEscape(token) + "&expires=" + url.QueryEscape(expiresStr)
	}
	rewritten := rewriteManifest(raw, videoID, rest, tokenQuery)

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, rewritten)
}

// validManifestPath reports whether rest is a safe relative playlist path:
// plain path segments (no traversal, no absolute paths, no escapes) ending in
// .m3u8.
func validManifestPath(rest string) bool {
	if rest == "" || len(rest) > 512 || !strings.HasSuffix(rest, ".m3u8") {
		return false
	}
	for _, seg := range strings.Split(rest, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		if strings.ContainsAny(seg, "\\?#%") {
			return false
		}
	}
	return true
}

func (h *HLSManifestHandler) fetchFromSeaweedFS(ctx context.Context, videoID, rest string) (string, error) {
	u := fmt.Sprintf("%s/hls/%s/%s", h.filerURL, videoID, rest)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", errUpstreamNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("seaweedfs returned %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func rewriteManifest(content, videoID, manifestPath, tokenQuery string) string {
	basePath := ""
	if i := strings.LastIndex(manifestPath, "/"); i >= 0 {
		basePath = manifestPath[:i+1]
	}

	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(trimmed, "#EXT-X-MAP:") {
			out = append(out, rewriteTagURIAttr(trimmed, videoID, basePath, tokenQuery, "/hls/"))
			continue
		}
		if strings.HasPrefix(trimmed, "#EXT-X-MEDIA:") {
			out = append(out, rewriteTagURIAttr(trimmed, videoID, basePath, tokenQuery, "/hls-proxy/"))
			continue
		}
		if strings.HasPrefix(trimmed, "#EXT-X-IMAGE-STREAM-INF:") {
			out = append(out, rewriteTagURIAttr(trimmed, videoID, basePath, tokenQuery, "/hls-proxy/"))
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			out = append(out, line)
			continue
		}
		// URL line — resolve relative path
		relPath := trimmed
		if !strings.HasPrefix(relPath, "/") {
			relPath = basePath + relPath
		}
		relPath = strings.TrimPrefix(relPath, "/")

		switch {
		case strings.HasSuffix(trimmed, ".m3u8"):
			out = append(out, fmt.Sprintf("/hls-proxy/%s/%s%s", videoID, relPath, tokenQuery))
		case strings.HasSuffix(trimmed, ".ts"), strings.HasSuffix(trimmed, ".m4s"),
			strings.HasSuffix(trimmed, ".jpg"), strings.HasSuffix(trimmed, ".jpeg"),
			strings.HasSuffix(trimmed, ".vtt"):
			out = append(out, fmt.Sprintf("/hls/%s/%s%s", videoID, relPath, tokenQuery))
		default:
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// filterMasterByCodec keeps only EXT-X-STREAM-INF entries whose URL starts with "<codec>/".
// Falls back to the original content if no entries match (old videos, unavailable codec).
func filterMasterByCodec(content, codec string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	pendingInf := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#EXT-X-STREAM-INF:") {
			pendingInf = line
			continue
		}
		if pendingInf != "" {
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				out = append(out, pendingInf, line)
				pendingInf = ""
				continue
			}
			if strings.HasPrefix(trimmed, codec+"/") {
				out = append(out, pendingInf, line)
			}
			pendingInf = ""
			continue
		}
		out = append(out, line)
	}
	result := strings.Join(out, "\n")
	if !strings.Contains(result, "#EXT-X-STREAM-INF:") {
		return content
	}
	return result
}

// filterMasterByTracks keeps only the #EXT-X-MEDIA entries for the requested
// audio index and/or subtitle index. Unspecified types (empty string) are kept
// as-is. The selected audio track is forced to DEFAULT=YES.
func filterMasterByTracks(content, audioIdx, subsIdx string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#EXT-X-MEDIA:TYPE=AUDIO,") {
			if audioIdx == "" || containsURISegment(trimmed, "audio/"+audioIdx+"/") {
				if audioIdx != "" {
					line = forceDefault(trimmed)
				}
				out = append(out, line)
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#EXT-X-MEDIA:TYPE=SUBTITLES,") {
			if subsIdx == "" || containsURISegment(trimmed, "subs/"+subsIdx+"/") {
				out = append(out, line)
			}
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// containsURISegment checks whether the URI="..." attribute of an HLS tag
// contains the given path segment.
func containsURISegment(line, segment string) bool {
	const uriPrefix = `URI="`
	start := strings.Index(line, uriPrefix)
	if start < 0 {
		return false
	}
	start += len(uriPrefix)
	end := strings.Index(line[start:], `"`)
	if end < 0 {
		return false
	}
	return strings.Contains(line[start:start+end], segment)
}

// forceDefault rewrites DEFAULT=NO to DEFAULT=YES in a tag line.
func forceDefault(line string) string {
	if strings.Contains(line, "DEFAULT=NO") {
		return strings.Replace(line, "DEFAULT=NO", "DEFAULT=YES", 1)
	}
	return line
}

// rewriteTagURIAttr rewrites the URI="..." attribute inside an HLS tag line,
// routing sub-manifests through proxyPrefix (e.g. "/hls-proxy/") and segments
// through "/hls/".
func rewriteTagURIAttr(line, videoID, basePath, tokenQuery, proxyPrefix string) string {
	const uriPrefix = `URI="`
	start := strings.Index(line, uriPrefix)
	if start < 0 {
		return line
	}
	start += len(uriPrefix)
	end := strings.Index(line[start:], `"`)
	if end < 0 {
		return line
	}
	uri := line[start : start+end]
	relPath := uri
	if !strings.HasPrefix(relPath, "/") {
		relPath = basePath + relPath
	}
	relPath = strings.TrimPrefix(relPath, "/")
	newURI := fmt.Sprintf("%s%s/%s%s", proxyPrefix, videoID, relPath, tokenQuery)
	return line[:start] + newURI + line[start+end:]
}

func (h *HLSManifestHandler) validateToken(videoID, token, expiresStr string) bool {
	if videoID == "" || token == "" || expiresStr == "" {
		return false
	}
	expires, err := strconv.ParseInt(expiresStr, 10, 64)
	if err != nil || time.Now().Unix() > expires {
		return false
	}
	expected := service.ComputeHLSToken(h.secret, videoID, expiresStr)
	return hmac.Equal([]byte(token), []byte(expected))
}
