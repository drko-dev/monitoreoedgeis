package anpr

// Authorizer decides whether a camera is commercially entitled to run ANPR
// candidate extraction at all — a SaaS/entitlement decision this Edge
// package never makes itself (spec item 35). It is a pure external
// boundary: PREP ships only fakes, never a reimplementation of the SaaS's
// J1-J4 entitlement logic.
//
// When ANPRAllowed returns false, the registry guarantees zero mutation:
// no burst is opened or touched, no crop is computed, no dedupe/quality
// state is written, no envelope is built (spec item 35/36 — see
// registry.go's ProcessCandidate).
type Authorizer interface {
	ANPRAllowed(cameraKey string) bool
}

// AllowAllAuthorizer authorizes every camera. Useful for tests and for a
// deployment that hasn't wired a real entitlement check yet — but it must
// be chosen explicitly, never a silent default inside the registry (see
// registry.go: a nil Authorizer denies everything, fail-closed).
type AllowAllAuthorizer struct{}

func (AllowAllAuthorizer) ANPRAllowed(string) bool { return true }

// DenyAllAuthorizer denies every camera. Useful to test the zero-mutation
// guarantee (B22).
type DenyAllAuthorizer struct{}

func (DenyAllAuthorizer) ANPRAllowed(string) bool { return false }

// StaticAuthorizer authorizes exactly the cameras listed, deterministically
// — a simple fake for tests that need a mixed allow/deny set.
type StaticAuthorizer map[string]bool

func (s StaticAuthorizer) ANPRAllowed(cameraKey string) bool { return s[cameraKey] }
