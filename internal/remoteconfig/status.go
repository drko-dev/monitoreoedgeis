package remoteconfig

// Status is the small block published to /status under "remote_config". It
// never carries the full config document -- only version/lifecycle
// bookkeeping -- since the payload may hold operationally sensitive
// runtime knobs (ROI coordinates, model paths) that don't belong on an
// unauthenticated or broadly-scoped status surface.
type Status struct {
	DesiredVersion  int64  `json:"desired_version,omitempty"`
	AppliedVersion  int64  `json:"applied_version"`
	LastApplyStatus string `json:"last_apply_status,omitempty"`
	LastApplyAt     string `json:"last_apply_at,omitempty"`
	LastErrorSafe   string `json:"last_error_safe,omitempty"`
	RollbackCount   int64  `json:"rollback_count"`
}

// HealthSink lets Module publish its status snapshot into the agent health
// reporter, mirroring internal/fulledge.HealthSink.
type HealthSink interface {
	SetRemoteConfigStatus(Status)
}

// Status returns the current small status snapshot for /status.
// desiredVersion is the version last seen from SaaS (0 if never polled
// successfully, or unknown); it is tracked by the poller (Module), not by
// this package's own state, since a rejected/failed poll result is still
// worth surfacing as "desired" even when never persisted as applied.
func (s State) toStatus(desiredVersion int64) Status {
	return Status{
		DesiredVersion:  desiredVersion,
		AppliedVersion:  s.AppliedVersion,
		LastApplyStatus: string(s.LastApplyStatus),
		LastApplyAt:     s.LastApplyAt,
		LastErrorSafe:   s.LastErrorSafe,
		RollbackCount:   s.RollbackCount,
	}
}
