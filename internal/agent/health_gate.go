package agent

import (
	"sync"

	"github.com/drko-dev/monitoreoedgeis/internal/health"
)

// agentHealthGate derives the agent-wide health state from its causes instead of
// letting each caller write it directly.
//
// The distinction it encodes is the one Y10 turns on: some DEGRADED causes are
// facts about this process run (a corrupt identity file, a module that failed to
// start) and only a restart clears them — but one cause is genuinely dynamic. A
// 401/403 from the SaaS marks the whole Edge DEGRADED because an Edge the SaaS
// refuses to recognise is not doing its job; the same credential can start
// working again the moment an operator re-enables the device, and before this
// gate existed nothing ever wrote READY again. The agent stayed DEGRADED — and
// /readyz returned 503 — until the process was restarted, which is exactly the
// "incorrectly latched" failure Y10 asks about. Worse, `geocam-edge check` and
// the appliance's own wait-ready.sh readiness gate both read that state, and
// update.sh rolls back a release when /readyz does not come up in time.
//
// Only the dynamic cause is clearable. Everything else stays degraded on
// purpose: reconnecting a component cannot fix a missing credential file, and
// pretending otherwise would hide a real fault.
type agentHealthGate struct {
	mu       sync.Mutex
	reporter *health.Reporter
	// startupDegraded is set once by Run when identity, credentials, module
	// startup or a module construction failed. It never clears.
	startupDegraded bool
	// credentialRevoked mirrors the heartbeat module's own 401/403 verdict and
	// is cleared when a heartbeat succeeds again.
	credentialRevoked bool
	// published becomes true once Run has finished deciding the initial state.
	// Until then the gate does not touch the reporter, so a recovery hook that
	// fires during startup cannot publish a state Run is about to overwrite.
	published bool
}

func newAgentHealthGate(reporter *health.Reporter) *agentHealthGate {
	return &agentHealthGate{reporter: reporter}
}

// aggregate reports the state implied by the current causes. Callers must hold
// g.mu.
func (g *agentHealthGate) aggregate() health.State {
	if g.startupDegraded || g.credentialRevoked {
		return health.StateDegraded
	}
	return health.StateReady
}

// publish applies the aggregate state, unless the lifecycle has moved past the
// point where the gate owns it. Shutdown sets STOPPING and must never be
// overwritten by a late recovery: an Edge on its way down is not READY.
func (g *agentHealthGate) publish() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.publishLocked()
}

func (g *agentHealthGate) publishLocked() {
	if !g.published {
		return
	}
	switch g.reporter.State() {
	case health.StateStopping:
		return
	}
	g.reporter.Set(g.aggregate())
}

// markStartupDegraded records a cause that only a restart clears. It is called
// from Run's startup switch, i.e. before the gate is published, and the state
// Run publishes at the end of that switch is what makes it visible.
func (g *agentHealthGate) markStartupDegraded() {
	g.mu.Lock()
	g.startupDegraded = true
	g.mu.Unlock()
}

// MarkCredentialRevoked records the SaaS's 401/403 verdict.
func (g *agentHealthGate) MarkCredentialRevoked() {
	g.mu.Lock()
	g.credentialRevoked = true
	g.publishLocked()
	g.mu.Unlock()
}

// ClearCredentialRevoked clears the revocation cause and republishes. It is a
// no-op when nothing was revoked, so a run of successful heartbeats does not
// repeatedly rewrite the same state.
func (g *agentHealthGate) ClearCredentialRevoked() {
	g.mu.Lock()
	if !g.credentialRevoked {
		g.mu.Unlock()
		return
	}
	g.credentialRevoked = false
	g.publishLocked()
	g.mu.Unlock()
}

// activate publishes the initial aggregate state and hands ownership of
// READY/DEGRADED to the gate for the rest of the process lifetime. It must be
// called exactly once, at the end of Run's startup switch.
func (g *agentHealthGate) activate() health.State {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.published = true
	g.publishLocked()
	return g.aggregate()
}

// credentialHealth is the narrow surface the heartbeat module needs to mirror a
// credential-revocation verdict into the agent-wide state, and to clear it when
// the SaaS accepts the credential again. Implemented by *agentHealthGate.
type credentialHealth interface {
	MarkCredentialRevoked()
	ClearCredentialRevoked()
}
