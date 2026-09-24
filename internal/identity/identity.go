// Package identity resolves and persists the agent's own edge_id.
//
// edge_id is a random UUID that belongs to this GEO CAM Edge *instance* — it
// is never derived from hostname, MAC, serial or any other hardware trait,
// so re-imaging or moving the binary to different hardware does not change
// it. It is generated once and persisted to <data-dir>/identity.json.
//
// This is device identity only. Real SaaS enrollment (tokens, certs, tenant
// association) is a later milestone (Hito C) and is not modeled here.
package identity

import (
	"errors"
	"time"
)

// ErrManagedEdgeIDOverride indicates that a managed agent attempted to select
// its identity from GEOCAM_EDGE_ID instead of the persisted identity file.
var ErrManagedEdgeIDOverride = errors.New("identity: GEOCAM_EDGE_ID override is forbidden in managed mode")

// EnrollmentStatus reflects whether this agent has a resolved edge_id, not
// whether it is registered with the SaaS (that is Hito C's concern).
type EnrollmentStatus string

const (
	// StatusUnenrolled means no edge_id could be resolved.
	StatusUnenrolled EnrollmentStatus = "UNENROLLED"
	// StatusEnrolled means edge_id is present and usable.
	StatusEnrolled EnrollmentStatus = "ENROLLED"
)

func (s EnrollmentStatus) String() string { return string(s) }

// Source records where EdgeID came from.
type Source string

const (
	// SourcePersisted means EdgeID was read from (or created in) identity.json.
	SourcePersisted Source = "persisted"
	// SourceEnvOverride means EdgeID came from the GEOCAM_EDGE_ID dev override
	// and identity.json was neither read nor written.
	SourceEnvOverride Source = "env-override"
)

// Identity is the agent's resolved identity.
type Identity struct {
	EdgeID    string
	CreatedAt time.Time
	Status    EnrollmentStatus
	Source    Source
}

// IsEnrolled reports whether the agent has a resolved edge_id.
func (i Identity) IsEnrolled() bool { return i.Status == StatusEnrolled }

// Load resolves the agent identity.
//
// envEdgeID, when non-empty, is the GEOCAM_EDGE_ID dev override: it is used
// verbatim and identity.json is never touched. This exists for local/dev
// testing only — it is documented as a secondary, non-authoritative path.
//
// Otherwise identity.json under dataDir is the single source of truth: it is
// read if present, or generated and persisted on first run. A corrupt file
// (bad JSON, invalid edge_id, unsupported schema_version) is a hard error —
// the caller must not fall back to silently generating a new identity, since
// that would silently orphan the device from anything already associated
// with the old edge_id.
func Load(dataDir, envEdgeID string) (Identity, error) {
	if envEdgeID != "" {
		return Identity{EdgeID: envEdgeID, Status: StatusEnrolled, Source: SourceEnvOverride}, nil
	}
	return loadOrCreate(dataDir)
}

// LoadExisting returns the persisted identity only. It never honors an
// environment override and never creates or repairs identity.json.
func LoadExisting(dataDir string) (Identity, error) {
	return loadExisting(dataDir)
}
