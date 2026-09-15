// Package cameracreds manages per-camera ONVIF/RTSP credentials synced from
// the SaaS: a local AES-256-GCM-encrypted cache, keyed by a stable device
// identity (Hito E's StableIdentity) or by a SaaS-defined group id, resolved
// with DEVICE-over-GROUP precedence via CredentialProvider.
//
// This is deliberately separate from internal/credentials, which manages the
// Edge's own SaaS enrollment credential. Neither package imports the other.
package cameracreds

import "fmt"

// Scope is the assignment level a Credential was issued at.
type Scope string

const (
	// ScopeDevice targets one camera by its stable identity.
	ScopeDevice Scope = "DEVICE"
	// ScopeGroup targets every camera in a SaaS-defined group.
	ScopeGroup Scope = "GROUP"
)

// Credential is one resolved camera credential, held in memory with its
// password in plaintext. It is only ever written to disk through Store,
// which encrypts Password before serializing.
type Credential struct {
	// ID is the SaaS-issued stable identifier for this credential entry.
	ID string
	// Scope is ScopeDevice or ScopeGroup.
	Scope Scope
	// TargetID is the stable_identity (ScopeDevice) or the SaaS group id
	// (ScopeGroup) this credential applies to. Never an IP address.
	TargetID string
	Username string
	// Password is the plaintext secret. Never logged, never encoded in a
	// String()/error message.
	Password string
	// Revision is the SaaS-side version of this entry, used for
	// idempotent, out-of-order-safe sync: a fetch carrying a Revision no
	// greater than what is already cached is a no-op for that entry.
	Revision int
}

func (c Credential) validate() error {
	if c.ID == "" {
		return fmt.Errorf("cameracreds: credential missing id")
	}
	if c.Scope != ScopeDevice && c.Scope != ScopeGroup {
		return fmt.Errorf("cameracreds: credential %s: invalid scope %q", c.ID, c.Scope)
	}
	if c.TargetID == "" {
		return fmt.Errorf("cameracreds: credential %s: missing target_id", c.ID)
	}
	if c.Username == "" {
		return fmt.Errorf("cameracreds: credential %s: missing username", c.ID)
	}
	if c.Password == "" {
		return fmt.Errorf("cameracreds: credential %s: missing password", c.ID)
	}
	return nil
}
