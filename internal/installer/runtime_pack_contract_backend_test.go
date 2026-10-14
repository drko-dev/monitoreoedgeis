package installer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildRuntimePackWorkerLaunchSpecPinsBackend(t *testing.T) {
	t.Run("backend must be declared", func(t *testing.T) {
		root, manifest := syntheticMediaPack(t)
		manifest.Worker.BackendRelativePath = ""
		manifest.Worker.BackendSHA256 = ""
		manifest.Worker.BackendSizeBytes = 0
		_, err := BuildRuntimePackWorkerLaunchSpec(manifest, root, t.TempDir(), t.TempDir(), PlatformDarwin, ArchArm64)
		if !errors.Is(err, ErrRuntimePackArtifactsUnverified) {
			t.Fatalf("unpinned backend must fail closed: %v", err)
		}
	})
	t.Run("backend tampering invalidates manifest", func(t *testing.T) {
		root, manifest := syntheticMediaPack(t)
		backend := filepath.Join(root, manifest.Worker.BackendRelativePath)
		if err := os.WriteFile(backend, []byte("tampered backend"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := BuildRuntimePackWorkerLaunchSpec(manifest, root, t.TempDir(), t.TempDir(), PlatformDarwin, ArchArm64)
		if !errors.Is(err, ErrArtifactCorrupted) {
			t.Fatalf("tampered backend must fail closed: %v", err)
		}
	})
	t.Run("backend outside worker directory rejected", func(t *testing.T) {
		root, manifest := syntheticMediaPack(t)
		manifest.Worker.BackendRelativePath = "media/backend.py"
		_, err := BuildRuntimePackWorkerLaunchSpec(manifest, root, t.TempDir(), t.TempDir(), PlatformDarwin, ArchArm64)
		if err == nil {
			t.Fatal("worker backend must come from the expected worker directory")
		}
	})
}
