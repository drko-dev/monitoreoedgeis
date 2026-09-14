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
)
