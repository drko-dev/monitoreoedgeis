// Package credentials manages the SaaS enrollment credential: the rotatable
// secret issued after a successful gateway enrollment.
//
// This is deliberately a separate concept from internal/identity: edge_id
// (identity) is a stable, permanent device identifier that never changes;
// the credential is a secret that can be rotated or revoked without ever
// touching edge_id. Storage is a separate file (credentials.json) so the two
// concepts can never be confused or accidentally overwritten together.
package credentials

import "time"

// Status is the local enrollment status as this agent understands it from
// disk alone. It does NOT mean the SaaS still honors the credential — only
// an authenticated call (e.g. GET /edge/me) can prove that.
type Status string

const (
	// StatusUnenrolled means no valid credentials.json is present.
	StatusUnenrolled Status = "UNENROLLED"
	// StatusEnrolled means a credential is stored locally.
	StatusEnrolled Status = "ENROLLED"
	// StatusRevoked is never persisted here: it is reported by callers
	// (cmd/geocam-edge) after the SaaS explicitly rejects the stored
	// credential with 401/403 on an authenticated call.
	StatusRevoked Status = "REVOKED"
)

func (s Status) String() string { return string(s) }

// Credentials is the agent's resolved SaaS enrollment credential.
type Credentials struct {
	EdgeID            string
	DeviceID          string
	Credential        string // secret — never log, never expose over HTTP/status
	CredentialVersion int
	TenantID          string
	SiteID            string
	EnrolledAt        time.Time
	Status            Status
}

// IsEnrolled reports whether a credential is stored locally.
func (c Credentials) IsEnrolled() bool { return c.Status == StatusEnrolled }
