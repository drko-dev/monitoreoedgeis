package cameracreds

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/drko-dev/monitoreoedgeis/internal/platform"
)

// writeFileAtomic creates dataDir if missing and writes data to name inside
// it via temp-file-then-rename, matching the pattern used by
// internal/identity and internal/credentials: a crash mid-write never
// leaves a partial file, and the directory/file get 0700/0600 permissions.
func writeFileAtomic(dataDir, name string, data []byte) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("cameracreds: create data dir %s: %w", dataDir, err)
	}

	tmp, err := os.CreateTemp(dataDir, "."+name+"-*.tmp")
	if err != nil {
		return fmt.Errorf("cameracreds: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return platform.WrapDiskError(fmt.Errorf("cameracreds: write temp file: %w", err))
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("cameracreds: sync temp file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("cameracreds: chmod temp file: %w", err)
	}
	// Flush the file's data and metadata BEFORE the rename publishes it.
	// Without this, temp-file-then-rename is atomic with respect to ordering
	// but not with respect to durability: on a filesystem with delayed
	// allocation a power loss after the rename can leave the new name
	// pointing at a zero-length or partially-written file. For this call site
	// that is fatal rather than merely annoying -- a truncated identity.json,
	// credentials.json or camera_master.key is treated as corruption and never
	// regenerated, so the agent would stay DEGRADED until an operator
	// intervened. These are cold paths (enrollment, rotation, a credential
	// sync), so the fsync costs nothing in steady state.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return platform.WrapDiskError(fmt.Errorf("cameracreds: sync temp file: %w", err))
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cameracreds: close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, filepath.Join(dataDir, name)); err != nil {
		return platform.WrapDiskError(fmt.Errorf("cameracreds: rename into place: %w", err))
	}
	return nil
}
