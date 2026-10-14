package evidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

func TestHermeticClipperRequiresVerifiedPackFFmpeg(t *testing.T) {
	_, err := NewClipper(ClipConfig{
		DataDir:        t.TempDir(),
		MaxFrames:      1,
		FFmpegPath:     "ffmpeg",
		FFmpegMode:     processing.FFmpegExecutionHermetic,
		VerifiedFFmpeg: nil,
	})
	if err == nil {
		t.Fatal("expected hermetic clipper to reject missing verified executable")
	}
}

func TestHermeticClipperUsesVerifiedPackExecutable(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "clipper-ran")
	path := filepath.Join(root, "bin", "ffmpeg")
	contents := []byte("#!/bin/sh\nprintf ran > '" + marker + "'\nwhile IFS= read -r line; do :; done\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	verified, err := processing.VerifyPackFFmpeg(root, path, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatalf("VerifyPackFFmpeg: %v", err)
	}
	pathDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pathDir, "ffmpeg"), []byte("not executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)

	clipper, err := NewClipper(ClipConfig{
		DataDir:        t.TempDir(),
		MaxFrames:      1,
		FFmpegPath:     "ffmpeg",
		FFmpegMode:     processing.FFmpegExecutionHermetic,
		VerifiedFFmpeg: &verified,
	})
	if err != nil {
		t.Fatalf("NewClipper: %v", err)
	}
	frame := processing.Frame{OutputWidth: 32, OutputHeight: 32, Data: make([]byte, 32*32*3/2)}
	if err := clipper.encode(context.Background(), filepath.Join(t.TempDir(), "clip.mp4"), []processing.Frame{frame}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("bundled FFmpeg fixture did not run: %v", err)
	}
}
