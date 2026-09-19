// Package factoryreset removes the local device state while preserving the
// installed Edge software and release artifacts.
package factoryreset

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrConfirmationRequired = errors.New("factory reset requires explicit local confirmation")

// StatePaths is the allowlist of device state removed by Reset. Release and
// software paths are intentionally absent from this list.
var StatePaths = []string{
	"identity.json",
	"credentials.json",
	"camera_credentials.json",
	"camera_master.key",
	"remote_config_state.json",
	"control_ledger.json",
	"local-event-backlog",
	"cloud-buffer",
	"events",
	"evidence",
}

// Reset removes only allowlisted state below dataDir. The caller must pass
// confirmed=true; there is no implicit or unattended reset path.
func Reset(dataDir string, confirmed bool) error {
	if !confirmed {
		return ErrConfirmationRequired
	}
	if err := validateDataDir(dataDir); err != nil {
		return err
	}

	root := filepath.Clean(dataDir)
	for _, rel := range StatePaths {
		path := filepath.Join(root, rel)
		if !isWithin(root, path) {
			return fmt.Errorf("factory reset: refusing path outside data dir: %s", rel)
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("factory reset: remove %s: %w", rel, err)
		}
	}
	return nil
}

func validateDataDir(dataDir string) error {
	if strings.TrimSpace(dataDir) == "" {
		return errors.New("factory reset: data dir is required")
	}
	root, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("factory reset: resolve data dir: %w", err)
	}
	if root == string(filepath.Separator) || root == "." {
		return fmt.Errorf("factory reset: refusing unsafe data dir %q", dataDir)
	}
	return nil
}

func isWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
