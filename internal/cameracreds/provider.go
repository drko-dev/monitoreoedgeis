package cameracreds

import "strings"

// ChannelCandidateKeySeparator is the delimiter separating a multi-channel
// physical device's stable identity from its channel/source token.
const ChannelCandidateKeySeparator = "|ch="

// SplitChannelCandidateKey decomposes a composite channel candidate key
// ("<deviceStableIdentity>|ch=<sourceToken>") into its device and source
// token parts. If the key is not a composite channel key, it returns ok=false.
func SplitChannelCandidateKey(candidateKey string) (deviceKey, sourceToken string, ok bool) {
	idx := strings.Index(candidateKey, ChannelCandidateKeySeparator)
	if idx <= 0 {
		return "", "", false
	}
	sourceToken = candidateKey[idx+len(ChannelCandidateKeySeparator):]
	if sourceToken == "" {
		return "", "", false
	}
	return candidateKey[:idx], sourceToken, true
}

// Provider resolves the camera credential to use for a discovered device.
//
// It is intentionally read-only and decoupled from internal/discovery: it
// takes a plain candidate-key string rather than importing discovery types, so
// this package stays usable without pulling in ONVIF/SOAP.
//
// Resolution is by CANDIDATE KEY with deterministic precedence:
//
//  1. Exact candidate match:
//     - a DEVICE-scoped credential whose CandidateKeys contains candidateKey;
//     - otherwise a GROUP-scoped credential whose CandidateKeys contains it.
//
//  2. Channel inheritance (shared recorder credential):
//     If candidateKey is a composite channel key ("<device>|ch=<token>"), and
//     no specific credential matched in step 1:
//     - a DEVICE-scoped credential whose CandidateKeys contains the physical device key;
//     - otherwise a GROUP-scoped credential whose CandidateKeys contains the physical device key.
//     Channels never inherit credentials from other sibling channels.
//
//  3. Recorder bootstrap fallback (device discovery / rediscovery):
//     If candidateKey is a physical device key (no "|ch="), and no direct device
//     credential matched in step 1:
//     - a DEVICE-scoped credential whose CandidateKeys contains ANY channel of
//       this device ("<candidateKey>|ch=...");
//     - otherwise a GROUP-scoped credential containing any channel of this device.
//     When multiple channel credentials exist, the tie-break is deterministic:
//     lexicographically smallest channel key first, then lexicographically smallest ID.
//
// Returns ok=false when no credential matches, with a zero Credential — there is no
// sentinel error, so a caller cannot mistake "no credential" for a failure
// that should block the rest of the cameras.
type Provider struct {
	store *Store
}

// NewProvider builds a Provider backed by store.
func NewProvider(store *Store) *Provider {
	return &Provider{store: store}
}

// Resolve returns the credential to use for the camera or recorder channel
// identified by candidateKey.
func (p *Provider) Resolve(candidateKey string) (Credential, bool) {
	if candidateKey == "" {
		return Credential{}, false
	}
	entries := p.store.Snapshot()

	// 1. Exact candidate match (DEVICE over GROUP)
	if c, ok := bestMatch(entries, ScopeDevice, candidateKey); ok {
		return c, true
	}
	if c, ok := bestMatch(entries, ScopeGroup, candidateKey); ok {
		return c, true
	}

	// 2. Channel inheritance: composite channel key inherits from physical recorder
	if deviceKey, _, isChannel := SplitChannelCandidateKey(candidateKey); isChannel {
		if c, ok := bestMatch(entries, ScopeDevice, deviceKey); ok {
			return c, true
		}
		if c, ok := bestMatch(entries, ScopeGroup, deviceKey); ok {
			return c, true
		}
		return Credential{}, false
	}

	// 3. Recorder bootstrap fallback: physical device key falls back to an enrolled channel credential
	if c, ok := bestChannelMatch(entries, ScopeDevice, candidateKey); ok {
		return c, true
	}
	if c, ok := bestChannelMatch(entries, ScopeGroup, candidateKey); ok {
		return c, true
	}

	return Credential{}, false
}

// ResolveExact matches ONLY the exact candidateKey without inheritance or fallback.
func (p *Provider) ResolveExact(candidateKey string) (Credential, bool) {
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

func bestChannelMatch(entries []Credential, scope Scope, deviceKey string) (Credential, bool) {
	prefix := deviceKey + ChannelCandidateKeySeparator
	var best Credential
	var bestKey string
	found := false
	for _, c := range entries {
		if c.Scope != scope {
			continue
		}
		channelKey, ok := firstChannelKeyWithPrefix(c.CandidateKeys, prefix)
		if !ok {
			continue
		}
		if !found || channelKey < bestKey || (channelKey == bestKey && c.ID < best.ID) {
			best = c
			bestKey = channelKey
			found = true
		}
	}
	return best, found
}

func firstChannelKeyWithPrefix(keys []string, prefix string) (string, bool) {
	var best string
	found := false
	for _, k := range keys {
		if strings.HasPrefix(k, prefix) && len(k) > len(prefix) {
			if !found || k < best {
				best = k
				found = true
			}
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
