// Package discovery implements ONVIF WS-Discovery and local network device inventory
// for GEO CAM Edge (Milestone E).
package discovery

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// DeviceType classifies the physical or logical network video hardware.
type DeviceType string

const (
	DeviceTypeCamera  DeviceType = "camera"
	DeviceTypeNVR     DeviceType = "nvr"
	DeviceTypeDVR     DeviceType = "dvr"
	DeviceTypeEncoder DeviceType = "encoder"
	DeviceTypeUnknown DeviceType = "unknown"
)

// StreamRole classifies the intended video stream consumption profile.
type StreamRole string

const (
	StreamRoleMainStream StreamRole = "main"
	StreamRoleSubStream  StreamRole = "sub"
	StreamRoleUnknown    StreamRole = "unknown"
)

// ChannelAvailability records whether a recorder explicitly reports a
// channel as enabled. Unknown is deliberately distinct from enabled: ONVIF
// GetVideoSources establishes that a source exists, but does not universally
// define the recorder's enabled/disabled state.
type ChannelAvailability string

const (
	ChannelAvailabilityUnknown  ChannelAvailability = "unknown"
	ChannelAvailabilityEnabled  ChannelAvailability = "enabled"
	ChannelAvailabilityDisabled ChannelAvailability = "disabled"
)

// CredentialResolver resolves the camera credential to use for an
// authenticated ONVIF retry, keyed by a device's StableIdentity — never an
// IP address. ok=false means no credential is available for that candidate;
// the caller must keep treating the device as AuthRequired and move on to
// the next one rather than guessing a default.
//
// This is a narrow function type, not an import of internal/cameracreds:
// discovery stays usable without pulling in the credential cache/store, and
// the real adapter (over cameracreds.Provider.Resolve) is built by
// internal/agent, which already depends on both packages.
type CredentialResolver func(candidateKey string) (username, password string, ok bool)

// MediaProfile represents an ONVIF Media Profile exposed by a video source.
type MediaProfile struct {
	Token            string              `json:"token"`
	Name             string              `json:"name,omitempty"`
	Codec            string              `json:"codec,omitempty"` // H264, H265, JPEG, etc.
	Width            int                 `json:"width,omitempty"`
	Height           int                 `json:"height,omitempty"`
	FPS              float64             `json:"fps,omitempty"`
	StreamURI        string              `json:"stream_uri,omitempty"` // Sanitized RTSP URI (never contains userinfo/passwords)
	StreamURIOrigin  string              `json:"stream_uri_origin,omitempty"`
	Availability     ChannelAvailability `json:"availability,omitempty"`
	Role             StreamRole          `json:"role,omitempty"`
	VideoSourceToken string              `json:"video_source_token,omitempty"`
	ChannelNumber    int                 `json:"channel_number,omitempty"`
	StreamID         int                 `json:"stream_id,omitempty"`
}

// VideoSource represents a physical or logical sensor / channel on a device.
// Multichannel devices (DVRs, NVRs, dual-sensor cameras) expose multiple video sources.
type VideoSource struct {
	SourceToken   string              `json:"source_token"`
	ChannelNumber int                 `json:"channel_number,omitempty"`
	Label         string              `json:"label,omitempty"`
	Availability  ChannelAvailability `json:"availability,omitempty"`
	Profiles      []MediaProfile      `json:"profiles,omitempty"`
	Capabilities  []string            `json:"capabilities,omitempty"`
}

// DiscoveredDevice represents a distinct network video device found on the LAN.
// CRITICAL ARCHITECTURAL RULE: 1 IP != 1 camera. A DiscoveredDevice may host
// multiple VideoSources (e.g. an 8-channel NVR has 8 VideoSources).
type DiscoveredDevice struct {
	// StableIdentity is the primary local deduplication key:
	// Priority: 1. Normalized EPR UUID, 2. Hardware serial if known, 3. Normalized endpoint host:port:path.
	StableIdentity string `json:"stable_identity"`

	// Network identifiers
	EPRAddress string   `json:"epr_address,omitempty"`
	IP         string   `json:"ip"`
	Port       int      `json:"port"`
	Path       string   `json:"path"`
	XAddr      string   `json:"xaddr"` // Primary service URL validated as private IPv4 HTTP/HTTPS
	AllXAddrs  []string `json:"all_xaddrs,omitempty"`

	// ONVIF announced metadata
	Types  string   `json:"types,omitempty"`
	Scopes []string `json:"scopes,omitempty"`

	// Device info (enriched if publicly available or from scopes)
	Manufacturer string     `json:"manufacturer,omitempty"`
	Model        string     `json:"model,omitempty"`
	Serial       string     `json:"serial,omitempty"`
	Firmware     string     `json:"firmware,omitempty"`
	DeviceType   DeviceType `json:"device_type"`

	// Security / Auth boundary (Milestone E vs F)
	// If the device rejected unauthenticated SOAP inspection, AuthRequired is true.
	AuthRequired bool `json:"auth_required"`

	// ManualRecorder marks an operator-seeded recorder endpoint. It is local-only
	// routing metadata: the SaaS contract derives identity from the reported
	// endpoint fields and must never depend on this implementation detail.
	ManualRecorder bool `json:"-"`

	// Multichannel / video sources
	VideoSources []VideoSource `json:"video_sources,omitempty"`

	// Timestamps
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// ChannelCount returns the number of video channels discovered on this device.
func (d *DiscoveredDevice) ChannelCount() int {
	if len(d.VideoSources) == 0 {
		return 1
	}
	return len(d.VideoSources)
}

// channelCandidateKeySeparator joins a device's StableIdentity with one of
// its VideoSource.SourceToken values. It is intentionally not a character
// StableIdentity or SourceToken can themselves start with in practice
// (both are opaque vendor/protocol identifiers, never containing "|ch=" in
// any known ONVIF implementation), so the composite stays unambiguous.
const channelCandidateKeySeparator = "|ch="

// ChannelCandidateKey builds the stable, opaque candidate key for one
// channel of a multi-source (DVR/NVR) device: the device's own
// StableIdentity plus that channel's SourceToken. It is never used for a
// single-source device, whose CandidateKey stays exactly StableIdentity
// (see UX6_DVR_NVR_MULTICHANNEL.md) -- every layer that already treats
// CandidateKey as an opaque string (installer, SaaS onboarding,
// cameracreds sync, rtsp.CameraTarget) needs no further change to support
// this, as long as discovery, the installer, and the reconciler all build
// exactly this same string for the same (device, channel) pair.
func ChannelCandidateKey(deviceStableIdentity, sourceToken string) string {
	return deviceStableIdentity + channelCandidateKeySeparator + sourceToken
}

// ParseChannelSourceToken extracts a channel candidate key's SourceToken
// given the device's own StableIdentity, the inverse of ChannelCandidateKey.
// Returns ("", false) for a single-source candidateKey (one that is exactly
// deviceStableIdentity) or anything that isn't
// deviceStableIdentity+channelCandidateKeySeparator+<token>.
func ParseChannelSourceToken(candidateKey, deviceStableIdentity string) (string, bool) {
	prefix := deviceStableIdentity + channelCandidateKeySeparator
	if !strings.HasPrefix(candidateKey, prefix) {
		return "", false
	}
	token := candidateKey[len(prefix):]
	if token == "" {
		return "", false
	}
	return token, true
}

// Inventory is a thread-safe local cache of discovered devices.
type Inventory struct {
	mu      sync.RWMutex
	devices map[string]*DiscoveredDevice
}

// NewInventory creates an empty local inventory.
func NewInventory() *Inventory {
	return &Inventory{
		devices: make(map[string]*DiscoveredDevice),
	}
}

// Upsert adds or updates a discovered device in the local inventory.
// If the device already exists, its mutable fields and LastSeen are refreshed
// while FirstSeen is preserved.
func (inv *Inventory) Upsert(dev DiscoveredDevice) *DiscoveredDevice {
	inv.mu.Lock()
	defer inv.mu.Unlock()

	key := strings.TrimSpace(dev.StableIdentity)
	if key == "" {
		key = fmt.Sprintf("%s:%d%s", dev.IP, dev.Port, dev.Path)
	}

	now := time.Now().UTC()
	inv.evictExpiredLocked(now)

	existing, found := inv.devices[key]
	if !found {
		if len(inv.devices) >= MaxInventoryDevices {
			// Fail-closed: reject the new device rather than evicting an
			// arbitrary existing one under active-attack conditions. Report it
			// for this scan without persisting it into the inventory.
			rejected := dev
			return &rejected
		}
		dev.FirstSeen = now
		dev.LastSeen = now
		stored := dev
		inv.devices[key] = &stored
		return &stored
	}

	// Update mutable attributes
	existing.LastSeen = now
	if dev.EPRAddress != "" {
		existing.EPRAddress = dev.EPRAddress
	}
	if dev.IP != "" {
		existing.IP = dev.IP
	}
	if dev.Port != 0 {
		existing.Port = dev.Port
	}
	if dev.Path != "" {
		existing.Path = dev.Path
	}
	if dev.XAddr != "" {
		existing.XAddr = dev.XAddr
	}
	if len(dev.AllXAddrs) > 0 {
		existing.AllXAddrs = dev.AllXAddrs
	}
	if dev.Types != "" {
		existing.Types = dev.Types
	}
	if len(dev.Scopes) > 0 {
		existing.Scopes = dev.Scopes
	}
	if dev.Manufacturer != "" {
		existing.Manufacturer = dev.Manufacturer
	}
	if dev.Model != "" {
		existing.Model = dev.Model
	}
	if dev.Serial != "" {
		existing.Serial = dev.Serial
	}
	if dev.Firmware != "" {
		existing.Firmware = dev.Firmware
	}
	if dev.DeviceType != "" && dev.DeviceType != DeviceTypeUnknown {
		existing.DeviceType = dev.DeviceType
	}
	existing.AuthRequired = dev.AuthRequired
	if len(dev.VideoSources) > 0 {
		existing.VideoSources = dev.VideoSources
	}

	return existing
}

// evictExpiredLocked removes devices not seen within DeviceTTL. Caller must hold inv.mu.
func (inv *Inventory) evictExpiredLocked(now time.Time) {
	for key, d := range inv.devices {
		if now.Sub(d.LastSeen) > DeviceTTL {
			delete(inv.devices, key)
		}
	}
}

// PruneExpired removes every device whose LastSeen is older than DeviceTTL and
// returns how many were removed.
//
// It exists because expiry previously happened only as a side effect of Upsert
// (evictExpiredLocked above), so List/Count/Get kept reporting devices that had
// stopped being seen — up to a full DeviceTTL after the last successful scan.
// Any consumer that reconciles inventory into other subsystems must prune
// explicitly before reading, otherwise it would keep acting on stale devices.
//
// A single missed multicast response is NOT an expiry: a device is removed only
// once now-LastSeen exceeds DeviceTTL, which is deliberately not configurable
// here. Callers pass `now` so the behaviour is deterministic under test.
func (inv *Inventory) PruneExpired(now time.Time) int {
	inv.mu.Lock()
	defer inv.mu.Unlock()

	removed := 0
	for key, d := range inv.devices {
		if now.Sub(d.LastSeen) > DeviceTTL {
			delete(inv.devices, key)
			removed++
		}
	}
	return removed
}

// List returns a snapshot of all discovered devices in the inventory.
func (inv *Inventory) List() []DiscoveredDevice {
	inv.mu.RLock()
	defer inv.mu.RUnlock()

	result := make([]DiscoveredDevice, 0, len(inv.devices))
	for _, d := range inv.devices {
		result = append(result, *d)
	}
	return result
}

// Count returns the number of devices currently in the inventory.
func (inv *Inventory) Count() int {
	inv.mu.RLock()
	defer inv.mu.RUnlock()
	return len(inv.devices)
}

// Get returns a discovered device by its stable identity key, or nil if not found.
func (inv *Inventory) Get(key string) *DiscoveredDevice {
	inv.mu.RLock()
	defer inv.mu.RUnlock()
	if d, ok := inv.devices[key]; ok {
		copyDev := *d
		return &copyDev
	}
	return nil
}

// SetLastSeenForTests backdates an already-inventoried device's LastSeen,
// for deterministic TTL-expiry tests in other packages (e.g.
// internal/agent's reconciler suite) that cannot wait out the real
// DeviceTTL and have no access to this package's unexported fields. It is a
// no-op if key is not present. Production code has no reason to call this.
func (inv *Inventory) SetLastSeenForTests(key string, when time.Time) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	if d, ok := inv.devices[key]; ok {
		d.LastSeen = when
	}
}
