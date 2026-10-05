package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/discovery/onvif"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery/wsdiscovery"
)

// Engine orchestrates WS-Discovery, local deduplication, unauthenticated SOAP enrichment,
// and local inventory management.
type Engine struct {
	scanner     *wsdiscovery.Scanner
	onvifClient *onvif.Client
	inventory   *Inventory
	log         *slog.Logger
	interfaces  []string      // Explicit interfaces or empty for auto-private
	scanTimeout time.Duration // Scan duration per interface

	// credentialResolver is nil until SetCredentialResolver is called (see
	// internal/agent's discovery module wiring, Hito Z G1-B). A nil resolver
	// preserves the exact previous behavior: a device that rejects
	// unauthenticated SOAP inspection is left with AuthRequired=true and no
	// profiles/StreamURI, and the scan continues normally.
	credentialResolver CredentialResolver
	recorderAdapter    RecorderAdapter
	manualRecorders    []string
}

// RecorderAdapter is an optional vendor-specific read-only enrichment seam.
// Implementations return channels, never mutate inventory or credentials.
type RecorderAdapter interface {
	Vendor() string
	Discover(ctx context.Context, endpoint, username, password string) ([]VideoSource, error)
}

// NewEngine creates a new discovery engine.
func NewEngine(
	scanner *wsdiscovery.Scanner,
	onvifClient *onvif.Client,
	inventory *Inventory,
	interfaces []string,
	scanTimeout time.Duration,
	log *slog.Logger,
) *Engine {
	if scanner == nil {
		scanner = wsdiscovery.NewScanner(nil)
	}
	if onvifClient == nil {
		onvifClient = onvif.NewClient(2*time.Second, nil)
	}
	// Inject the shared fail-closed XAddr validator into every outbound SOAP
	// call the client makes, regardless of where the destination XAddr came
	// from (initial discovery, GetCapabilities' Media XAddr, etc). This closes
	// the second-hop SSRF path since PostSOAP now refuses to run without it.
	onvifClient.SetXAddrValidator(ValidateXAddr)
	if inventory == nil {
		inventory = NewInventory()
	}
	if scanTimeout <= 0 {
		scanTimeout = DefaultScanTimeout
	}
	if log == nil {
		log = slog.Default()
	}

	return &Engine{
		scanner:     scanner,
		onvifClient: onvifClient,
		inventory:   inventory,
		log:         log,
		interfaces:  interfaces,
		scanTimeout: scanTimeout,
	}
}

// Inventory returns the active local inventory.
func (e *Engine) Inventory() *Inventory {
	return e.inventory
}

// SetCredentialResolver wires an authenticated-ONVIF-retry credential
// resolver into the engine (Hito Z G1-B). It follows the same
// post-construction-setter shape as SetXAddrValidator on the onvif.Client,
// for the same reason: the resolver (backed by cameracreds.Provider) is
// only available once internal/agent has built the camera-credentials
// subsystem, which happens after the engine itself is constructed. Passing
// nil restores the previous anonymous-only behavior.
func (e *Engine) SetCredentialResolver(resolve CredentialResolver) {
	e.credentialResolver = resolve
}

// SetRecorderAdapter wires the one optional vendor-specific recorder adapter.
func (e *Engine) SetRecorderAdapter(adapter RecorderAdapter) { e.recorderAdapter = adapter }

// SetManualHikvisionEndpoints adds operator-configured private recorder base
// URLs. These URLs contain no credentials; the normal camera credential
// resolver remains the only source of authentication material.
func (e *Engine) SetManualHikvisionEndpoints(endpoints []string) {
	e.manualRecorders = append([]string(nil), endpoints...)
}

// ScanResult holds the outcome of a single discovery scan cycle.
type ScanResult struct {
	DevicesFound []DiscoveredDevice
	Duration     time.Duration
}

// RunScan executes a discovery scan cycle across the configured network interfaces.
func (e *Engine) RunScan(ctx context.Context) (*ScanResult, error) {
	start := time.Now()
	scopes, err := SelectInterfaces(e.interfaces)
	if err != nil {
		return nil, fmt.Errorf("discovery engine: interface selection: %w", err)
	}

	if len(scopes) == 0 && len(e.manualRecorders) == 0 {
		e.log.Info("discovery: no active private network interfaces found for scanning")
		return &ScanResult{Duration: time.Since(start)}, nil
	}

	e.log.Info("discovery: starting probe scan",
		slog.Int("interface_count", len(scopes)),
		slog.Duration("timeout", e.scanTimeout),
	)

	// Step 1: Collect raw ProbeMatches across all interfaces
	var rawCandidates []wsdiscovery.DiscoveredRawCandidate
	for _, scope := range scopes {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		wsScope := wsdiscovery.NetworkScope{
			Interface: scope.Interface,
			IPv4:      scope.IPv4,
		}
		cands, err := e.scanner.ScanInterface(ctx, wsScope, e.scanTimeout)
		if err != nil {
			e.log.Warn("discovery: scan failed on interface",
				slog.String("interface", scope.Interface.Name),
				slog.String("ip", scope.IPv4.String()),
				slog.String("error", err.Error()),
			)
			continue
		}
		rawCandidates = append(rawCandidates, cands...)
	}

	// Step 2: Validate XAddrs and deduplicate across interfaces
	// Priority hierarchy:
	//   1. EPR UUID (normalized)
	//   2. Fallback: Host:Port/Path
	deduped := deduplicateRawCandidates(rawCandidates)
	for _, endpoint := range e.manualRecorders {
		u, port, err := ValidateXAddr(endpoint)
		if err != nil {
			e.log.Warn("discovery: rejected manual Hikvision endpoint", slog.Any("error", err))
			continue
		}
		if e.recorderAdapter == nil {
			e.log.Warn("discovery: manual Hikvision endpoint ignored without a recorder adapter")
			continue
		}
		// Manual seeds use the same canonical endpoint identity as the SaaS
		// credential projection. Manual-vs-discovered behavior is tracked
		// separately so identity never depends on discovery source.
		stable := fmt.Sprintf("endpoint:%s:%d%s", strings.ToLower(u.Hostname()), port, u.Path)
		deduped = append(deduped, DiscoveredDevice{
			StableIdentity: stable,
			ManualRecorder: true,
			IP:             u.Hostname(),
			Port:           port,
			Path:           u.Path,
			XAddr:          u.String(),
			AllXAddrs:      []string{u.String()},
			Manufacturer:   e.recorderAdapter.Vendor(),
			DeviceType:     DeviceTypeUnknown,
		})
	}
	deduped = deduplicateDevices(deduped)

	e.log.Info("discovery: raw scan completed",
		slog.Int("raw_matches", len(rawCandidates)),
		slog.Int("unique_devices", len(deduped)),
	)

	// Step 3: Concurrently enrich metadata using a bounded worker pool (max 4 workers)
	enriched := e.enrichCandidates(ctx, deduped)

	// Step 4: Update local inventory
	var recorded []DiscoveredDevice
	for _, dev := range enriched {
		stored := e.inventory.Upsert(dev)
		recorded = append(recorded, *stored)
	}

	// Step 5: expire devices that have not been seen within DeviceTTL.
	//
	// This runs on every SUCCESSFUL scan, including one that found nothing, so
	// a camera that disappears from the LAN is eventually removed even if no
	// further Upsert happens (expiry used to be a side effect of Upsert only).
	// It is TTL-based, so a single missed multicast response never removes a
	// device — only sustained absence does.
	if pruned := e.inventory.PruneExpired(time.Now().UTC()); pruned > 0 {
		e.log.Info("discovery: pruned expired devices from inventory", slog.Int("pruned", pruned))
	}

	return &ScanResult{
		DevicesFound: recorded,
		Duration:     time.Since(start),
	}, nil
}

// deduplicateRawCandidates groups raw candidates by stable identity after validating XAddrs.
func deduplicateRawCandidates(raw []wsdiscovery.DiscoveredRawCandidate) []DiscoveredDevice {
	byStableKey := make(map[string]*DiscoveredDevice)

	for _, r := range raw {
		for _, rawXAddr := range r.XAddrs {
			u, port, err := ValidateXAddr(rawXAddr)
			if err != nil {
				continue
			}

			// Calculate stable identity
			epr := strings.ToLower(strings.TrimSpace(r.EPRAddress))
			var key string
			if epr != "" {
				key = "epr:" + epr
			} else {
				key = fmt.Sprintf("endpoint:%s:%d%s", strings.ToLower(u.Hostname()), port, u.Path)
			}

			existing, found := byStableKey[key]
			if !found {
				_, cleanTokens := CleanScopes(r.Scopes)
				devType := classifyDeviceType(r.Scopes, r.Types, 1)

				dev := &DiscoveredDevice{
					StableIdentity: key,
					EPRAddress:     r.EPRAddress,
					IP:             u.Hostname(),
					Port:           port,
					Path:           u.Path,
					XAddr:          u.String(),
					AllXAddrs:      []string{u.String()},
					Types:          r.Types,
					Scopes:         cleanTokens,
					Model:          r.Model,
					DeviceType:     devType,
				}
				byStableKey[key] = dev
			} else {
				// Append alternate XAddr if not already present
				hasXAddr := false
				for _, x := range existing.AllXAddrs {
					if x == u.String() {
						hasXAddr = true
						break
					}
				}
				if !hasXAddr && len(existing.AllXAddrs) < MaxXAddrsPerMatch {
					existing.AllXAddrs = append(existing.AllXAddrs, u.String())
				}
			}
		}
	}

	result := make([]DiscoveredDevice, 0, len(byStableKey))
	for _, dev := range byStableKey {
		result = append(result, *dev)
	}
	return result
}

func deduplicateDevices(devices []DiscoveredDevice) []DiscoveredDevice {
	byIdentity := make(map[string]DiscoveredDevice, len(devices))
	for _, device := range devices {
		if device.ManualRecorder && hasMatchingDiscoveredEndpoint(byIdentity, device.XAddr) {
			continue
		}
		if previous, ok := byIdentity[device.StableIdentity]; ok {
			if len(device.AllXAddrs) > len(previous.AllXAddrs) {
				previous.AllXAddrs = device.AllXAddrs
			}
			if previous.Manufacturer == "" {
				previous.Manufacturer = device.Manufacturer
			}
			byIdentity[device.StableIdentity] = previous
			continue
		}
		byIdentity[device.StableIdentity] = device
	}
	out := make([]DiscoveredDevice, 0, len(byIdentity))
	for _, device := range byIdentity {
		out = append(out, device)
	}
	return out
}

func hasMatchingDiscoveredEndpoint(devices map[string]DiscoveredDevice, endpoint string) bool {
	for _, device := range devices {
		if device.ManualRecorder {
			continue
		}
		if device.XAddr == endpoint {
			return true
		}
		for _, xaddr := range device.AllXAddrs {
			if xaddr == endpoint {
				return true
			}
		}
	}
	return false
}

// enrichCandidates runs SOAP enrichment concurrently over a worker pool of at most 4 goroutines.
func (e *Engine) enrichCandidates(ctx context.Context, devices []DiscoveredDevice) []DiscoveredDevice {
	if len(devices) == 0 {
		return nil
	}

	numWorkers := 4
	if len(devices) < numWorkers {
		numWorkers = len(devices)
	}

	deviceCh := make(chan int, len(devices))
	resultCh := make(chan DiscoveredDevice, len(devices))
	var wg sync.WaitGroup

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range deviceCh {
				select {
				case <-ctx.Done():
					return
				default:
				}
				enrichedDev := devices[idx]
				if !enrichedDev.ManualRecorder {
					enrichedDev = e.enrichSingleDevice(ctx, enrichedDev)
				}
				enrichedDev = e.enrichWithRecorderAdapter(ctx, enrichedDev)
				resultCh <- enrichedDev
			}
		}()
	}

	for i := range devices {
		deviceCh <- i
	}
	close(deviceCh)

	wg.Wait()
	close(resultCh)

	out := make([]DiscoveredDevice, 0, len(devices))
	for d := range resultCh {
		out = append(out, d)
	}
	return out
}

func (e *Engine) enrichWithRecorderAdapter(ctx context.Context, dev DiscoveredDevice) DiscoveredDevice {
	adapter := e.recorderAdapter
	if adapter == nil || !strings.EqualFold(strings.TrimSpace(dev.Manufacturer), adapter.Vendor()) {
		return dev
	}
	var username, password string
	if e.credentialResolver != nil {
		username, password, _ = e.credentialResolver(dev.StableIdentity)
	}
	channels, err := adapter.Discover(ctx, dev.XAddr, username, password)
	if err != nil {
		if errors.Is(err, ErrRecorderAuthRequired) {
			dev.AuthRequired = true
		}
		e.log.Debug("discovery: recorder adapter did not enrich candidate", slog.String("vendor", adapter.Vendor()), slog.Any("error", err))
		return dev
	}
	dev.VideoSources = mergeRecorderChannels(dev.VideoSources, channels)
	if len(dev.VideoSources) > 1 {
		dev.DeviceType = classifyDeviceType(strings.Join(dev.Scopes, " "), dev.Types, len(dev.VideoSources))
	}
	return dev
}

// ErrRecorderAuthRequired lets an optional vendor adapter report an
// authentication failure without exposing protocol response details.
var ErrRecorderAuthRequired = errors.New("discovery: recorder authentication required")

func mergeRecorderChannels(existing, additional []VideoSource) []VideoSource {
	byToken := make(map[string]int, len(existing)+len(additional))
	merged := append([]VideoSource(nil), existing...)
	for i := range merged {
		byToken[merged[i].SourceToken] = i
	}
	for _, candidate := range additional {
		index := -1
		if candidate.SourceToken != "" {
			if exactIndex, found := byToken[candidate.SourceToken]; found {
				index = exactIndex
			} else {
				// A numeric source/channel ID is an exact join; otherwise
				// the ONVIF stream URI must identify the same stream number.
				for i := range merged {
					if candidate.ChannelNumber > 0 && merged[i].SourceToken == strconv.Itoa(candidate.ChannelNumber) {
						index = i
						break
					}
					for _, profile := range merged[i].Profiles {
						if candidateProfilesContainURI(candidate, profile.StreamURI) {
							index = i
							break
						}
					}
					if index >= 0 {
						break
					}
				}
			}
		}
		if index < 0 {
			byToken[candidate.SourceToken] = len(merged)
			merged = append(merged, candidate)
			continue
		}
		if merged[index].Availability == "" || merged[index].Availability == ChannelAvailabilityUnknown {
			merged[index].Availability = candidate.Availability
		}
		if merged[index].ChannelNumber == 0 {
			merged[index].ChannelNumber = candidate.ChannelNumber
		}
		if merged[index].Label == "" {
			merged[index].Label = candidate.Label
		}
		known := make(map[string]struct{}, len(merged[index].Profiles))
		for _, profile := range merged[index].Profiles {
			known[profile.Token] = struct{}{}
		}
		for _, profile := range candidate.Profiles {
			if _, exists := known[profile.Token]; !exists {
				merged[index].Profiles = append(merged[index].Profiles, profile)
			}
		}
	}
	return merged
}

func candidateProfilesContainURI(source VideoSource, uri string) bool {
	if uri == "" {
		return false
	}
	u, err := url.Parse(uri)
	if err != nil {
		return false
	}
	streamPath := strings.TrimSuffix(u.Path, "/")
	for _, profile := range source.Profiles {
		if profile.StreamID > 0 && strings.HasSuffix(streamPath, "/channels/"+strconv.Itoa(profile.StreamID)) {
			return true
		}
	}
	return false
}

func (e *Engine) enrichSingleDevice(ctx context.Context, dev DiscoveredDevice) DiscoveredDevice {
	if dev.XAddr == "" {
		return dev
	}

	// 1. GetDeviceInformation
	info, err := e.onvifClient.GetDeviceInformation(ctx, dev.XAddr)
	if err != nil {
		if errors.Is(err, onvif.ErrAuthRequired) {
			dev.AuthRequired = true
			// Hito Z G1-B: a resolvable credential lets us retry the whole
			// enrichment over WS-Security instead of leaving the device
			// permanently un-enriched. No credential, or the authenticated
			// retry itself failing, falls through to the same behavior as
			// before: AuthRequired stays true, no profiles/StreamURI, and
			// the scan continues with the other devices.
			if authDev, ok := e.tryAuthenticatedEnrich(ctx, dev); ok {
				return authDev
			}
		}
		// If unauthenticated SOAP fails, we keep the data discovered passively
		return dev
	}

	if info != nil {
		if info.Manufacturer != "" {
			dev.Manufacturer = info.Manufacturer
		}
		if info.Model != "" {
			dev.Model = info.Model
		}
		if info.SerialNumber != "" {
			dev.Serial = info.SerialNumber
		}
		if info.FirmwareVersion != "" {
			dev.Firmware = info.FirmwareVersion
		}
	}

	// 2. Discover Media Service XAddr via GetCapabilities
	mediaXAddr, err := e.onvifClient.GetCapabilities(ctx, dev.XAddr)
	if err != nil || mediaXAddr == "" {
		mediaXAddr = dev.XAddr // fallback to primary XAddr
	}

	// 3. GetVideoSources to detect multichannel NVR/DVR
	sources, err := e.onvifClient.GetVideoSources(ctx, mediaXAddr)
	if err == nil && len(sources) > 0 {
		var mappedSources []VideoSource
		for _, s := range sources {
			mappedSources = append(mappedSources, VideoSource{
				SourceToken:  s.SourceToken,
				Label:        s.Label,
				Availability: ChannelAvailabilityUnknown,
			})
		}
		dev.VideoSources = mappedSources
		dev.DeviceType = classifyDeviceType(strings.Join(dev.Scopes, " "), dev.Types, len(sources))
	}

	// 4. GetProfiles to discover streams and resolutions
	profiles, err := e.onvifClient.GetProfiles(ctx, mediaXAddr)
	if err == nil && len(profiles) > 0 {
		var mappedProfiles []MediaProfile
		for _, p := range profiles {
			role := StreamRoleUnknown
			if p.Role == onvif.StreamRoleMain {
				role = StreamRoleMainStream
			} else if p.Role == onvif.StreamRoleSub {
				role = StreamRoleSubStream
			}

			uri, _ := e.onvifClient.GetStreamUri(ctx, mediaXAddr, p.Token)

			mappedProfiles = append(mappedProfiles, MediaProfile{
				Token:            p.Token,
				Name:             p.Name,
				Codec:            p.Codec,
				Width:            p.Width,
				Height:           p.Height,
				FPS:              p.FPS,
				StreamURI:        uri,
				StreamURIOrigin:  "onvif",
				Role:             role,
				VideoSourceToken: p.VideoSourceToken,
			})
		}

		dev.VideoSources = associateProfiles(dev.VideoSources, mappedProfiles)
	}

	return dev
}

// tryAuthenticatedEnrich retries enrichment over WS-Security when a
// credential resolves for dev.StableIdentity. It mirrors
// enrichSingleDevice's anonymous flow field-for-field, using only the
// existing GetXxxAuth methods (never reimplementing WS-Security).
//
// ok=false means "leave dev exactly as the caller already set it" — no
// credential was available, or an authenticated call itself failed (wrong
// or stale password, camera-side change). Either way this never falls back
// to guessing default credentials and never aborts the rest of the scan.
func (e *Engine) tryAuthenticatedEnrich(ctx context.Context, dev DiscoveredDevice) (DiscoveredDevice, bool) {
	if e.credentialResolver == nil {
		return dev, false
	}
	username, password, ok := e.credentialResolver(dev.StableIdentity)
	if !ok {
		return dev, false
	}

	// 1. GetDeviceInformationAuth
	info, err := e.onvifClient.GetDeviceInformationAuth(ctx, dev.XAddr, username, password)
	if err != nil {
		return dev, false
	}
	if info != nil {
		if info.Manufacturer != "" {
			dev.Manufacturer = info.Manufacturer
		}
		if info.Model != "" {
			dev.Model = info.Model
		}
		if info.SerialNumber != "" {
			dev.Serial = info.SerialNumber
		}
		if info.FirmwareVersion != "" {
			dev.Firmware = info.FirmwareVersion
		}
	}

	// 2. Discover Media Service XAddr via GetCapabilitiesAuth
	mediaXAddr, err := e.onvifClient.GetCapabilitiesAuth(ctx, dev.XAddr, username, password)
	if err != nil || mediaXAddr == "" {
		mediaXAddr = dev.XAddr // fallback to primary XAddr
	}

	// 3. GetVideoSourcesAuth to detect multichannel NVR/DVR
	sources, err := e.onvifClient.GetVideoSourcesAuth(ctx, mediaXAddr, username, password)
	if err == nil && len(sources) > 0 {
		var mappedSources []VideoSource
		for _, s := range sources {
			mappedSources = append(mappedSources, VideoSource{
				SourceToken:  s.SourceToken,
				Label:        s.Label,
				Availability: ChannelAvailabilityUnknown,
			})
		}
		dev.VideoSources = mappedSources
		dev.DeviceType = classifyDeviceType(strings.Join(dev.Scopes, " "), dev.Types, len(sources))
	}

	// 4. GetProfilesAuth to discover streams and resolutions
	profiles, err := e.onvifClient.GetProfilesAuth(ctx, mediaXAddr, username, password)
	if err == nil && len(profiles) > 0 {
		var mappedProfiles []MediaProfile
		for _, p := range profiles {
			role := StreamRoleUnknown
			if p.Role == onvif.StreamRoleMain {
				role = StreamRoleMainStream
			} else if p.Role == onvif.StreamRoleSub {
				role = StreamRoleSubStream
			}

			uri, _ := e.onvifClient.GetStreamUriAuth(ctx, mediaXAddr, p.Token, username, password)

			mappedProfiles = append(mappedProfiles, MediaProfile{
				Token:            p.Token,
				Name:             p.Name,
				Codec:            p.Codec,
				Width:            p.Width,
				Height:           p.Height,
				FPS:              p.FPS,
				StreamURI:        uri,
				StreamURIOrigin:  "onvif",
				Role:             role,
				VideoSourceToken: p.VideoSourceToken,
			})
		}

		dev.VideoSources = associateProfiles(dev.VideoSources, mappedProfiles)
	}

	// dev.AuthRequired stays true: it is informational ("this device does
	// require credentials"), not a gate the target builder uses — the
	// builder only cares whether a credential actually resolved.
	return dev, true
}

// associateProfiles attaches each ONVIF media profile to the VideoSource
// named by its VideoSourceConfiguration/SourceToken. A recorder's profile
// list is device-wide; assigning it to the first source would mix channels.
// A tokenless profile is only safe to keep for the sole source of a
// single-source device, which preserves older ONVIF camera behavior.
func associateProfiles(sources []VideoSource, profiles []MediaProfile) []VideoSource {
	if len(sources) == 0 {
		byToken := make(map[string][]MediaProfile)
		for _, profile := range profiles {
			if profile.VideoSourceToken != "" {
				byToken[profile.VideoSourceToken] = append(byToken[profile.VideoSourceToken], profile)
			}
		}
		if len(byToken) == 1 {
			for token, grouped := range byToken {
				return []VideoSource{{SourceToken: token, Availability: ChannelAvailabilityUnknown, Profiles: grouped}}
			}
		}
		if len(byToken) > 1 {
			out := make([]VideoSource, 0, len(byToken))
			for token, grouped := range byToken {
				out = append(out, VideoSource{SourceToken: token, Availability: ChannelAvailabilityUnknown, Profiles: grouped})
			}
			sort.Slice(out, func(i, j int) bool { return out[i].SourceToken < out[j].SourceToken })
			return out
		}
		// No source token was reported. Keep the legacy single-channel fallback
		// only when every profile is untagged; tagged profiles must not be folded
		// into a fabricated channel.
		for _, profile := range profiles {
			if profile.VideoSourceToken != "" {
				return nil
			}
		}
		return []VideoSource{{SourceToken: "source_0", Label: "Channel 1", Availability: ChannelAvailabilityUnknown, Profiles: profiles}}
	}

	byToken := make(map[string]int, len(sources))
	for i := range sources {
		sources[i].Profiles = nil
		byToken[sources[i].SourceToken] = i
	}
	for _, profile := range profiles {
		if idx, ok := byToken[profile.VideoSourceToken]; ok && profile.VideoSourceToken != "" {
			sources[idx].Profiles = append(sources[idx].Profiles, profile)
			continue
		}
		if len(sources) == 1 && profile.VideoSourceToken == "" {
			sources[0].Profiles = append(sources[0].Profiles, profile)
		}
	}
	return sources
}

func classifyDeviceType(scopes, types string, videoSourceCount int) DeviceType {
	lowerScopes := strings.ToLower(scopes)
	lowerTypes := strings.ToLower(types)

	if videoSourceCount > 1 {
		if strings.Contains(lowerScopes, "dvr") {
			return DeviceTypeDVR
		}
		return DeviceTypeNVR
	}

	if strings.Contains(lowerScopes, "nvr") || strings.Contains(lowerScopes, "recorder") {
		return DeviceTypeNVR
	}
	if strings.Contains(lowerScopes, "dvr") {
		return DeviceTypeDVR
	}
	if strings.Contains(lowerScopes, "encoder") {
		return DeviceTypeEncoder
	}
	if strings.Contains(lowerTypes, "networkvideotransmitter") || strings.Contains(lowerScopes, "camera") || strings.Contains(lowerScopes, "hardware") {
		return DeviceTypeCamera
	}

	return DeviceTypeCamera
}
