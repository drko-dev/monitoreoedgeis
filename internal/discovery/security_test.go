package discovery

import (
	"strings"
	"testing"
)

func TestValidateXAddr_AllowedPrivateIPv4(t *testing.T) {
	valid := []struct {
		raw          string
		expectedHost string
		expectedPort int
	}{
		{"http://192.168.1.100/onvif/device_service", "192.168.1.100", 80},
		{"https://192.168.1.100:8443/onvif/device_service", "192.168.1.100", 8443},
		{"http://10.0.0.15:8000/device_service", "10.0.0.15", 8000},
		{"http://172.16.0.50:8899/onvif", "172.16.0.50", 8899},
		{"http://169.254.1.20:80/onvif", "169.254.1.20", 80},
	}

	for _, tc := range valid {
		u, port, err := ValidateXAddr(tc.raw)
		if err != nil {
			t.Errorf("ValidateXAddr(%q) failed unexpectedly: %v", tc.raw, err)
			continue
		}
		if u.Hostname() != tc.expectedHost {
			t.Errorf("expected host %s, got %s", tc.expectedHost, u.Hostname())
		}
		if port != tc.expectedPort {
			t.Errorf("expected port %d, got %d", tc.expectedPort, port)
		}
	}
}

func TestValidateXAddr_DisallowedDestinations(t *testing.T) {
	invalid := []struct {
		raw         string
		expectedErr string
	}{
		{"http://8.8.8.8/onvif", "not in RFC 1918/3927 private range"},
		{"http://1.1.1.1:8080/onvif", "not in RFC 1918/3927 private range"},
		{"http://127.0.0.1/onvif", "loopback addresses are prohibited"},
		{"http://127.0.0.50:8000/onvif", "loopback addresses are prohibited"},
		{"http://169.254.169.254/latest/meta-data/", "cloud metadata endpoint is prohibited"},
		{"http://admin:pass@192.168.1.50/onvif", "must not contain userinfo"},
		{"ftp://192.168.1.50/onvif", "scheme must be http or https"},
		{"rtsp://192.168.1.50/onvif", "scheme must be http or https"},
		{"http://192.168.1.50:70000/onvif", "port out of valid range"},
		{"http://192.168.1.50:0/onvif", "port out of valid range"},
		{"http://192.168.1.50/onvif\x00evil", "illegal control characters"},
		{"http://example.com/onvif", "is not an IP literal"},
		{"", "empty XAddr"},
	}

	for _, tc := range invalid {
		_, _, err := ValidateXAddr(tc.raw)
		if err == nil {
			t.Errorf("ValidateXAddr(%q) should have failed, but succeeded", tc.raw)
			continue
		}
		if !strings.Contains(err.Error(), tc.expectedErr) {
			t.Errorf("ValidateXAddr(%q) error %q does not contain %q", tc.raw, err.Error(), tc.expectedErr)
		}
	}
}

func TestValidateXAddr_Oversized(t *testing.T) {
	huge := "http://192.168.1.1/" + strings.Repeat("a", MaxURLLength)
	_, _, err := ValidateXAddr(huge)
	if err != ErrXAddrTooLong {
		t.Errorf("expected ErrXAddrTooLong, got %v", err)
	}
}

func TestSanitizeText(t *testing.T) {
	input := "  Hello \t\n World! \x00\x1f Test  "
	got := SanitizeText(input, 30)
	if got != "Hello World! Test" {
		t.Errorf("unexpected SanitizeText output: %q", got)
	}

	truncated := SanitizeText("1234567890", 5)
	if truncated != "12345" {
		t.Errorf("expected truncated text '12345', got %q", truncated)
	}
}

func TestCleanScopes_PurgesCredentials(t *testing.T) {
	raw := "onvif://www.onvif.org/hardware/Model1 password=secret123 onvif://www.onvif.org/type/camera token=abc123xyz"
	cleanedStr, tokens := CleanScopes(raw)

	if strings.Contains(cleanedStr, "secret123") || strings.Contains(cleanedStr, "abc123xyz") {
		t.Errorf("CleanScopes leaked credential token: %s", cleanedStr)
	}

	if len(tokens) != 2 {
		t.Fatalf("expected 2 clean tokens, got %d: %v", len(tokens), tokens)
	}
	if tokens[0] != "onvif://www.onvif.org/hardware/Model1" || tokens[1] != "onvif://www.onvif.org/type/camera" {
		t.Errorf("unexpected tokens: %v", tokens)
	}
}
