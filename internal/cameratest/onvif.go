// Package cameratest wires the camera-credentials Provider (Hito F Bloque 1)
// to the authenticated ONVIF operations (Hito F Bloque 2) for a one-shot
// credential validation test. It performs no streaming, no RTSP
// SETUP/PLAY, and no reconnect/health logic — that is Hito G.
package cameratest

import (
	"context"
	"errors"
	"net"

	"github.com/drko-dev/monitoreoedgeis/internal/cameracreds"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery/onvif"
)

// State is the classification of a one-shot credential test.
type State string

const (
	// StateValid means the device accepted the credential and returned
	// parseable data.
	StateValid State = "VALID"
	// StateInvalid means the device rejected the credential (401 / SOAP
	// auth fault).
	StateInvalid State = "INVALID"
	// StateUnreachable means the device could not be reached at all
	// (timeout, connection refused, DNS failure).
	StateUnreachable State = "UNREACHABLE"
	// StateError covers everything else: malformed responses, missing
	// credential, etc.
	StateError State = "ERROR"
)

// ONVIFResult is the outcome of TestONVIFCredential. It never carries the
// plaintext password. StreamURI, when set, is always sanitized (userinfo
// stripped).
type ONVIFResult struct {
	State        State
	Manufacturer string
	Model        string
	ProfileCount int
	StreamURI    string
	Err          error
}

// TestONVIFCredential resolves the credential for candidateKey/groupID via
// provider (DEVICE-over-GROUP precedence, see cameracreds.Provider.Resolve),
// then performs an authenticated GetDeviceInformation against xaddr. On a
// VALID result it continues with GetProfiles and GetStreamUri to confirm
// full access.
//
// candidateKey MUST be Hito E's stable device identity (discovery.Candidate
// / DiscoveredDevice's StableIdentity) — never an IP address — since that is
// the key credentials are assigned against.
func TestONVIFCredential(ctx context.Context, client *onvif.Client, provider *cameracreds.Provider, candidateKey, groupID, xaddr string) ONVIFResult {
	cred, ok := provider.Resolve(candidateKey, groupID)
	if !ok {
		return ONVIFResult{State: StateError, Err: errors.New("cameratest: no credential assigned for this candidate")}
	}

	info, err := client.GetDeviceInformationAuth(ctx, xaddr, cred.Username, cred.Password)
	if err != nil {
		return ONVIFResult{State: classify(err), Err: err}
	}

	result := ONVIFResult{State: StateValid, Manufacturer: info.Manufacturer, Model: info.Model}

	// Some devices (e.g. the Tapo TC70) require WS-Security on every ONVIF
	// operation, including GetCapabilities — so the Media service XAddr is
	// discovered authenticated, not via the passive GetCapabilities.
	mediaXAddr, capErr := client.GetCapabilitiesAuth(ctx, xaddr, cred.Username, cred.Password)
	if capErr != nil || mediaXAddr == "" {
		mediaXAddr = xaddr
	}

	profiles, err := client.GetProfilesAuth(ctx, mediaXAddr, cred.Username, cred.Password)
	if err != nil {
		result.Err = err
		return result
	}
	result.ProfileCount = len(profiles)

	uri, err := client.GetStreamUriAuth(ctx, mediaXAddr, profiles[0].Token, cred.Username, cred.Password)
	if err != nil {
		result.Err = err
		return result
	}
	result.StreamURI = uri

	return result
}

// classify maps a failure from the ONVIF client into one of the four test
// states.
func classify(err error) State {
	if errors.Is(err, onvif.ErrAuthRequired) {
		return StateInvalid
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return StateUnreachable
	}
	return StateError
}
