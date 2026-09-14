package agent

// Build metadata. Version is the source of truth for the agent version;
// Commit and BuildDate are injected at build time via -ldflags, e.g.
//
//	go build -ldflags "-X .../internal/agent.Commit=abc123 -X .../internal/agent.BuildDate=2026-01-01T00:00:00Z"
var (
	Version   = "0.1.0"
	Commit    = "unknown"
	BuildDate = "unknown"
)
