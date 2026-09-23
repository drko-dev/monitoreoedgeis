package cameracreds

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const schemaVersion = 1

const fileName = "camera_credentials.json"

// ErrCorrupt wraps any camera_credentials.json content the agent refuses to
// trust. Like internal/identity and internal/credentials, this is fatal —
// never silently discarded and recreated.
var ErrCorrupt = errors.New("cameracreds: stored camera credentials are invalid")

// fileRecord is the on-disk schema. Password is never present in plaintext:
// PasswordEnc is base64(nonce || AES-256-GCM ciphertext), sealed under the
// local master key from LoadOrCreateMasterKey.
type fileRecord struct {
	SchemaVersion int           `json:"schema_version"`
	UpdatedAt     string        `json:"updated_at"`
	Entries       []entryRecord `json:"entries"`
}

type entryRecord struct {
	ID            string   `json:"id"`
	Scope         string   `json:"scope"`
	CandidateKeys []string `json:"candidate_keys"`
	Username      string   `json:"username"`
	Revision      int      `json:"revision"`
	PasswordEnc   string   `json:"password_enc"`
}

func credentialsPath(dataDir string) string {
	return filepath.Join(dataDir, fileName)
}

// Store holds the in-memory camera-credentials cache and persists it,
// encrypted, to camera_credentials.json. All access is safe for concurrent
// use.
type Store struct {
	dataDir   string
	masterKey []byte

	mu      sync.RWMutex
	entries map[string]Credential // keyed by Credential.ID
}

// OpenStore loads dataDir/camera_credentials.json (if present) and returns a
// ready-to-use Store. A missing file is not an error: the store starts
// empty. A malformed file is a hard error, never silently discarded.
func OpenStore(dataDir string, masterKey []byte) (*Store, error) {
	if len(masterKey) != MasterKeySize {
		return nil, fmt.Errorf("cameracreds: master key must be %d bytes", MasterKeySize)
	}
	s := &Store{dataDir: dataDir, masterKey: masterKey, entries: map[string]Credential{}}

	data, err := os.ReadFile(credentialsPath(dataDir))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("cameracreds: read %s: %w", credentialsPath(dataDir), err)
	}

	var rec fileRecord
	if jsonErr := json.Unmarshal(data, &rec); jsonErr != nil {
		return nil, fmt.Errorf("%w: invalid JSON: %v", ErrCorrupt, jsonErr)
	}
	if rec.SchemaVersion != schemaVersion {
		return nil, fmt.Errorf("%w: unsupported schema_version %d (want %d)", ErrCorrupt, rec.SchemaVersion, schemaVersion)
	}
	for _, e := range rec.Entries {
		password, decErr := decryptSecret(masterKey, e.PasswordEnc)
		if decErr != nil {
			return nil, fmt.Errorf("%w: entry %s: %v", ErrCorrupt, e.ID, decErr)
		}
		cred := Credential{
			ID:            e.ID,
			Scope:         Scope(e.Scope),
			CandidateKeys: e.CandidateKeys,
			Username:      e.Username,
			Password:      password,
			Revision:      e.Revision,
		}
		if valErr := cred.validate(); valErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrCorrupt, valErr)
		}
		s.entries[e.ID] = cred
	}
	return s, nil
}

// Snapshot returns a copy of every cached credential.
func (s *Store) Snapshot() []Credential {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Credential, 0, len(s.entries))
	for _, c := range s.entries {
		out = append(out, c)
	}
	return out
}

// ApplyStats reports what one Apply did, for sanitized observability. It
// carries counts only — never an id, candidate key, username or password.
type ApplyStats struct {
	Added   int
	Updated int
	Removed int
}

// Changed reports whether the cache contents differ from before the Apply.
func (s ApplyStats) Changed() bool {
	return s.Added > 0 || s.Updated > 0 || s.Removed > 0
}

// Apply merges incoming (the SaaS's current, authoritative set of active
// credentials) into the cache and persists the result if anything changed:
//
//   - an entry not yet cached is added;
//   - an entry cached at a lower Revision is replaced (revision mayor
//     reemplaza);
//   - an entry cached at the same or a higher Revision than incoming is left
//     untouched (same revision = no-op; stale/lower incoming revision is
//     ignored);
//   - a cached entry whose ID is absent from incoming is removed.
//
// Absence-means-removed is the CORRECT and REQUIRED semantics: this endpoint is
// a full authoritative snapshot, so a successful 200 with an empty list
// legitimately means "this gateway has no active camera credentials", and a
// revoked credential is expressed by omitting it. There is deliberately NO
// "empty snapshot protection" here — retaining entries the SaaS no longer lists
// would resurrect revoked camera access. Last-good-cache behaviour belongs to
// fetch/decode failures upstream (see Syncer), not to a successful snapshot.
//
// incoming must already be validated; malformed input is rejected by the
// caller (see Syncer) before it ever reaches Apply, so the cache is never
// partially corrupted by a bad payload.
func (s *Store) Apply(incoming []Credential) (ApplyStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var stats ApplyStats
	next := make(map[string]Credential, len(incoming))
	for _, c := range incoming {
		existing, ok := s.entries[c.ID]
		if !ok {
			next[c.ID] = c
			stats.Added++
			continue
		}
		if c.Revision > existing.Revision {
			next[c.ID] = c
			stats.Updated++
			continue
		}
		if c.Revision == existing.Revision {
			if !candidateKeysEqual(c.CandidateKeys, existing.CandidateKeys) || c.Scope != existing.Scope {
				next[c.ID] = c
				stats.Updated++
				continue
			}
		}
		// Same or stale revision: keep what's already cached.
		next[c.ID] = existing
	}
	for id := range s.entries {
		if _, ok := next[id]; !ok {
			stats.Removed++
		}
	}

	if !stats.Changed() {
		s.entries = next
		return stats, nil
	}
	prev := s.entries
	s.entries = next
	if err := s.persistLocked(); err != nil {
		s.entries = prev // memoria y disco siguen coincidiendo; el próximo intento reintenta
		return ApplyStats{}, err
	}
	return stats, nil
}

// persistLocked writes the current entries to disk atomically. Callers must
// hold s.mu.
func (s *Store) persistLocked() error {
	rec := fileRecord{
		SchemaVersion: schemaVersion,
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
		Entries:       make([]entryRecord, 0, len(s.entries)),
	}
	for _, c := range s.entries {
		enc, err := encryptSecret(s.masterKey, c.Password)
		if err != nil {
			return fmt.Errorf("cameracreds: encrypt credential %s: %w", c.ID, err)
		}
		rec.Entries = append(rec.Entries, entryRecord{
			ID:            c.ID,
			Scope:         string(c.Scope),
			CandidateKeys: c.CandidateKeys,
			Username:      c.Username,
			Revision:      c.Revision,
			PasswordEnc:   enc,
		})
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("cameracreds: encode camera credentials: %w", err)
	}
	return writeFileAtomic(s.dataDir, fileName, data)
}

func candidateKeysEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	count := make(map[string]int, len(a))
	for _, x := range a {
		count[x]++
	}
	for _, y := range b {
		count[y]--
		if count[y] < 0 {
			return false
		}
	}
	for _, v := range count {
		if v != 0 {
			return false
		}
	}
	return true
}
