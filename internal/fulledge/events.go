package fulledge

import (
	"fmt"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/identity"
)

// SyncStatus represents the sync lifecycle of an event towards SaaS.
type SyncStatus string

const (
	SyncStatusPending SyncStatus = "pending"
	SyncStatusSynced  SyncStatus = "synced"
	SyncStatusFailed  SyncStatus = "failed"
)

// LocalEvent is the canonical event record created when local YOLO produces a valid detection.
type LocalEvent struct {
	EventUUID               string           `json:"event_uuid"`
	EdgeID                  string           `json:"edge_id"`
	CandidateKey            string           `json:"candidate_key"`
	TenantID                string           `json:"tenant_id,omitempty"`
	SiteID                  string           `json:"site_id,omitempty"`
	FrameSeq                uint64           `json:"frame_seq"`
	SourceTimestamp         time.Time        `json:"source_timestamp"`
	LocalInferenceTimestamp time.Time        `json:"local_inference_timestamp"`
	Tipo                    string           `json:"tipo"`
	ClassID                 int              `json:"class_id"`
	Confidence              float64          `json:"confidence"`
	BBox                    BoundingBox      `json:"bbox"`
	Model                   string           `json:"model"`
	Device                  string           `json:"device"`
	ProcessingMode          string           `json:"processing_mode"` // always "edge"
	SyncStatus              SyncStatus       `json:"sync_status"`     // "pending" | "synced" | "failed"
	QuarantineReason        string           `json:"quarantine_reason,omitempty"`
	CreatedAt               time.Time        `json:"created_at"`
	Detections              []LocalDetection `json:"detections,omitempty"`
	Evidence                *EvidenceRef     `json:"evidence,omitempty"`
}

// NewLocalEvent constructs a validated LocalEvent from an InferenceResult and primary detection.
func NewLocalEvent(edgeID, tenantID, siteID, model string, res InferenceResult, primary LocalDetection, evRef *EvidenceRef) (*LocalEvent, error) {
	if primary.Label == "" && primary.Tipo == "" {
		return nil, fmt.Errorf("fulledge: detection requires label or tipo")
	}

	uuid, err := identity.NewUUIDv4()
	if err != nil {
		return nil, fmt.Errorf("fulledge: generate event uuid: %w", err)
	}

	// M4: prefer the model's specific label (car/motorcycle/bus/truck) over
	// the generic Tipo (person/vehicle) when the worker already provides one
	// (deploy/vision-worker/backend.py:VEHICLE_CLASS_IDS) — never invented,
	// just not discarded.
	tipo := primary.Label
	if tipo == "" {
		tipo = primary.Tipo
	}

	return &LocalEvent{
		EventUUID:               uuid,
		EdgeID:                  edgeID,
		CandidateKey:            res.CandidateKey,
		TenantID:                tenantID,
		SiteID:                  siteID,
		FrameSeq:                res.FrameSeq,
		SourceTimestamp:         res.FrameTimestamp,
		LocalInferenceTimestamp: res.InferenceTimestamp,
		Tipo:                    tipo,
		ClassID:                 primary.ClassID,
		Confidence:              primary.Confidence,
		BBox:                    primary.BBox,
		Model:                   model,
		Device:                  res.Device,
		ProcessingMode:          "edge",
		SyncStatus:              SyncStatusPending,
		CreatedAt:               time.Now().UTC(),
		Detections:              res.Detections,
		Evidence:                evRef,
	}, nil
}
