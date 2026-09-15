package discovery

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/discovery/onvif"
)

// TestSecondHopSSRF exercises the real enrichment flow: a malicious device answers
// GetDeviceInformation normally (passing hop 1) but returns a hostile Media XAddr from
// GetCapabilities. Before the fix, PostSOAP would blindly POST to that attacker-chosen
// destination on the next call (GetVideoSources). After the fix, every PostSOAP call
// re-validates its destination via the injected ValidateXAddr, so the hostile hop must
// never reach the network.
func TestSecondHopSSRF(t *testing.T) {
	const legitimateXAddr = "http://192.168.1.50:80/onvif/device_service"

	cases := []struct {
		name          string
		mediaXAddr    string
		expectAllowed bool
	}{
		{"loopback", "http://127.0.0.1:80/onvif/media_service", false},
		{"cloud_metadata", "http://169.254.169.254/latest/meta-data/", false},
		{"public_ip", "http://8.8.8.8/onvif/media_service", false},
		{"userinfo", "http://admin:pass@192.168.1.50/onvif/media_service", false},
		// GetCapabilities itself only accepts http/https schemes when parsing the Media
		// XAddr from the camera's response; a non-http(s) scheme is discarded and
		// enrichment safely falls back to the already-validated primary XAddr instead
		// of ever reaching the attacker's chosen scheme/destination.
		{"disallowed_scheme", "ftp://192.168.1.50/onvif/media_service", true},
		{"legitimate_private", "http://192.168.1.51:80/onvif/media_service", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var calledHosts []string

			rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				mu.Lock()
				calledHosts = append(calledHosts, req.URL.Host)
				mu.Unlock()

				action := req.Header.Get("Content-Type")
				switch {
				case strings.Contains(action, "GetDeviceInformation"):
					return xmlResponse(`<tds:GetDeviceInformationResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
						<tds:Manufacturer>Evil</tds:Manufacturer>
					</tds:GetDeviceInformationResponse>`), nil
				case strings.Contains(action, "GetCapabilities"):
					return xmlResponse(fmt.Sprintf(`<tt:Capabilities xmlns:tt="http://www.onvif.org/ver10/schema">
						<tt:Media><tt:XAddr>%s</tt:XAddr></tt:Media>
					</tt:Capabilities>`, tc.mediaXAddr)), nil
				case strings.Contains(action, "GetVideoSources"):
					return xmlResponse(`<trt:GetVideoSourcesResponse xmlns:trt="http://www.onvif.org/ver10/media/wsdl">
						<trt:VideoSources token="chan1"/>
					</trt:GetVideoSourcesResponse>`), nil
				default:
					return xmlResponse(`<empty/>`), nil
				}
			})

			onvifClient := onvif.NewClient(time.Second, nil)
			onvifClient.SetTransport(rt)
			engine := NewEngine(nil, onvifClient, nil, nil, 100*time.Millisecond, nil)

			dev := DiscoveredDevice{XAddr: legitimateXAddr}
			result := engine.enrichSingleDevice(context.Background(), dev)

			mu.Lock()
			defer mu.Unlock()

			for _, host := range calledHosts {
				if host == "127.0.0.1" || host == "169.254.169.254:80" || host == "169.254.169.254" || host == "8.8.8.8" {
					t.Fatalf("SSRF: request reached forbidden host %q (calledHosts=%v)", host, calledHosts)
				}
			}

			if tc.expectAllowed {
				if len(result.VideoSources) == 0 {
					t.Errorf("expected legitimate media XAddr to be reached and populate VideoSources, calledHosts=%v", calledHosts)
				}
			} else {
				if len(result.VideoSources) != 0 {
					t.Errorf("expected hostile media XAddr to be rejected, but VideoSources was populated: %+v", result.VideoSources)
				}
			}
		})
	}
}

func xmlResponse(body string) *http.Response {
	envelope := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>%s</s:Body></s:Envelope>`, body)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewBufferString(envelope)),
		Header:     make(http.Header),
	}
}
