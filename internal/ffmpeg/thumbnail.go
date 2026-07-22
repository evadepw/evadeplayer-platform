package ffmpeg

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
)

type ThumbnailConfig struct {
	SpriteColumns         int
	SpriteIntervalSeconds int
	SpriteWidth           int
	SpriteHeight          int
	PreviewWidth          int
	PreviewHeight         int
	ImageStreamBandwidth  int
}

func DefaultThumbnailConfig() ThumbnailConfig {
	return ThumbnailConfig{
		SpriteColumns:         10,
		SpriteIntervalSeconds: 10,
		SpriteWidth:           320,
		SpriteHeight:          180,
		PreviewWidth:          640,
		PreviewHeight:         360,
		ImageStreamBandwidth:  30000,
	}
}

func (c ThumbnailConfig) withDefaults() ThumbnailConfig {
	def := DefaultThumbnailConfig()
	if c.SpriteColumns < 1 {
		c.SpriteColumns = def.SpriteColumns
	}
	if c.SpriteIntervalSeconds < 1 {
		c.SpriteIntervalSeconds = def.SpriteIntervalSeconds
	}
	if c.SpriteWidth < 1 {
		c.SpriteWidth = def.SpriteWidth
	}
	if c.SpriteHeight < 1 {
		c.SpriteHeight = def.SpriteHeight
	}
	if c.PreviewWidth < 1 {
		c.PreviewWidth = def.PreviewWidth
	}
	if c.PreviewHeight < 1 {
		c.PreviewHeight = def.PreviewHeight
	}
	if c.ImageStreamBandwidth < 1 {
		c.ImageStreamBandwidth = def.ImageStreamBandwidth
	}
	return c
}

func GeneratePreview(ctx context.Context, inputPath, outputDir string, duration float64) (string, error) {
	return GeneratePreviewWithConfig(ctx, inputPath, outputDir, duration, DefaultThumbnailConfig())
}

func GeneratePreviewWithConfig(ctx context.Context, inputPath, outputDir string, duration float64, cfg ThumbnailConfig) (string, error) {
	cfg = cfg.withDefaults()
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", fmt.Errorf("create thumbnail dir: %w", err)
	}

	seek := 1.0
	if duration > 30 {
		seek = duration * 0.1
	}
	if duration > 2 && seek > duration-1 {
		seek = duration - 1
	}

	previewPath := filepath.Join(outputDir, "preview.jpg")
	args := []string{
		"-ss", fmt.Sprintf("%.3f", seek),
		"-i", inputPath,
		"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2",
			cfg.PreviewWidth, cfg.PreviewHeight, cfg.PreviewWidth, cfg.PreviewHeight),
		"-frames:v", "1",
		"-qscale:v", "3",
		"-y",
		previewPath,
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("generate preview: %w", err)
	}
	return previewPath, nil
}

func GenerateSprite(ctx context.Context, inputPath, outputDir string, duration float64) (string, error) {
	return GenerateSpriteWithConfig(ctx, inputPath, outputDir, duration, DefaultThumbnailConfig())
}

func GenerateSpriteWithConfig(ctx context.Context, inputPath, outputDir string, duration float64, cfg ThumbnailConfig) (string, error) {
	cfg = cfg.withDefaults()
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", fmt.Errorf("create thumbnail dir: %w", err)
	}

	count := int(math.Ceil(duration / float64(cfg.SpriteIntervalSeconds)))
	if count < 1 {
		count = 1
	}
	rows := int(math.Ceil(float64(count) / float64(cfg.SpriteColumns)))

	// fps=1/interval → scale → tile mosaic
	filter := fmt.Sprintf(
		"fps=1/%d,scale=%d:%d,tile=%dx%d",
		cfg.SpriteIntervalSeconds, cfg.SpriteWidth, cfg.SpriteHeight, cfg.SpriteColumns, rows,
	)

	spritePath := filepath.Join(outputDir, "sprite.jpg")
	args := []string{
		"-i", inputPath,
		"-vf", filter,
		"-qscale:v", "3",
		"-frames:v", "1",
		"-y",
		spritePath,
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("generate sprite: %w", err)
	}
	return spritePath, nil
}

func WriteImageStreamManifest(hlsDir, spritePath string, duration float64) error {
	return WriteImageStreamManifestWithConfig(hlsDir, spritePath, duration, DefaultThumbnailConfig())
}

func WriteImageStreamManifestWithConfig(hlsDir, spritePath string, duration float64, cfg ThumbnailConfig) error {
	cfg = cfg.withDefaults()
	imagesDir := filepath.Join(hlsDir, "images")
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		return fmt.Errorf("create image stream dir: %w", err)
	}

	data, err := os.ReadFile(spritePath)
	if err != nil {
		return fmt.Errorf("read sprite: %w", err)
	}
	if err := os.WriteFile(filepath.Join(imagesDir, "sprite.jpg"), data, 0o644); err != nil {
		return fmt.Errorf("write image stream sprite: %w", err)
	}

	count := int(math.Ceil(duration / float64(cfg.SpriteIntervalSeconds)))
	if count < 1 {
		count = 1
	}
	rows := int(math.Ceil(float64(count) / float64(cfg.SpriteColumns)))
	targetDuration := int(math.Ceil(duration))
	if targetDuration < 1 {
		targetDuration = 1
	}

	playlist := fmt.Sprintf(`#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:%d
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-PLAYLIST-TYPE:VOD
#EXT-X-IMAGES-ONLY
#EXTINF:%.3f,
#EXT-X-TILES:RESOLUTION=%dx%d,LAYOUT=%dx%d,DURATION=%d
sprite.jpg
#EXT-X-ENDLIST
`, targetDuration, duration, cfg.SpriteWidth, cfg.SpriteHeight, cfg.SpriteColumns, rows, cfg.SpriteIntervalSeconds)

	if err := os.WriteFile(filepath.Join(imagesDir, "index.m3u8"), []byte(playlist), 0o644); err != nil {
		return fmt.Errorf("write image stream manifest: %w", err)
	}
	return nil
}
