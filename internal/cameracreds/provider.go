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
		if c, ok := bestMatch(entries, ScopeDevice, stableIdentity); ok {
			return c, true
		}
	}
	if groupID != "" {
		if c, ok := bestMatch(entries, ScopeGroup, groupID); ok {
			return c, true
		}
	}
	return Credential{}, false
}

// bestMatch returns the credential of the given scope whose CandidateKeys
// contains candidateKey. If more than one matches — an ambiguous state the
// SaaS is expected to prevent via a 409 on assignment, but the Edge cannot
// rely on that silently against a stale/duplicate cache — it picks the one
// with the lexicographically smallest ID, so the result is deterministic
// regardless of Go's unordered map iteration in Store.Snapshot.
func bestMatch(entries []Credential, scope Scope, candidateKey string) (Credential, bool) {
	var best Credential
	found := false
	for _, c := range entries {
		if c.Scope != scope || !containsKey(c.CandidateKeys, candidateKey) {
			continue
		}
		if !found || c.ID < best.ID {
			best = c
			found = true
		}
	}
	return best, found
}

func containsKey(keys []string, key string) bool {
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}
