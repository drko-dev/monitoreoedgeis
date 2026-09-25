// Package anpr is Milestone J6-B's Edge-side ANPR/LPR candidate-extraction
// PREP: pure contracts, burst/context bookkeeping and bounded crop math for
// the hybrid vehicle -> plate-region -> plate-candidate pipeline. It is
// PREP-only: no OCR, no plate detector model, no final plate recognition.
//
// # Reused, not duplicated
//
// This package never opens an RTSP stream, decodes video or samples frames
// itself. It consumes:
//   - processing.Frame — the already-decoded/sampled/routed frame (see
//     internal/processing).
//   - processing.FrameHistory (Manager.FrameHistory) — the existing bounded
//     ring-buffer read surface, reused as-is for pre-event context (see
//     context.go). No second ring buffer is created.
//   - vision.Detection — when a vehicle detection already exists (K1 edge
//     inference), it is the source of VehicleCandidate. anpr does not run
//     its own vehicle detector.
//
// # Edge -> SaaS contract mapping
//
// This package's PlateCandidate is a Go-native, semantically-compatible
// counterpart to the SaaS's PlateCandidate (drko-dev/monitoreoia,
// feature/j6a-anpr-domain-prep, commit 93cba562d36bab9b8ba6f61ef6437ef0182b47db),
// not a struct copy:
//
//	SaaS field              Edge field                          Notes
//	candidate_id             PlateCandidate.CandidateID          Edge generates it (see candidate.go); SaaS may re-key on ingest.
//	organization_id          (not present)                       Resolved SaaS-side from the authenticated device, same pattern as FramesPath — Edge never asserts org identity.
//	camera_id                PlateCandidate.CameraKey             Edge's local camera_key (candidateKey), not the SaaS numeric camera_id.
//	track_id (opt)           PlateCandidate.TrackID (opt)         Both optional; Edge does not implement J5 tracking, so this stays empty until a future tracker sets it.
//	vehicle_detection_id     (not present)                        Edge has no persisted vehicle-detection row; VehicleBBox+FrameSeq are the correlating data instead.
//	frame_timestamp          PlateCandidate.Timestamp             Same semantics (frame's own decode time), not RTP or wall-clock at upload.
//	bbox                     PlateCandidate.PlateBBox (opt), VehicleBBox  Edge separates vehicle bbox (always present) from plate bbox (optional — no plate-region provider guarantee in PREP).
//	crop_reference (opt)     ANPRCandidateEnvelope.Payload/PayloadRef  Edge's transport envelope carries the crop payload or a reference, kept separate from candidate metadata (see transport.go).
//	processing_mode          PlateCandidate carries it via the originating VehicleCandidate.ProcessingMode, forwarded into ANPRCandidateEnvelope
//	source                   ANPRCandidateEnvelope.Source          Fixed "edge" for everything this package produces.
//	sequence_id/burst_id     PlateCandidate.BurstID                Edge's BurstID plays both roles; there is no separate sequence_id concept Edge-side.
package anpr
