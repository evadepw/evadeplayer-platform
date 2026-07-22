package ffmpeg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func qualityByName(name string) *Quality {
	for i := range Qualities {
		if Qualities[i].Name == name {
			return &Qualities[i]
		}
	}
	return nil
}

func makeVariants(codecIdx int, qualityNames []string) []Variant {
	c := &Codecs[codecIdx]
	var v []Variant
	for _, name := range qualityNames {
		q := qualityByName(name)
		if q != nil {
			v = append(v, Variant{Codec: c, Quality: q})
		}
	}
	return v
}

func TestWriteMasterManifest_ContainsQualities(t *testing.T) {
	dir := t.TempDir()
	variants := makeVariants(0, []string{"360p", "720p", "1080p"}) // h264

	if err := WriteMasterManifest(dir, variants, nil, nil); err != nil {
		t.Fatalf("WriteMasterManifest: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "master.m3u8"))
	if err != nil {
		t.Fatalf("read master.m3u8: %v", err)
	}
	content := string(data)

	if !strings.HasPrefix(content, "#EXTM3U") {
		t.Error("manifest must start with #EXTM3U")
	}
	for _, q := range []string{"360p", "720p", "1080p"} {
		want := "h264/" + q + "/index.m3u8"
		if !strings.Contains(content, want) {
			t.Errorf("manifest must contain %q", want)
		}
	}
}

func TestWriteMasterManifest_MultiCodec(t *testing.T) {
	dir := t.TempDir()
	var variants []Variant
	for ci := range Codecs {
		variants = append(variants, makeVariants(ci, []string{"720p"})...)
	}

	if err := WriteMasterManifest(dir, variants, nil, nil); err != nil {
		t.Fatalf("WriteMasterManifest: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "master.m3u8"))
	content := string(data)

	for _, codec := range []string{"h264", "h265", "av1"} {
		want := codec + "/720p/index.m3u8"
		if !strings.Contains(content, want) {
			t.Errorf("expected %q in manifest:\n%s", want, content)
		}
	}
	for _, codecs := range []string{"avc1.", "hvc1.", "av01."} {
		if !strings.Contains(content, codecs) {
			t.Errorf("expected CODECS containing %q in manifest:\n%s", codecs, content)
		}
	}
}

func TestWriteMasterManifest_ContainsBandwidth(t *testing.T) {
	dir := t.TempDir()
	variants := makeVariants(0, []string{"720p"})
	if err := WriteMasterManifest(dir, variants, nil, nil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "master.m3u8"))
	if !strings.Contains(string(data), "BANDWIDTH=") {
		t.Error("manifest must contain BANDWIDTH attribute")
	}
}

func TestWriteMasterManifest_ContainsImageStreamWhenPresent(t *testing.T) {
	dir := t.TempDir()
	imagesDir := filepath.Join(dir, "images")
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imagesDir, "index.m3u8"), []byte("#EXTM3U\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteMasterManifest(dir, makeVariants(0, []string{"720p"}), nil, nil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "master.m3u8"))
	content := string(data)
	if !strings.Contains(content, "#EXT-X-IMAGE-STREAM-INF:") {
		t.Errorf("expected image stream in manifest:\n%s", content)
	}
	if !strings.Contains(content, `URI="images/index.m3u8"`) {
		t.Errorf("expected image stream URI in manifest:\n%s", content)
	}
}

func TestWriteMasterManifest_UsesImageStreamConfig(t *testing.T) {
	dir := t.TempDir()
	imagesDir := filepath.Join(dir, "images")
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imagesDir, "index.m3u8"), []byte("#EXTM3U\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultThumbnailConfig()
	cfg.SpriteWidth = 480
	cfg.SpriteHeight = 270
	cfg.ImageStreamBandwidth = 70000

	if err := WriteMasterManifestWithConfig(dir, makeVariants(0, []string{"720p"}), nil, nil, cfg); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "master.m3u8"))
	content := string(data)
	for _, want := range []string{"BANDWIDTH=70000", "RESOLUTION=480x270"} {
		if !strings.Contains(content, want) {
			t.Errorf("expected %q in manifest:\n%s", want, content)
		}
	}
}

func TestWriteImageStreamManifest(t *testing.T) {
	dir := t.TempDir()
	spritePath := filepath.Join(dir, "sprite.jpg")
	if err := os.WriteFile(spritePath, []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteImageStreamManifest(filepath.Join(dir, "hls"), spritePath, 61); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "hls", "images", "index.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{"#EXT-X-IMAGES-ONLY", "#EXT-X-TILES:", "sprite.jpg"} {
		if !strings.Contains(content, want) {
			t.Errorf("expected %q in image playlist:\n%s", want, content)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "hls", "images", "sprite.jpg")); err != nil {
		t.Fatalf("expected copied sprite: %v", err)
	}
}

func TestWriteImageStreamManifest_UsesConfig(t *testing.T) {
	dir := t.TempDir()
	spritePath := filepath.Join(dir, "sprite.jpg")
	if err := os.WriteFile(spritePath, []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultThumbnailConfig()
	cfg.SpriteWidth = 480
	cfg.SpriteHeight = 270
	cfg.SpriteColumns = 5
	cfg.SpriteIntervalSeconds = 15

	if err := WriteImageStreamManifestWithConfig(filepath.Join(dir, "hls"), spritePath, 61, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "hls", "images", "index.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{"RESOLUTION=480x270", "LAYOUT=5x1", "DURATION=15"} {
		if !strings.Contains(content, want) {
			t.Errorf("expected %q in image playlist:\n%s", want, content)
		}
	}
}

func TestWriteMasterManifest_EmptyVariants(t *testing.T) {
	dir := t.TempDir()
	if err := WriteMasterManifest(dir, nil, nil, nil); err != nil {
		t.Fatalf("unexpected error for empty variants: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "master.m3u8"))
	if !strings.HasPrefix(string(data), "#EXTM3U") {
		t.Error("empty manifest must still have #EXTM3U header")
	}
}

func TestBitrateKbpsToInt(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"800k", 800},
		{"2500k", 2500},
		{"5000k", 5000},
	}
	for _, c := range cases {
		got := bitrateKbpsToInt(c.in)
		if got != c.want {
			t.Errorf("bitrateKbpsToInt(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestDoubleRate(t *testing.T) {
	if got := doubleRate("1000k"); got != "2000k" {
		t.Errorf("doubleRate(1000k) = %q, want 2000k", got)
	}
}

func TestResolutionStr(t *testing.T) {
	q := &Quality{Height: 720}
	s := resolutionStr(q)
	if !strings.Contains(s, "720") {
		t.Errorf("resolutionStr must contain height, got %q", s)
	}
	if !strings.Contains(s, "x") {
		t.Errorf("resolutionStr must be WxH format, got %q", s)
	}
}

func TestQualityByName(t *testing.T) {
	q := qualityByName("720p")
	if q == nil {
		t.Fatal("qualityByName(720p) returned nil")
	}
	if q.Height != 720 {
		t.Errorf("expected height 720, got %d", q.Height)
	}
	if qualityByName("nonexistent") != nil {
		t.Error("unknown quality must return nil")
	}
}

func TestResolutionStr_WithExplicitWidth(t *testing.T) {
	q := &Quality{Height: 800, Width: 1920}
	s := resolutionStr(q)
	if s != "1920x800" {
		t.Errorf("expected 1920x800 when Width is set, got %q", s)
	}
}

// --- BuildQualities ---

func TestBuildQualities_KnownNames(t *testing.T) {
	qs, err := BuildQualities([]string{"360p", "1080p"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(qs) != 2 {
		t.Fatalf("expected 2 qualities, got %d", len(qs))
	}
	if qs[0].Name != "360p" || qs[1].Name != "1080p" {
		t.Errorf("unexpected order: %v %v", qs[0].Name, qs[1].Name)
	}
}

func TestBuildQualities_UnknownName(t *testing.T) {
	_, err := BuildQualities([]string{"360p", "9999p"}, nil)
	if err == nil {
		t.Error("expected error for unknown quality name")
	}
}

func TestBuildQualities_EmptyNames_ReturnsDefaults(t *testing.T) {
	qs, err := BuildQualities(nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(qs) != len(Qualities) {
		t.Errorf("expected %d default qualities, got %d", len(Qualities), len(qs))
	}
}

func TestBuildQualities_BitrateOverride(t *testing.T) {
	qs, err := BuildQualities([]string{"720p", "1080p"}, map[string]string{
		"720p":  "9999k",
		"1080p": "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if qs[0].Bitrate != "9999k" {
		t.Errorf("720p bitrate: got %q, want 9999k", qs[0].Bitrate)
	}
	if qs[1].Bitrate == "9999k" {
		t.Error("empty override must not change 1080p bitrate")
	}
	if qs[1].Bitrate == "" {
		t.Error("1080p must keep its default bitrate when override is empty")
	}
}

func TestBuildQualities_OriginalQuality(t *testing.T) {
	qs, err := BuildQualities([]string{"720p", "original"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var orig *Quality
	for i := range qs {
		if qs[i].Name == "original" {
			orig = &qs[i]
		}
	}
	if orig == nil {
		t.Fatal("original quality not found in result")
	}
	if !orig.Original {
		t.Error("original quality must have Original=true")
	}
	if orig.Bitrate != "" {
		t.Errorf("original quality must have empty Bitrate, got %q", orig.Bitrate)
	}
}

func TestBuildQualities_OriginalResolvesInManifest(t *testing.T) {
	// After TranscodeHLS resolves "original" to actual dimensions, the manifest
	// must include the correct resolution string.
	dir := t.TempDir()
	qs, _ := BuildQualities([]string{"original"}, nil)
	// Simulate what TranscodeHLS does before passing to WriteMasterManifest.
	for i := range qs {
		if qs[i].Original {
			qs[i].Height = 1080
			qs[i].Width = 1920
		}
	}
	variants := []Variant{{Codec: &Codecs[0], Quality: &qs[0]}}
	if err := WriteMasterManifest(dir, variants, nil, nil); err != nil {
		t.Fatalf("WriteMasterManifest: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "master.m3u8"))
	content := string(data)
	if !strings.Contains(content, "1920x1080") {
		t.Errorf("manifest must contain actual resolution 1920x1080, got:\n%s", content)
	}
	if !strings.Contains(content, "h264/original/index.m3u8") {
		t.Errorf("manifest must reference original quality path:\n%s", content)
	}
}
