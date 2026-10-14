package processing

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
)

func TestHermeticFFmpegRejectsMissingCapabilityAndUntrustedInputs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bin", "ffmpeg")
	writeSyntheticFFmpeg(t, path, "")

	t.Run("empty configuration ignores PATH and fails closed", func(t *testing.T) {
		fakePath := t.TempDir()
		writeSyntheticFFmpeg(t, filepath.Join(fakePath, "ffmpeg"), "unexpected-host")
		t.Setenv("PATH", fakePath)
		_, err := NewFFmpegDecoder(FFmpegDecoderConfig{Mode: FFmpegExecutionHermetic, BinaryPath: "ffmpeg"}, 32, 32, nil, nil)
		if !errors.Is(err, ErrHermeticFFmpegRequired) {
			t.Fatalf("error = %v, want %v", err, ErrHermeticFFmpegRequired)
		}
	})

	t.Run("missing executable", func(t *testing.T) {
		_, err := VerifyPackFFmpeg(root, filepath.Join(root, "bin", "missing"), strings.Repeat("0", 64))
		if err == nil {
			t.Fatal("expected missing executable to fail")
		}
	})

	t.Run("external symlink", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "ffmpeg")
		writeSyntheticFFmpeg(t, outside, "outside")
		link := filepath.Join(root, "bin", "external-ffmpeg")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		sum := sha256.Sum256([]byte("#!/bin/sh\n# synthetic ffmpeg\n"))
		_, err := VerifyPackFFmpeg(root, link, hex.EncodeToString(sum[:]))
		if err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("error = %v, want symlink rejection", err)
		}
	})

	t.Run("wrong hash", func(t *testing.T) {
		_, err := VerifyPackFFmpeg(root, path, strings.Repeat("0", 64))
		if err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
			t.Fatalf("error = %v, want SHA-256 mismatch", err)
		}
	})

	t.Run("relative path", func(t *testing.T) {
		_, err := VerifyPackFFmpeg(root, "bin/ffmpeg", strings.Repeat("0", 64))
		if err == nil || !strings.Contains(err.Error(), "absolute") {
			t.Fatalf("error = %v, want absolute path rejection", err)
		}
	})

	t.Run("path traversal", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "ffmpeg")
		writeSyntheticFFmpeg(t, outside, "")
		_, err := VerifyPackFFmpeg(root, outside, strings.Repeat("0", 64))
		if err == nil || !strings.Contains(err.Error(), "escapes Runtime Pack") {
			t.Fatalf("error = %v, want Runtime Pack confinement rejection", err)
		}
	})

	t.Run("non executable file", func(t *testing.T) {
		nonExec := filepath.Join(root, "bin", "not-executable")
		if err := os.WriteFile(nonExec, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte("data"))
		_, err := VerifyPackFFmpeg(root, nonExec, hex.EncodeToString(sum[:]))
		if err == nil || !strings.Contains(err.Error(), "executable") {
			t.Fatalf("error = %v, want executable permission rejection", err)
		}
	})
}

func TestHermeticFFmpegUsesOnlyVerifiedPackExecutable(t *testing.T) {
	root := t.TempDir()
	packMarker := filepath.Join(root, "bundled-ran")
	packPath := filepath.Join(root, "bin", "ffmpeg")
	packContents := writeSyntheticFFmpeg(t, packPath, packMarker)
	packSum := sha256.Sum256(packContents)
	verified, err := VerifyPackFFmpeg(root, packPath, hex.EncodeToString(packSum[:]))
	if err != nil {
		t.Fatalf("VerifyPackFFmpeg: %v", err)
	}

	pathDir := t.TempDir()
	hostMarker := filepath.Join(t.TempDir(), "host-ran")
	hostPath := filepath.Join(pathDir, "ffmpeg")
	writeSyntheticFFmpeg(t, hostPath, hostMarker)
	t.Setenv("PATH", pathDir)

	dec, err := NewFFmpegDecoder(FFmpegDecoderConfig{
		Mode:           FFmpegExecutionHermetic,
		BinaryPath:     "ffmpeg",
		VerifiedFFmpeg: &verified,
	}, 32, 32, nil, nil)
	if err != nil {
		t.Fatalf("NewFFmpegDecoder: %v", err)
	}
	if dec.cmd.Path != verified.path {
		_ = dec.Close()
		t.Fatalf("decoder executable = %q, want bundled absolute path %q", dec.cmd.Path, verified.path)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(packMarker); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = dec.Close()
	if _, err := os.Stat(packMarker); err != nil {
		t.Fatalf("bundled FFmpeg fixture did not run: %v", err)
	}
	if _, err := os.Stat(hostMarker); !os.IsNotExist(err) {
		t.Fatalf("PATH FFmpeg fixture ran unexpectedly (stat error %v)", err)
	}
}

func TestLegacyFFmpegStillResolvesFromPATH(t *testing.T) {
	pathDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "legacy-ran")
	legacyPath := filepath.Join(pathDir, "ffmpeg")
	writeSyntheticFFmpeg(t, legacyPath, marker)
	t.Setenv("PATH", pathDir)

	dec, err := NewFFmpegDecoder(FFmpegDecoderConfig{}, 32, 32, nil, nil)
	if err != nil {
		t.Fatalf("NewFFmpegDecoder legacy: %v", err)
	}
	if dec.cmd.Path != legacyPath {
		_ = dec.Close()
		t.Fatalf("legacy executable = %q, want %q", dec.cmd.Path, legacyPath)
	}
	waitForFile(t, marker)
	_ = dec.Close()
}

func TestVerifiedFFmpegRevalidatesAfterCapabilityCreation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "ffmpeg")
	original := writeSyntheticFFmpeg(t, path, "")
	sum := sha256.Sum256(original)
	verified, err := VerifyPackFFmpeg(root, path, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatalf("VerifyPackFFmpeg: %v", err)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := verified.Resolve(); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("Resolve error = %v, want hash mismatch after tampering", err)
	}
}

func TestCameraPipelinePropagatesHermeticFFmpegCapability(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "pipeline-ran")
	path := filepath.Join(root, "bin", "ffmpeg")
	contents := writeSyntheticFFmpeg(t, path, marker)
	sum := sha256.Sum256(contents)
	verified, err := VerifyPackFFmpeg(root, path, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatalf("VerifyPackFFmpeg: %v", err)
	}
	pathDir := t.TempDir()
	t.Setenv("PATH", pathDir)

	pipeline := newCameraPipeline("camera-1", rtsp.StreamDescriptor{Codec: "H264", Width: 32, Height: 32}, Config{
		FFmpegPath:     "ffmpeg",
		FFmpegMode:     FFmpegExecutionHermetic,
		VerifiedFFmpeg: &verified,
	}, nil, nil)
	decoder, err := pipeline.decoderFactory()
	if err != nil {
		t.Fatalf("pipeline decoder factory: %v", err)
	}
	got := decoder.(*FFmpegDecoder).cmd.Path
	if got != verified.path {
		_ = decoder.Close()
		t.Fatalf("pipeline decoder executable = %q, want %q", got, verified.path)
	}
	waitForFile(t, marker)
	_ = decoder.Close()
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func writeSyntheticFFmpeg(t *testing.T, path, marker string) []byte {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	contents := []byte("#!/bin/sh\n")
	if marker != "" {
		contents = append(contents, []byte("printf ran > '"+marker+"'\n")...)
	}
	contents = append(contents, []byte("cat >/dev/null\n")...)
	if err := os.WriteFile(path, contents, 0o700); err != nil {
		t.Fatal(err)
	}
	return contents
}
