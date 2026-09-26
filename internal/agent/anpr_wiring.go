package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/anpr"
	"github.com/drko-dev/monitoreoedgeis/internal/cloudsink"
	"github.com/drko-dev/monitoreoedgeis/internal/remoteconfig"
)

// defaultAnprConfig is the production Hito J6 tuning. Bounds chosen
// conservatively (a handful of concurrent bursts/cameras, short TTLs) --
// real per-deployment tuning is a documented future improvement, not
// something this integration invents numbers for beyond a safe default.
func defaultAnprConfig() anpr.Config {
	return anpr.Config{
		Enabled:                  true, // gated per-camera by the Authorizer below, never globally bypassed
		MaxActiveBurstsPerCamera: 4,
		MaxFramesPerBurst:        5,
		MaxCandidateBytes:        2 * 1024 * 1024,
		MaxContextFrames:         5,
		MaxCameras:               64,
		BurstTTL:                 3 * time.Second,
		CropPolicy:               anpr.CropVehicleContext,
		FrameSelection:           anpr.SelectTopNQuality,
		MaxEncodedCropBytes:      2 * 1024 * 1024,
		DedupeCacheSize:          512,
	}
}

// remoteConfigAnprAuthorizer implements anpr.Authorizer by reading the
// per-camera ANPR desired state SaaS already resolved, out of the live
// RuntimeConfig snapshot (item 37/38). Fail-closed: no live config yet, no
// entry for this camera, or Enabled=false all deny -- exactly
// anpr.DenyAllAuthorizer's own default, just backed by a real source
// instead of a hardcoded stub.
type remoteConfigAnprAuthorizer struct {
	current func() remoteconfig.RuntimeConfig
}

func (a *remoteConfigAnprAuthorizer) ANPRAllowed(cameraKey string) bool {
	if a.current == nil {
		return false
	}
	cfg := a.current()
	cam, ok := cfg.Cameras[cameraKey]
	if !ok || cam.ANPR == nil {
		return false
	}
	return cam.ANPR.Enabled
}

// newAnprRegistry builds the process-wide anpr.Registry. currentConfig is
// deferred (called lazily on every ANPRAllowed check, never memoized here)
// so it safely observes a.runtimeApplier even though that field is only
// assigned later in Agent construction -- Submit() is never called before
// the agent finishes bootstrapping and starts receiving real frames.
func newAnprRegistry(currentConfig func() remoteconfig.RuntimeConfig, samplingHint *samplerBurstHint) *anpr.Registry {
	return anpr.NewRegistry(
		defaultAnprConfig(),
		anpr.WithAuthorizer(&remoteConfigAnprAuthorizer{current: currentConfig}),
		anpr.WithSamplingHint(samplingHint, burstFPSFromRemoteConfig(currentConfig)),
	)
}

// anprEnvelopeSchemaVersion must match the SaaS's AnprCandidateMetadataV1
// schema_version exactly (rule #17: one frozen wire contract, never
// independently evolved on each side).
const anprEnvelopeSchemaVersion = "anpr_candidate_v1"

// anprCandidateEnvelope mirrors the SaaS's AnprCandidateMetadataV1 pydantic
// model field-for-field. Built here (not in internal/cloudsink, which
// stays decoupled from anpr's Go types) so the wire contract lives in
// exactly one place on the Edge side.
type anprCandidateEnvelope struct {
	SchemaVersion  string          `json:"schema_version"`
	CandidateID    string          `json:"candidate_id"`
	CameraKey      string          `json:"camera_key"`
	FrameSeq       uint64          `json:"frame_seq"`
	Timestamp      string          `json:"timestamp"`    // RFC3339, UTC
	VehicleBBox    [4]float64      `json:"vehicle_bbox"` // x, y, width, height (pixel space)
	PlateBBox      *[4]float64     `json:"plate_bbox,omitempty"`
	TrackID        *string         `json:"track_id,omitempty"`
	BurstID        string          `json:"burst_id"`
	CorrelationID  *string         `json:"correlation_id,omitempty"`
	ProcessingMode string          `json:"processing_mode"`
	QualityHints   json.RawMessage `json:"quality_hints,omitempty"`
	ModelMetadata  json.RawMessage `json:"model_metadata,omitempty"`
	CropSHA256     string          `json:"crop_sha256"`
	CropSizeBytes  int             `json:"crop_size_bytes"`
}

// buildAnprCandidateEnvelope encodes candidate + cropJPEG's own hash/size
// into the wire contract's JSON body. The hash/size are always computed
// from the EXACT bytes about to be sent, never trusted from elsewhere,
// so the SaaS's own hash/size verification (item #25) can never
// legitimately fail against a byte-for-byte-correct upload.
func buildAnprCandidateEnvelope(candidate anpr.PlateCandidate, cropJPEG []byte) ([]byte, error) {
	sum := sha256.Sum256(cropJPEG)
	env := anprCandidateEnvelope{
		SchemaVersion: anprEnvelopeSchemaVersion,
		CandidateID:   candidate.CandidateID,
		CameraKey:     candidate.CameraKey,
		FrameSeq:      candidate.FrameSeq,
		Timestamp:     candidate.Timestamp.UTC().Format(time.RFC3339Nano),
		VehicleBBox: [4]float64{
			candidate.VehicleBBox.X0, candidate.VehicleBBox.Y0,
			candidate.VehicleBBox.X1 - candidate.VehicleBBox.X0,
			candidate.VehicleBBox.Y1 - candidate.VehicleBBox.Y0,
		},
		BurstID: candidate.BurstID,
		// anpr.PlateCandidate itself doesn't carry ProcessingMode forward
		// (only VehicleCandidate does, pre-burst) -- this transport is only
		// ever reached from the local-detection consumer, which only ever
		// runs in hybrid mode (see consumeAnprCandidates).
		ProcessingMode: "hybrid",
		CropSHA256:     hex.EncodeToString(sum[:]),
		CropSizeBytes:  len(cropJPEG),
	}
	if candidate.PlateBBox != nil {
		bbox := [4]float64{
			candidate.PlateBBox.X0, candidate.PlateBBox.Y0,
			candidate.PlateBBox.X1 - candidate.PlateBBox.X0,
			candidate.PlateBBox.Y1 - candidate.PlateBBox.Y0,
		}
		env.PlateBBox = &bbox
	}
	if candidate.TrackID != "" {
		env.TrackID = &candidate.TrackID
	}
	if candidate.CorrelationID != "" {
		env.CorrelationID = &candidate.CorrelationID
	}
	return json.Marshal(env)
}

// anprCloudTransport bridges fullEdgeEventConsumer's injectable transport
// callback to the CURRENT CloudSink instance. CloudSink is rebuilt on every
// processing-mode transition (remoteconfig.WithCloudSinkFactory) -- an
// atomic pointer, updated wherever a new CloudSink is built, means the
// callback always targets the live instance without the two ever needing a
// direct reference to each other.
type anprCloudTransport struct {
	sink   atomic.Pointer[cloudsink.CloudSink]
	logger *slog.Logger
}

// SetSink is called every time a new CloudSink is built (initial bootstrap
// and every later mode transition). A nil sink (Edge-only mode, or no
// SaaS configured) makes Send a documented no-op -- fail closed, never a
// silent fallback transport.
func (t *anprCloudTransport) SetSink(sink *cloudsink.CloudSink) {
	t.sink.Store(sink)
}

// Send implements the func(anpr.PlateCandidate, []byte) signature
// fullEdgeEventConsumer.SetAnprTransport expects.
func (t *anprCloudTransport) Send(candidate anpr.PlateCandidate, cropJPEG []byte) {
	sink := t.sink.Load()
	if sink == nil {
		return // no CloudSink currently configured -- nothing to send to
	}
	metadataJSON, err := buildAnprCandidateEnvelope(candidate, cropJPEG)
	if err != nil {
		if t.logger != nil {
			t.logger.Warn("anpr: failed to build candidate envelope",
				slog.String("candidate_id", candidate.CandidateID), slog.Any("error", err))
		}
		return
	}
	if err := sink.EnqueueANPRCandidate(candidate.CameraKey, candidate.Timestamp, metadataJSON, cropJPEG); err != nil {
		if t.logger != nil {
			t.logger.Warn("anpr: failed to enqueue candidate for transport",
				slog.String("candidate_id", candidate.CandidateID), slog.Any("error", err))
		}
	}
}
