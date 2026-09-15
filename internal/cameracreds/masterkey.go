package cameracreds

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// MasterKeySize is the length in bytes of the local AES-256 master key.
const MasterKeySize = 32

const masterKeyFileName = "camera_master.key"

// ErrCorruptMasterKey wraps any camera_master.key content the agent refuses
// to trust. Callers must treat this as fatal: regenerating it would make any
// existing encrypted camera_credentials.json permanently unreadable.
var ErrCorruptMasterKey = errors.New("cameracreds: stored master key is invalid")

func masterKeyPath(dataDir string) string {
	return filepath.Join(dataDir, masterKeyFileName)
}

// LoadOrCreateMasterKey reads the local camera-credentials master key from
// dataDir, generating and persisting a fresh random one on first run.
//
// The key is never derived from the enrollment credential: that credential
// rotates, and doing so must never make the camera-credentials cache
// unreadable. A present-but-corrupt key file (wrong size, unreadable) is a
// hard error — it is never silently regenerated, since that would orphan
// any already-encrypted cache. The key value and its length are never
// logged by this package.
func LoadOrCreateMasterKey(dataDir string) ([]byte, error) {
	path := masterKeyPath(dataDir)

	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return createMasterKey(dataDir)
	case err != nil:
		return nil, fmt.Errorf("%w: %s: %v", ErrCorruptMasterKey, path, err)
	}

	if len(data) != MasterKeySize {
		return nil, fmt.Errorf("%w: %s: unexpected key length", ErrCorruptMasterKey, path)
	}
	return data, nil
}

func createMasterKey(dataDir string) ([]byte, error) {
	key := make([]byte, MasterKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("cameracreds: generate master key: %w", err)
	}
	if err := writeFileAtomic(dataDir, masterKeyFileName, key); err != nil {
		return nil, err
	}
	return key, nil
}
