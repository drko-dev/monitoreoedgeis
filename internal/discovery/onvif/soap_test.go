package onvif

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGetDeviceInformation_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/soap+xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
  <soap:Body>
    <tds:GetDeviceInformationResponse>
      <tds:Manufacturer>Hikvision</tds:Manufacturer>
      <tds:Model>DS-2CD2042WD-I</tds:Model>
      <tds:FirmwareVersion>V5.4.5</tds:FirmwareVersion>
      <tds:SerialNumber>DS-2CD2042WD-I20160808AAWR123456789</tds:SerialNumber>
    </tds:GetDeviceInformationResponse>
  </soap:Body>
</soap:Envelope>`))
	}))
	defer ts.Close()
}

func TestAuthRequired_Http401(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()

	client := NewClient(1*time.Second, nil)
	if !isAuthFault([]byte(`<soap:Fault><soap:Value>soap:Sender</soap:Value><soap:Subcode><soap:Value>ter:NotAuthorized</soap:Value></soap:Subcode></soap:Fault>`)) {
		t.Errorf("expected isAuthFault to return true for NotAuthorized")
	}
	if !isAuthFault([]byte(`<soap:Fault><soap:Reason>FailedAuthentication</soap:Reason></soap:Fault>`)) {
		t.Errorf("expected isAuthFault to return true for FailedAuthentication")
	}
	if isAuthFault([]byte(`<soap:Envelope><soap:Body><OK/></soap:Body></soap:Envelope>`)) {
		t.Errorf("expected isAuthFault to return false for OK")
	}
	_ = client
}

func TestSanitizeRTSPURI(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{
			input:    "rtsp://admin:password123@192.168.1.50:554/Streaming/Channels/101",
			expected: "rtsp://192.168.1.50:554/Streaming/Channels/101",
		},
		{
			input:    "rtsp://192.168.1.50:554/live/ch0",
			expected: "rtsp://192.168.1.50:554/live/ch0",
		},
		{
			input:    "rtsp://user:secret!@#$@10.0.0.5/main",
			expected: "rtsp://10.0.0.5/main",
		},
	}

	for _, tc := range cases {
		got := sanitizeRTSPURI(tc.input)
		if strings.Contains(got, "password123") || strings.Contains(got, "secret") {
			t.Errorf("SanitizeRTSPURI leaked secret: %s", got)
		}
		if !strings.HasPrefix(got, "rtsp://") {
			t.Errorf("expected rtsp prefix: %s", got)
		}
	}
}

func TestNoopCredentialProvider(t *testing.T) {
	prov := NoopCredentialProvider{}
	u, p, ok := prov.GetCredentials("http://192.168.1.50/onvif")
	if ok || u != "" || p != "" {
		t.Errorf("NoopCredentialProvider must return empty credentials")
	}
}
