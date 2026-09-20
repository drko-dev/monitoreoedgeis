package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const schemaVersion = 1

const fileName = "credentials.json"

// fileRecord is the on-disk schema of credentials.json.
type fileRecord struct {
	SchemaVersion     int    `json:"schema_version"`
	EdgeID            string `json:"edge_id"`
	DeviceID          string `json:"device_id"`
	Credential        string `json:"credential"`
	CredentialVersion int    `json:"credential_version"`
	TenantID          string `json:"tenant_id"`
	SiteID            string `json:"site_id"`
	EnrolledAt        string `json:"enrolled_at"`
}

// ErrCorrupt wraps any credentials.json content the agent refuses to trust.
// Callers must treat this as fatal, not as a signal to regenerate.
var ErrCorrupt = errors.New("credentials: stored credentials are invalid")

func credentialsPath(dataDir string) string {
	return filepath.Join(dataDir, fileName)
}

// Exists reports whether credentials.json is present, without validating
// it. Used by CLI double-enrollment guards.
func Exists(dataDir string) bool {
	_, err := os.Stat(credentialsPath(dataDir))
	return err == nil
}

// Load reads credentials.json from dataDir. A missing file is not an error:
// it returns Credentials{Status: StatusUnenrolled}. A malformed file is a
// hard error — never silently ignored or regenerated.
func Load(dataDir string) (Credentials, error) {
	path := credentialsPath(dataDir)

	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Credentials{Status: StatusUnenrolled}, nil
	case err != nil:
		return Credentials{}, fmt.Errorf("credentials: read %s: %w", path, err)
	}

	var rec fileRecord
	if jsonErr := json.Unmarshal(data, &rec); jsonErr != nil {
		return Credentials{}, fmt.Errorf("%w: %s: invalid JSON: %v", ErrCorrupt, path, jsonErr)
	}
	if rec.SchemaVersion != schemaVersion {
		return Credentials{}, fmt.Errorf("%w: %s: unsupported schema_version %d (want %d)",
			ErrCorrupt, path, rec.SchemaVersion, schemaVersion)
	}
	if rec.EdgeID == "" || rec.Credential == "" {
		return Credentials{}, fmt.Errorf("%w: %s: missing edge_id or credential", ErrCorrupt, path)
	}
	enrolledAt, timeErr := time.Parse(time.RFC3339, rec.EnrolledAt)
	if timeErr != nil {
		return Credentials{}, fmt.Errorf("%w: %s: invalid enrolled_at %q: %v", ErrCorrupt, path, rec.EnrolledAt, timeErr)
	}

	return Credentials{
		EdgeID:            rec.EdgeID,
		DeviceID:          rec.DeviceID,
		Credential:        rec.Credential,
		CredentialVersion: rec.CredentialVersion,
		TenantID:          rec.TenantID,
		SiteID:            rec.SiteID,
		EnrolledAt:        enrolledAt,
		Status:            StatusEnrolled,
	}, nil
}

// Save atomically persists creds to dataDir via temp-file-then-rename: the
// write happens entirely to a temp file first, and only a fully successful
// write is renamed into place. A failure at any point before that final
// rename leaves any existing credentials.json completely untouched — this
// is what makes credential rotation safe to retry after a mid-write error.
func Save(dataDir string, creds Credentials) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("credentials: create data dir %s: %w", dataDir, err)
	}

	rec := fileRecord{
		SchemaVersion:     schemaVersion,
		EdgeID:            creds.EdgeID,
		DeviceID:          creds.DeviceID,
		Credential:        creds.Credential,
		CredentialVersion: creds.CredentialVersion,
		TenantID:          creds.TenantID,
		SiteID:            creds.SiteID,
		EnrolledAt:        creds.EnrolledAt.Format(time.RFC3339),
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("credentials: encode credentials: %w", err)
	}

	tmp, err := os.CreateTemp(dataDir, ".credentials-*.json.tmp")
	if err != nil {
		return fmt.Errorf("credentials: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("credentials: write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("credentials: sync temp file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("credentials: chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("credentials: close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, credentialsPath(dataDir)); err != nil {
		return fmt.Errorf("credentials: rename into place: %w", err)
	}
	return nil
}
