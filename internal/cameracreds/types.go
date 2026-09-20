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

// parseScope normalizes a scope string arriving from the SaaS into this
// package's Scope constants. It is the single place where the wire
// representation is translated, so a SaaS contract change is caught here
// rather than silently producing a credential that never matches.
//
// The SaaS sends the LOWERCASE values of its scope VARCHAR(16) column
// ("device" | "group" — see monitoreoia's camera_credentials table and
// routers/camera_credentials.py). This package's own constants are uppercase
// for historical reasons; the translation lives here.
//
// Anything else — including the uppercase wire form, which the SaaS does not
// send — is an error, and the caller must reject the entire payload rather
// than cache a credential whose scope it does not understand.
func parseScope(raw string) (Scope, error) {
	switch raw {
	case "device":
		return ScopeDevice, nil
	case "group":
		return ScopeGroup, nil
	default:
		return "", fmt.Errorf("cameracreds: unknown credential scope %q", raw)
	}
}

// Credential is one resolved camera credential, held in memory with its
// password in plaintext. It is only ever written to disk through Store,
// which encrypts Password before serializing.
type Credential struct {
	// ID is the SaaS numeric credential id (BIGSERIAL), rendered as the
	// canonical decimal string this package and Store key on. The conversion
	// happens once, at the transport boundary in decodePayload, so every
	// other layer sees a stable string.
	ID string
	// Scope is ScopeDevice or ScopeGroup, already normalized from the SaaS
	// lowercase wire form at the boundary.
	Scope Scope
	// CandidateKeys are the stable candidate identities this credential
	// applies to — never an IP address, and never a group id. The SaaS sends
	// the real per-camera candidate keys for BOTH scopes, so resolution is by
	// candidate key in either case (see Provider.Resolve). A DEVICE
	// credential has exactly one entry; a GROUP credential can have N (one
	// per device assigned to that group).
	CandidateKeys []string
	Username      string
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
	if len(c.CandidateKeys) == 0 {
		return fmt.Errorf("cameracreds: credential %s: missing candidate_keys", c.ID)
	}
	for _, k := range c.CandidateKeys {
		if k == "" {
			return fmt.Errorf("cameracreds: credential %s: empty candidate_key", c.ID)
		}
	}
	if c.Username == "" {
		return fmt.Errorf("cameracreds: credential %s: missing username", c.ID)
	}
	if c.Password == "" {
		return fmt.Errorf("cameracreds: credential %s: missing password", c.ID)
	}
	return nil
}
