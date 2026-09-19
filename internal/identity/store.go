package identity

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/platform"
)

const schemaVersion = 1

const fileName = "identity.json"

// fileRecord is the on-disk schema of identity.json. No secrets, ever.
type fileRecord struct {
	EdgeID        string `json:"edge_id"`
	CreatedAt     string `json:"created_at"`
	SchemaVersion int    `json:"schema_version"`
}

// ErrCorrupt wraps any identity.json content the agent refuses to trust.
// Callers must treat this as fatal, not as a signal to regenerate.
var ErrCorrupt = errors.New("identity: stored identity is invalid")

func identityPath(dataDir string) string {
	return filepath.Join(dataDir, fileName)
}

// loadOrCreate reads the persisted identity from dataDir, generating and
// persisting one on first run. A malformed file is a hard error.
func loadOrCreate(dataDir string) (Identity, error) {
	path := identityPath(dataDir)

	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return create(dataDir)
	case err != nil:
		return Identity{}, fmt.Errorf("identity: read %s: %w", path, err)
	}

	var rec fileRecord
	if jsonErr := json.Unmarshal(data, &rec); jsonErr != nil {
		return Identity{}, fmt.Errorf("%w: %s: invalid JSON: %v", ErrCorrupt, path, jsonErr)
	}
	if rec.SchemaVersion != schemaVersion {
		return Identity{}, fmt.Errorf("%w: %s: unsupported schema_version %d (want %d)",
			ErrCorrupt, path, rec.SchemaVersion, schemaVersion)
	}
	if !validUUID(rec.EdgeID) {
		return Identity{}, fmt.Errorf("%w: %s: invalid edge_id %q", ErrCorrupt, path, rec.EdgeID)
	}
	createdAt, timeErr := time.Parse(time.RFC3339, rec.CreatedAt)
	if timeErr != nil {
		return Identity{}, fmt.Errorf("%w: %s: invalid created_at %q: %v", ErrCorrupt, path, rec.CreatedAt, timeErr)
	}

	return Identity{
		EdgeID:    rec.EdgeID,
		CreatedAt: createdAt,
		Status:    StatusEnrolled,
		Source:    SourcePersisted,
	}, nil
}

// create generates a fresh identity and persists it to dataDir.
func create(dataDir string) (Identity, error) {
	id, err := newUUIDv4()
	if err != nil {
		return Identity{}, err
	}
	now := time.Now().UTC()

	if err := writeAtomic(dataDir, fileRecord{
		EdgeID:        id,
		CreatedAt:     now.Format(time.RFC3339),
		SchemaVersion: schemaVersion,
	}); err != nil {
		return Identity{}, err
	}

	return Identity{EdgeID: id, CreatedAt: now, Status: StatusEnrolled, Source: SourcePersisted}, nil
}

// writeAtomic creates dataDir if missing and writes identity.json via a
// temp-file-then-rename so a crash mid-write never leaves a partial file.
func writeAtomic(dataDir string, rec fileRecord) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("identity: create data dir %s: %w", dataDir, err)
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("identity: encode identity: %w", err)
	}

	tmp, err := os.CreateTemp(dataDir, ".identity-*.json.tmp")
	if err != nil {
		return fmt.Errorf("identity: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return platform.WrapDiskError(fmt.Errorf("identity: write temp file: %w", err))
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("identity: chmod temp file: %w", err)
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
		return platform.WrapDiskError(fmt.Errorf("identity: sync temp file: %w", err))
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("identity: close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, identityPath(dataDir)); err != nil {
		return platform.WrapDiskError(fmt.Errorf("identity: rename into place: %w", err))
	}
	return nil
}
