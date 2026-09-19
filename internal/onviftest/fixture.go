// Package onviftest provides a minimal, fixture-driven ONVIF device
// simulator (Hito W4): WS-Discovery Probe/ProbeMatch over UDP, plus the
// Device (GetDeviceInformation, GetCapabilities) and Media (GetProfiles,
// GetStreamUri) SOAP services over HTTP.
//
// It does not implement the full ONVIF specification and does not build a
// second discovery engine — it exists purely to drive the real production
// client (internal/discovery/onvif, internal/discovery/wsdiscovery) end to
// end in tests, the same way a real camera would.
package onviftest

// Profile is one ONVIF media profile served by GetProfiles/GetStreamUri.
type Profile struct {
	Token  string
	Name   string
	Codec  string
	Width  int
	Height int
	FPS    float64

	// StreamURI is returned verbatim by GetStreamUri. It may deliberately
	// include userinfo credentials (e.g. "rtsp://user:pass@host/stream")
	// to confirm production sanitizes it before use — see
	// TestSanitizesStreamURICredentials.
	StreamURI string
}

// Device is the fixture describing one simulated ONVIF camera.
type Device struct {
	Manufacturer    string
	Model           string
	FirmwareVersion string
	SerialNumber    string

	Profiles []Profile

	// RequireAuth, when true, makes the Device/Media SOAP services reject
	// requests that do not carry a valid WS-Security UsernameToken
	// (PasswordDigest) for Username/Password.
	RequireAuth bool
	Username    string
	Password    string

	// WS-Discovery fixture fields.
	EPRAddress string // endpoint reference, e.g. "urn:uuid:..."
	Types      string // e.g. "dn:NetworkVideoTransmitter"
	Scopes     string // space-separated scope URIs
}
