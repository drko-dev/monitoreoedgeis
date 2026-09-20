package cameracreds

// Provider resolves the camera credential to use for a discovered device.
//
// It is intentionally read-only and decoupled from internal/discovery: it
// takes a plain candidate-key string rather than importing discovery types, so
// this package stays usable without pulling in ONVIF/SOAP.
//
// Resolution is by CANDIDATE KEY for both scopes, in this order:
//
//  1. a DEVICE-scoped credential whose CandidateKeys contains candidateKey;
//  2. otherwise a GROUP-scoped credential whose CandidateKeys contains it.
//
// DEVICE wins. That matches the SaaS contract exactly: the device-facing sync
// endpoint resolves DEVICE-over-GROUP precedence server-side
// (monitoreoia's db_postgres.resolve_gateway_camera_credentials) and sends the
// real per-camera candidate keys for BOTH scopes. There is therefore no group
// identifier for the Edge to supply, and none is invented here.
//
// The candidateKey must be the stable candidate identity from discovery
// (Hito E's StableIdentity) — never an IP address.
type Provider struct {
	store *Store
}

// NewProvider builds a Provider backed by store.
func NewProvider(store *Store) *Provider {
	return &Provider{store: store}
}

// Resolve returns the credential to use for the camera identified by
// candidateKey, preferring a DEVICE-scoped assignment over a GROUP-scoped one.
// Returns ok=false when neither matches, with a zero Credential — there is no
// sentinel error, so a caller cannot mistake "no credential" for a failure
// that should block the rest of the cameras.
func (p *Provider) Resolve(candidateKey string) (Credential, bool) {
	if candidateKey == "" {
		return Credential{}, false
	}
	entries := p.store.Snapshot()

	if c, ok := bestMatch(entries, ScopeDevice, candidateKey); ok {
		return c, true
	}
	if c, ok := bestMatch(entries, ScopeGroup, candidateKey); ok {
		return c, true
	}
	return Credential{}, false
}

// bestMatch returns the credential of the given scope whose CandidateKeys
// contains candidateKey. If more than one matches — an ambiguous state the
// SaaS is expected to prevent by rejecting a second GROUP assignment over the
// same candidate with a 409, but the Edge cannot rely on that silently against
// a stale/duplicate cache — it picks the one with the lexicographically
// smallest ID, so the result is deterministic regardless of Go's unordered map
// iteration in Store.Snapshot.
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
