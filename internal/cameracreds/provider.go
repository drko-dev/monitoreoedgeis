package cameracreds

// Provider resolves the camera credential to use for a discovered device.
//
// It is intentionally read-only and decoupled from internal/discovery: it
// takes plain identity strings rather than importing discovery types, so
// this package stays usable without pulling in ONVIF/SOAP. Wiring it into
// actual ONVIF authentication is the next block's job, not this one's.
type Provider struct {
	store *Store
}

// NewProvider builds a Provider backed by store.
func NewProvider(store *Store) *Provider {
	return &Provider{store: store}
}

// Resolve returns the credential to use for a device, preferring a
// DEVICE-scoped assignment matched by stableIdentity (Hito E's
// discovery.Candidate.StableIdentity — never an IP address) and falling
// back to a GROUP-scoped assignment matched by groupID. groupID may be
// empty when the device has no group. Returns ok=false when neither
// matches.
func (p *Provider) Resolve(stableIdentity, groupID string) (Credential, bool) {
	entries := p.store.Snapshot()

	if stableIdentity != "" {
		for _, c := range entries {
			if c.Scope == ScopeDevice && c.TargetID == stableIdentity {
				return c, true
			}
		}
	}
	if groupID != "" {
		for _, c := range entries {
			if c.Scope == ScopeGroup && c.TargetID == groupID {
				return c, true
			}
		}
	}
	return Credential{}, false
}
