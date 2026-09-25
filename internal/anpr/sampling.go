package anpr

// BurstSamplingHint lets an open LPR burst ask the existing sampler
// (processing.Sampler / processing.NewAdaptiveSampler, J4/J5) for a
// temporary sampling boost, without this package ever constructing or
// mutating a Sampler itself (spec item 26: "no implementarlo globalmente si
// requiere integración J4/J5").
//
// This is deliberately just an interface plus a no-op default: wiring a
// real implementation that calls processing.cameraPipeline.SetTargetFPS (or
// an equivalent bounded-by-ceiling API) is J6-C/integration work, out of
// scope for this PREP milestone. Any implementation MUST still respect
// processing.HybridConfig's documented ceiling (TargetFPS is never
// exceeded) — this interface does not, by itself, grant that; the
// implementer must.
type BurstSamplingHint interface {
	// RequestBurstFPS asks for a temporary sampling rate for cameraKey
	// while a burst identified by burstID is open. Implementations decide
	// whether/how to honor it (e.g. clamped to the existing TargetFPS
	// ceiling) and for how long; this interface makes no promise about
	// duration or success.
	RequestBurstFPS(cameraKey, burstID string, fps float64)
	// ReleaseBurstFPS signals burstID no longer needs an elevated rate.
	ReleaseBurstFPS(cameraKey, burstID string)
}

// NoopSamplingHint implements BurstSamplingHint by doing nothing — the
// default when no sampler integration is wired (which is every caller in
// this PREP milestone). Using it guarantees zero behavior change to
// existing sampler defaults (spec item 26).
type NoopSamplingHint struct{}

func (NoopSamplingHint) RequestBurstFPS(string, string, float64) {}
func (NoopSamplingHint) ReleaseBurstFPS(string, string)          {}
