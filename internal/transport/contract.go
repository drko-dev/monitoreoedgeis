// Package transport is the GEO CAM Edge <-> SaaS HTTP client: gateway
// enrollment, credential rotation, and authenticated identity checks.
//
// # Contract
//
// Confirmed against the SaaS source (gateway_enroll router, authenticate_edge_device,
// and the self-service rotate-key router). Zero-knowledge model: the Edge
// generates its own credential locally and only ever sends the SaaS its
// SHA-256 hash — never the plaintext credential — in EnrollRequest.DeviceKeyHash
// and RotateKeyRequest.DeviceKeyHash. See internal/credentials.GenerateCredential
// and internal/credentials.HashCredential.
package transport

// Endpoint paths.
const (
	// EnrollPath claims a one-time enrollment token. The Edge sends only a
	// device_key_hash (never the plaintext credential) and receives back
	// only device_id/device_kind — no org/site metadata (fetch those via
	// GET MePath once authenticated with the locally-generated credential).
	EnrollPath = "/api/v1/gateway/enroll"
	// MePath returns the authenticated edge's own metadata (no secrets).
	MePath = "/api/v1/edge/me"
	// RotateKeyPath is the self-service credential rotation endpoint. The
	// Edge generates the new credential locally, sends only its hash plus a
	// client-generated rotation_id (idempotency key), and only persists the
	// new credential locally after the SaaS ACKs the rotation.
	RotateKeyPath = "/api/v1/edge/me/rotate-key"
	// HeartbeatPath is the periodic liveness + telemetry endpoint. It is the
	// pre-existing SaaS heartbeat endpoint (shared with the legacy Python
	// gateway agent), extended backward-compatibly with the Go agent's
	// fields — not a parallel v2 endpoint. The SaaS stamps last_seen from its
	// own clock on arrival; nothing in the body claims connectivity.
	HeartbeatPath = "/api/v1/edge/heartbeat"
	// DiscoveryNextPath is polled by gateway devices to claim pending discovery runs.
	// 204 No Content means no run is pending; 200 OK returns {"run_id": <int>}.
	DiscoveryNextPath = "/api/v1/gateway/discovery/next"
	// DiscoveryReportPath is used to report completion or failure of a claimed run.
	DiscoveryReportPath = "/api/v1/gateway/discovery/runs/%d/report"
	// FramesPath ingests one sampled JPEG video frame per request (Milestone
	// I), forwarded by the SaaS to its Cloud Vision Worker over its own
	// internal loopback IPC. The body is the raw JPEG (Content-Type:
	// image/jpeg), not JSON — frame metadata travels as headers instead (see
	// Client.PostFrame). The SaaS resolves organization_id from the
	// authenticated device and camera_id from X-Candidate-Key server-side;
	// neither is ever sent directly by the Edge.
	FramesPath = "/api/v1/edge/frames"
	// AnprCandidatesPath accepts Hito J6 ANPR/LPR candidates as
	// multipart/form-data (a "metadata" JSON part -- ANPRCandidateEnvelope
	// v1 -- plus a "crop" image/jpeg part), never base64-in-JSON. The SaaS
	// resolves organization_id/camera_id server-side exactly as
	// FramesPath does; the Edge only ever sends camera_key.
	AnprCandidatesPath = "/api/v1/edge/anpr/candidates"
	// LocalEventsPath accepts locally produced event metadata. The SaaS derives
	// organization and camera ownership from the authenticated edge device.
	LocalEventsPath   = "/api/v1/edge/local-events"
	ControlNextPath   = "/api/v1/edge/control/next"
	ControlReportPath = "/api/v1/edge/control/%s/report"
	// RemoteConfigNextPath returns the current desired remote-config
	// document (Hito O). 204 No Content means no config has been assigned
	// yet; 200 OK returns {"config": {...}}. Polled the same way as
	// ControlNextPath -- outbound-only, no push, no inbound port.
	RemoteConfigNextPath = "/api/v1/edge/remote-config/next"
	// RemoteConfigAckPath reports the outcome of applying one version.
	RemoteConfigAckPath = "/api/v1/edge/remote-config/ack"
	// OTANextPath returns the current eligible OTA release descriptor
	// (Hito T). 204 No Content means no update is currently eligible; 200
	// OK returns a release with release id, version, architecture, and
	// GitHub Releases URLs for the artifact, SHA256SUMS and its detached
	// signature. Polled the same way as RemoteConfigNextPath -- driven by
	// the existing heartbeat cadence, not a second poll loop.
	OTANextPath = "/api/v1/edge/ota/next"
)
