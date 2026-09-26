package agent

import (
	"sync"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/remoteconfig"
)

// videoFPSController is the minimal seam samplerBurstHint needs from the
// real processing.Manager -- narrow enough to fake in tests without a real
// RTSP/decoder/pipeline stack, while production always goes through
// managerFPSController (below), which is a thin, logic-free adapter over
// the actual Manager/Sampler (item D: no second sampler, no invented
// semantics -- this interface exists only for testability of
// samplerBurstHint's own request/release/compose bookkeeping).
type videoFPSController interface {
	// CurrentTargetFPS returns cameraKey's live TargetFPS. ok=false means
	// the camera has no active pipeline right now -- nothing to boost.
	CurrentTargetFPS(cameraKey string) (fps float64, ok bool)
	SetTargetFPS(cameraKey string, fps float64) error
}

// managerFPSController is the real, production videoFPSController,
// wrapping *processing.Manager -- the SAME Sampler/SetTargetFPS the
// existing J4/J5 adaptive sampling and remote-config FPS changes already
// use.
type managerFPSController struct{ manager *processing.Manager }

func (c managerFPSController) CurrentTargetFPS(cameraKey string) (float64, bool) {
	p := c.manager.Pipeline(cameraKey)
	if p == nil {
		return 0, false
	}
	return p.Sampler().TargetFPS(), true
}

func (c managerFPSController) SetTargetFPS(cameraKey string, fps float64) error {
	return c.manager.SetTargetFPS(cameraKey, fps)
}

// samplerBurstHint implements anpr.BurstSamplingHint against the REAL
// existing processing.Sampler -- never a second sampler, never a
// hardcoded FPS (item D).
//
// Semantics: baseline -> burst -> automatic return to baseline.
//   - On the FIRST active burst for a camera, the camera's CURRENT
//     TargetFPS is captured as that camera's baseline (whatever J4/J5's
//     adaptive sampler or a prior remote-config apply had already set --
//     never assumed to be any particular number).
//   - Each active burst for that camera contributes its own requested FPS;
//     the effective TargetFPS applied is the max across all of them, never
//     below baseline, so several concurrent bursts on the same camera
//     compose correctly (item #6: releasing one never drops below what
//     another still needs).
//   - Once the last active burst for a camera releases, TargetFPS is
//     restored to the captured baseline -- deterministically, never left
//     elevated (item D "rollback/release determinístico").
//
// A nil controller (not wired yet, or this build has no video pipeline)
// makes every call a documented no-op -- never a panic.
type samplerBurstHint struct {
	mu         sync.Mutex
	controller videoFPSController
	cameras    map[string]*cameraBoostState
}

type cameraBoostState struct {
	baselineFPS float64
	active      map[string]float64 // burstID -> requested (already-clamped) fps
}

func newSamplerBurstHint() *samplerBurstHint {
	return &samplerBurstHint{cameras: make(map[string]*cameraBoostState)}
}

// SetManager wires (or re-wires, e.g. after a Manager restart) the real
// video pipeline manager. Called once agent.go actually constructs
// videoMgr -- anprRegistry itself is built earlier, mirroring the same
// deferred-wiring pattern anprCloudTransport already uses for CloudSink.
func (h *samplerBurstHint) SetManager(m *processing.Manager) {
	h.setController(managerFPSController{manager: m})
}

func (h *samplerBurstHint) setController(c videoFPSController) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.controller = c
}

func (h *samplerBurstHint) RequestBurstFPS(cameraKey, burstID string, fps float64) {
	fps = clampBurstFPS(fps)

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.controller == nil {
		return
	}

	st, ok := h.cameras[cameraKey]
	if !ok {
		baseline, ok := h.controller.CurrentTargetFPS(cameraKey)
		if !ok {
			return // camera not currently active -- nothing to boost
		}
		st = &cameraBoostState{baselineFPS: baseline, active: make(map[string]float64)}
		h.cameras[cameraKey] = st
	}
	st.active[burstID] = fps
	h.applyLocked(cameraKey, st)
}

func (h *samplerBurstHint) ReleaseBurstFPS(cameraKey, burstID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	st, ok := h.cameras[cameraKey]
	if !ok {
		return // never requested (or already released) -- harmless no-op
	}
	delete(st.active, burstID)

	if len(st.active) == 0 {
		if h.controller != nil {
			_ = h.controller.SetTargetFPS(cameraKey, st.baselineFPS)
		}
		delete(h.cameras, cameraKey)
		return
	}
	h.applyLocked(cameraKey, st)
}

// applyLocked sets the camera's TargetFPS to the max of its baseline and
// every currently active burst's requested rate. Must be called with h.mu
// held; h.controller must be non-nil.
func (h *samplerBurstHint) applyLocked(cameraKey string, st *cameraBoostState) {
	effective := st.baselineFPS
	for _, fps := range st.active {
		if fps > effective {
			effective = fps
		}
	}
	_ = h.controller.SetTargetFPS(cameraKey, effective)
}

// clampBurstFPS enforces the SAME technical ceiling remote-config itself
// validates ordinary TargetFPS requests against (item E: "respetar límites
// técnicos definidos por el runtime", never a value invented here).
func clampBurstFPS(fps float64) float64 {
	if fps > config.MaxVideoTargetFPS {
		return config.MaxVideoTargetFPS
	}
	if fps < 0 {
		return 0
	}
	return fps
}

// burstFPSFromRemoteConfig resolves the CURRENT per-camera desired
// HIGH_SPEED_LPR boost from the live RuntimeConfig snapshot (item E/F: the
// SaaS-provided projection of its own canonical plate_capture_burst
// execution profile -- never invented on the Edge). ok=false whenever ANPR
// itself is disabled, HighSpeedLPR is off, or no positive burst_fps was
// provided -- all of which correctly mean "no boost", not an error.
func burstFPSFromRemoteConfig(currentConfig func() remoteconfig.RuntimeConfig) func(cameraKey string) (float64, bool) {
	return func(cameraKey string) (float64, bool) {
		if currentConfig == nil {
			return 0, false
		}
		cfg := currentConfig()
		cam, ok := cfg.Cameras[cameraKey]
		if !ok || cam.ANPR == nil || !cam.ANPR.Enabled || !cam.ANPR.HighSpeedLPR {
			return 0, false
		}
		if cam.ANPR.BurstFPS <= 0 {
			return 0, false
		}
		return cam.ANPR.BurstFPS, true
	}
}
