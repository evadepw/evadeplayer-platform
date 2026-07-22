package handler

import (
	"strings"
	"testing"
)

func TestRewriteManifest_MasterManifest(t *testing.T) {
	content := "#EXTM3U\n#EXT-X-VERSION:3\n\n#EXT-X-STREAM-INF:BANDWIDTH=896000\n360p/index.m3u8\n\n#EXT-X-STREAM-INF:BANDWIDTH=2628000\n720p/index.m3u8\n"

	got := rewriteManifest(content, "vid-123", "master.m3u8", "?token=tok&expires=9999")

	for _, q := range []string{"360p", "720p"} {
		want := "/hls-proxy/vid-123/" + q + "/index.m3u8?token=tok&expires=9999"
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in rewritten manifest, got:\n%s", want, got)
		}
	}
}

func TestRewriteManifest_QualityManifest(t *testing.T) {
	content := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.000000,\n00000.ts\n#EXTINF:4.000000,\n00001.ts\n#EXT-X-ENDLIST\n"

	got := rewriteManifest(content, "vid-123", "360p/index.m3u8", "?token=tok&expires=9999")

	for _, chunk := range []string{"00000.ts", "00001.ts"} {
		want := "/hls/vid-123/360p/" + chunk + "?token=tok&expires=9999"
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in rewritten manifest, got:\n%s", want, got)
		}
	}
}

func TestRewriteManifest_PreservesComments(t *testing.T) {
	content := "#EXTM3U\n#EXT-X-VERSION:3\n#EXTINF:4.0,\n00000.ts\n"
	got := rewriteManifest(content, "v", "360p/index.m3u8", "?token=t&expires=1")
	for _, comment := range []string{"#EXTM3U", "#EXT-X-VERSION:3", "#EXTINF:4.0,"} {
		if !strings.Contains(got, comment) {
			t.Errorf("comment %q must be preserved, got:\n%s", comment, got)
		}
	}
}

func TestRewriteManifest_EmptyLinesPreserved(t *testing.T) {
	content := "#EXTM3U\n\n360p/index.m3u8\n"
	got := rewriteManifest(content, "v", "master.m3u8", "?token=t&expires=1")
	lines := strings.Split(got, "\n")
	hasEmpty := false
	for _, l := range lines {
		if l == "" {
			hasEmpty = true
			break
		}
	}
	if !hasEmpty {
		t.Error("empty lines must be preserved")
	}
}

func TestRewriteManifest_BasePathResolution(t *testing.T) {
	// 360p/index.m3u8 → basePath = "360p/"
	// chunk "00000.ts" should resolve to "360p/00000.ts"
	content := "#EXTINF:4.0,\n00000.ts\n"
	got := rewriteManifest(content, "vid", "360p/index.m3u8", "?token=tok&expires=99")
	want := "/hls/vid/360p/00000.ts?token=tok&expires=99"
	if !strings.Contains(got, want) {
		t.Errorf("expected %q, got:\n%s", want, got)
	}
}

func TestRewriteManifest_NoDoubleSlash(t *testing.T) {
	content := "360p/index.m3u8\n"
	got := rewriteManifest(content, "vid", "master.m3u8", "?token=tok&expires=99")
	if strings.Contains(got, "//") {
		t.Errorf("rewritten URL must not contain double slash:\n%s", got)
	}
}

func TestRewriteManifest_FMP4Segments(t *testing.T) {
	content := "#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:4.0,\n00000.m4s\n#EXTINF:4.0,\n00001.m4s\n#EXT-X-ENDLIST\n"

	got := rewriteManifest(content, "vid-123", "h265/720p/index.m3u8", "?token=tok&expires=9999")

	for _, chunk := range []string{"00000.m4s", "00001.m4s"} {
		want := "/hls/vid-123/h265/720p/" + chunk + "?token=tok&expires=9999"
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in rewritten manifest, got:\n%s", want, got)
		}
	}
}

func TestRewriteManifest_ExtXMap(t *testing.T) {
	content := "#EXT-X-MAP:URI=\"init.mp4\"\n"
	got := rewriteManifest(content, "vid-123", "h265/720p/index.m3u8", "?token=tok&expires=9999")

	want := `URI="/hls/vid-123/h265/720p/init.mp4?token=tok&expires=9999"`
	if !strings.Contains(got, want) {
		t.Errorf("expected %q in rewritten manifest, got:\n%s", want, got)
	}
}

func TestRewriteManifest_ExtXImageStreamInf(t *testing.T) {
	content := "#EXT-X-IMAGE-STREAM-INF:BANDWIDTH=30000,RESOLUTION=320x180,CODECS=\"jpeg\",URI=\"images/index.m3u8\"\n"
	got := rewriteManifest(content, "vid-123", "master.m3u8", "?token=tok&expires=9999")

	want := `URI="/hls-proxy/vid-123/images/index.m3u8?token=tok&expires=9999"`
	if !strings.Contains(got, want) {
		t.Errorf("expected %q in rewritten manifest, got:\n%s", want, got)
	}
}

func TestRewriteManifest_ImagePlaylist(t *testing.T) {
	content := "#EXTM3U\n#EXT-X-IMAGES-ONLY\n#EXTINF:60.0,\nsprite.jpg\n#EXT-X-ENDLIST\n"
	got := rewriteManifest(content, "vid-123", "images/index.m3u8", "?token=tok&expires=9999")

	want := "/hls/vid-123/images/sprite.jpg?token=tok&expires=9999"
	if !strings.Contains(got, want) {
		t.Errorf("expected %q in rewritten manifest, got:\n%s", want, got)
	}
}

func TestFilterMasterByCodec(t *testing.T) {
	content := "#EXTM3U\n#EXT-X-VERSION:6\n\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=2628000,CODECS=\"avc1.640028,mp4a.40.2\"\nh264/720p/index.m3u8\n\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=1628000,CODECS=\"hvc1.1.6.L120.90,mp4a.40.2\"\nh265/720p/index.m3u8\n\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=500000,CODECS=\"av01.0.04M.08,mp4a.40.2\"\nav1/720p/index.m3u8\n\n"

	t.Run("filter h264", func(t *testing.T) {
		got := filterMasterByCodec(content, "h264")
		if !strings.Contains(got, "h264/720p/index.m3u8") {
			t.Error("expected h264 variant to be present")
		}
		if strings.Contains(got, "h265/") || strings.Contains(got, "av1/") {
			t.Error("expected h265 and av1 variants to be removed")
		}
	})

	t.Run("filter h265", func(t *testing.T) {
		got := filterMasterByCodec(content, "h265")
		if !strings.Contains(got, "h265/720p/index.m3u8") {
			t.Error("expected h265 variant to be present")
		}
		if strings.Contains(got, "h264/") || strings.Contains(got, "av1/") {
			t.Error("expected h264 and av1 variants to be removed")
		}
	})

	t.Run("unknown codec falls back to unfiltered", func(t *testing.T) {
		got := filterMasterByCodec(content, "vp9")
		if got != content {
			t.Error("expected unfiltered fallback for unknown codec")
		}
	})

	t.Run("old-style manifest (no codec prefix) falls back to unfiltered", func(t *testing.T) {
		old := "#EXTM3U\n#EXT-X-VERSION:3\n\n#EXT-X-STREAM-INF:BANDWIDTH=896000\n360p/index.m3u8\n"
		got := filterMasterByCodec(old, "h264")
		if got != old {
			t.Errorf("expected unfiltered fallback for old-style manifest, got:\n%s", got)
		}
	})
}

func TestRewriteManifest_ExtXMedia(t *testing.T) {
	content := "#EXTM3U\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",LANGUAGE=\"eng\",NAME=\"English\",DEFAULT=YES,AUTOSELECT=YES,URI=\"audio/0/index.m3u8\"\n" +
		"#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"subs\",LANGUAGE=\"rus\",NAME=\"Russian\",DEFAULT=NO,FORCED=NO,URI=\"subs/0/index.m3u8\"\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=2628000,AUDIO=\"audio\",SUBTITLES=\"subs\"\n" +
		"h264/720p/index.m3u8\n"

	got := rewriteManifest(content, "vid-123", "master.m3u8", "?token=tok&expires=9999")

	wantAudio := `/hls-proxy/vid-123/audio/0/index.m3u8?token=tok&expires=9999`
	if !strings.Contains(got, wantAudio) {
		t.Errorf("expected audio URI %q in rewritten manifest, got:\n%s", wantAudio, got)
	}
	wantSubs := `/hls-proxy/vid-123/subs/0/index.m3u8?token=tok&expires=9999`
	if !strings.Contains(got, wantSubs) {
		t.Errorf("expected subtitle URI %q in rewritten manifest, got:\n%s", wantSubs, got)
	}
	// the rest of the tag line must be preserved
	if !strings.Contains(got, `TYPE=AUDIO`) || !strings.Contains(got, `TYPE=SUBTITLES`) {
		t.Errorf("EXT-X-MEDIA tag attributes must be preserved:\n%s", got)
	}
}

func TestRewriteManifest_MultiCodecMaster(t *testing.T) {
	content := "#EXTM3U\n#EXT-X-VERSION:6\n\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=2628000,CODECS=\"avc1.640028,mp4a.40.2\"\nh264/720p/index.m3u8\n\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=1628000,CODECS=\"hvc1.1.6.L120.90,mp4a.40.2\"\nh265/720p/index.m3u8\n\n"

	got := rewriteManifest(content, "vid-123", "master.m3u8", "?token=tok&expires=9999")

	for _, codec := range []string{"h264", "h265"} {
		want := "/hls-proxy/vid-123/" + codec + "/720p/index.m3u8?token=tok&expires=9999"
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in rewritten manifest, got:\n%s", want, got)
		}
	}
}

func TestValidManifestPath(t *testing.T) {
	valid := []string{
		"master.m3u8",
		"h264/720p/index.m3u8",
		"audio/0/index.m3u8",
	}
	for _, p := range valid {
		if !validManifestPath(p) {
			t.Errorf("validManifestPath(%q) = false, want true", p)
		}
	}
	invalid := []string{
		"",
		"master.m3u8/",
		"../other-video/master.m3u8",
		"h264/../../secrets.m3u8",
		"/etc/passwd",
		"h264//index.m3u8",
		"h264/720p/00000.m4s",
		"a%2e%2e/index.m3u8",
		"dir\\index.m3u8",
		"index.m3u8?x=1",
	}
	for _, p := range invalid {
		if validManifestPath(p) {
			t.Errorf("validManifestPath(%q) = true, want false", p)
		}
	}
}
