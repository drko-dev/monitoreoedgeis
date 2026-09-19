// Package vision implements Milestone K's local inference path: a Go-owned
// Python "vision worker" subprocess (Ultralytics/PyTorch, K2), a model
// manager for the on-disk YOLO weights (K3), and a processing.Sink that
// drives CPU inference for edge mode (K4).
//
// The worker is never embedded in the Go binary (no PyTorch-in-Go) and is
// never reachable off this host: agent and worker talk newline-delimited
// JSON over a single Unix domain socket, loopback-local only.
package vision

import "time"

// Fixed COCO class IDs GEO CAM already uses SaaS-side (geocam/detector.py):
// person from the pose model, vehicle classes from the nano detector. K2
// keeps this identical rather than inventing new class semantics.
const (
	ClassIDPerson = 0
	ClassIDCar    = 2
	ClassIDMotor  = 3
	ClassIDBus    = 5
	ClassIDTruck  = 7
)

// DetectionTypePerson/DetectionTypeVehicle are the two categories the worker
// reports; anything else is dropped worker-side rather than forwarded as an
// unrecognized type.
const (
	DetectionTypePerson  = "person"
	DetectionTypeVehicle = "vehicle"
)

// wireRequest is one line of newline-delimited JSON sent to the worker.
type wireRequest struct {
	Type         string `json:"type"` // "health" | "infer" | "shutdown"
	CandidateKey string `json:"candidate_key,omitempty"`
	FrameSeq     uint64 `json:"frame_seq,omitempty"`
	TimestampMS  int64  `json:"timestamp_ms,omitempty"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	// JPEG is the frame, JPEG-encoded, base64-encoded by encoding/json's
	// []byte marshaling. Simplicity over a custom binary framing: frames are
	// already small (resized + JPEG) and this is a loopback socket.
	JPEG []byte `json:"jpeg,omitempty"`
}

// wireResponse is one line of newline-delimited JSON received from the
// worker, in reply to exactly one wireRequest.
type wireResponse struct {
	Type  string `json:"type"` // "health_ok" | "result" | "error"
	Ready bool   `json:"ready,omitempty"`
	// Device is the EFFECTIVE device the worker will drive Ultralytics with.
	// DeviceRequested is the raw configured value it was given. They are kept
	// separate because a worker that only echoed its input would otherwise be
	// indistinguishable from one that resolved it — a distinction Hito X's
	// device_requested/device_effective reporting depends on.
	Device          string      `json:"device,omitempty"`
	DeviceRequested string      `json:"device_requested,omitempty"`
	ModelsLoaded    []string    `json:"models_loaded,omitempty"`
	FrameSeq        uint64      `json:"frame_seq,omitempty"`
	InferenceMS     float64     `json:"inference_ms,omitempty"`
	Detections      []Detection `json:"detections,omitempty"`
	Error           string      `json:"error,omitempty"`
}

// Detection is one object found in a frame. Mirrors geocam/detector.py's
// SaaS-side detection shape (class_id/label/type/confidence/bbox) so a
// downstream consumer needs no Edge-specific parsing.
type Detection struct {
	ClassID    int        `json:"class_id"`
	Label      string     `json:"label"`
	Type       string     `json:"type"` // "person" | "vehicle"
	Confidence float64    `json:"confidence"`
	BBox       [4]float64 `json:"bbox"` // x1, y1, x2, y2 in output-frame pixels
}

// InferRequest is what a Sink hands the Worker for one frame.
type InferRequest struct {
	CandidateKey string
	FrameSeq     uint64
	Timestamp    time.Time
	Width        int
	Height       int
	JPEG         []byte
	// CorrelationID is the pipeline's own correlation id for this frame
	// (processing.Frame.CorrelationID) — carried through Go-side only,
	// never sent to the worker over the wire (Hito N: reuse, don't
	// re-derive, downstream of internal/agent).
	CorrelationID string
}

// InferenceResult is the local authority's verdict for one frame (K1: "Edge
// mode: YOLO local es la autoridad de inferencia").
type InferenceResult struct {
	CandidateKey  string
	FrameSeq      uint64
	Timestamp     time.Time
	InferenceMS   float64
	Device        string
	ModelsLoaded  []string
	Detections    []Detection
	CorrelationID string
}
