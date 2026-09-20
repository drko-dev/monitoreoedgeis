package rtsp

import "testing"

// TestParseTarget covers the URI decomposition used to build a CameraTarget
// from a discovered ONVIF StreamURI.
func TestParseTarget(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantAddr string
		wantPath string
		wantErr  bool
	}{
		{
			name:     "plain path",
			raw:      "rtsp://10.0.0.20:554/stream1",
			wantAddr: "10.0.0.20:554",
			wantPath: "/stream1",
		},
		{
			name:     "default port is preserved as 554",
			raw:      "rtsp://10.0.0.20/stream1",
			wantAddr: "10.0.0.20:554",
			wantPath: "/stream1",
		},
		{
			name:     "query string is preserved exactly",
			raw:      "rtsp://10.0.0.20:554/stream?channel=1&subtype=0",
			wantAddr: "10.0.0.20:554",
			wantPath: "/stream?channel=1&subtype=0",
		},
		{
			name:     "query with a single parameter",
			raw:      "rtsp://10.0.0.20/Streaming/Channels/101?transportmode=unicast",
			wantAddr: "10.0.0.20:554",
			wantPath: "/Streaming/Channels/101?transportmode=unicast",
		},
		{
			name:     "userinfo is discarded and never reaches addr or path",
			raw:      "rtsp://admin:supersecret@10.0.0.20:554/stream1",
			wantAddr: "10.0.0.20:554",
			wantPath: "/stream1",
		},
		{
			name:     "userinfo AND query: both handled",
			raw:      "rtsp://admin:supersecret@10.0.0.20:554/cam/realmonitor?channel=1&subtype=0",
			wantAddr: "10.0.0.20:554",
			wantPath: "/cam/realmonitor?channel=1&subtype=0",
		},
		{
			name:     "non-default port",
			raw:      "rtsp://10.0.0.20:8554/live",
			wantAddr: "10.0.0.20:8554",
			wantPath: "/live",
		},
		// rtsps:// is deliberately NOT supported: this client dials a plain TCP
		// socket and implements no TLS transport.
		{name: "rtsps is rejected", raw: "rtsps://10.0.0.20:322/stream1", wantErr: true},
		{name: "http is rejected", raw: "http://10.0.0.20/stream1", wantErr: true},
		{name: "missing scheme is rejected", raw: "10.0.0.20/stream1", wantErr: true},
		{name: "missing host is rejected", raw: "rtsp:///stream1", wantErr: true},
		{name: "garbage is rejected", raw: "://:::", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr, path, err := ParseTarget(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseTarget(%q) = (%q, %q), want error", tc.raw, addr, path)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTarget(%q) error = %v", tc.raw, err)
			}
			if addr != tc.wantAddr {
				t.Errorf("addr = %q, want %q", addr, tc.wantAddr)
			}
			if path != tc.wantPath {
				t.Errorf("path = %q, want %q", path, tc.wantPath)
			}
			// No credential material may survive in either output.
			for _, secret := range []string{"admin", "supersecret", "@"} {
				if contains(addr, secret) || contains(path, secret) {
					t.Errorf("ParseTarget leaked %q: addr=%q path=%q", secret, addr, path)
				}
			}
		})
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		})()
}

// TestParseTarget_QueryRoundTripsIntoTheRequestURI guards the reason the query
// must be kept: client.go builds the request URI as "rtsp://" + addr + path, so
// a dropped query silently dials the wrong stream.
func TestParseTarget_QueryRoundTripsIntoTheRequestURI(t *testing.T) {
	const raw = "rtsp://10.0.0.20:554/stream?channel=1&subtype=0"
	addr, path, err := ParseTarget(raw)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt := "rtsp://" + addr + path; rebuilt != raw {
		t.Fatalf("rebuilt URI = %q, want %q (the query must survive the round trip)", rebuilt, raw)
	}
}
