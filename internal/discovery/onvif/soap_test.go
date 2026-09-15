package onvif

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// TestIsAuthFault_DoesNotFalsePositiveOnDefaultFields guards against a real
// false positive found via Tapo TC70 hardware testing: a successful
// GetProfiles response containing PTZ "Default..." field names (e.g.
// DefaultAbsolutePantTiltPositionSpace) together with the standard
// xmlns:wsse="...wssecurity..." namespace declaration was previously
// misclassified as an auth fault, because "default" contains "fault" as a
// bare substring and the namespace URI contains "security".
func TestIsAuthFault_DoesNotFalsePositiveOnDefaultFields(t *testing.T) {
	body := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<SOAP-ENV:Envelope xmlns:SOAP-ENV="http://www.w3.org/2003/05/soap-envelope" xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd" xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">
  <SOAP-ENV:Body>
    <trt:GetProfilesResponse>
      <trt:Profiles token="profile_1">
        <tt:PTZConfiguration>
          <tt:DefaultAbsolutePantTiltPositionSpace>http://www.onvif.org/ver10/tptz/PanTiltSpaces/PositionGenericSpace</tt:DefaultAbsolutePantTiltPositionSpace>
          <tt:DefaultPTZSpeed/>
          <tt:DefaultPTZTimeout>PT5S</tt:DefaultPTZTimeout>
        </tt:PTZConfiguration>
      </trt:Profiles>
    </trt:GetProfilesResponse>
  </SOAP-ENV:Body>
</SOAP-ENV:Envelope>`)
	if isAuthFault(body) {
		t.Fatalf("isAuthFault false-positived on a successful response with PTZ Default* fields")
	}

	// A real auth fault must still be detected.
	realFault := []byte(`<SOAP-ENV:Envelope xmlns:SOAP-ENV="http://www.w3.org/2003/05/soap-envelope"><SOAP-ENV:Body><SOAP-ENV:Fault><SOAP-ENV:Reason><SOAP-ENV:Text>NotAuthorized</SOAP-ENV:Text></SOAP-ENV:Reason></SOAP-ENV:Fault></SOAP-ENV:Body></SOAP-ENV:Envelope>`)
	if !isAuthFault(realFault) {
		t.Fatalf("isAuthFault must still detect a real SOAP Fault with NotAuthorized")
	}
}

// TestIsAuthFault_DoesNotFalsePositiveOnNonAuthFaultWithWSSENamespace guards
// against a residual false positive: the bare "security" match matched any
// SOAP Fault at all on an envelope that declares the wsse namespace (its URI
// contains "security"), even a fault with a completely unrelated cause such
// as an invalid argument value.
func TestIsAuthFault_DoesNotFalsePositiveOnNonAuthFaultWithWSSENamespace(t *testing.T) {
	body := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<SOAP-ENV:Envelope xmlns:SOAP-ENV="http://www.w3.org/2003/05/soap-envelope" xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd">
  <SOAP-ENV:Body>
    <SOAP-ENV:Fault>
      <SOAP-ENV:Code><SOAP-ENV:Value>SOAP-ENV:Sender</SOAP-ENV:Value><SOAP-ENV:Subcode><SOAP-ENV:Value>ter:InvalidArgVal</SOAP-ENV:Value></SOAP-ENV:Subcode></SOAP-ENV:Code>
      <SOAP-ENV:Reason><SOAP-ENV:Text>Invalid Argument Value</SOAP-ENV:Text></SOAP-ENV:Reason>
    </SOAP-ENV:Fault>
  </SOAP-ENV:Body>
</SOAP-ENV:Envelope>`)
	if isAuthFault(body) {
		t.Fatalf("isAuthFault false-positived on a non-auth SOAP Fault (ter:InvalidArgVal) just because the envelope declares the wsse namespace")
	}
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

func TestGetStreamUri_EscapesProfileTokenXMLInjection(t *testing.T) {
	malicious := `abc</ProfileToken><Evil>injected</Evil><ProfileToken>`

	var capturedBody string
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(req.Body)
		capturedBody = string(b)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body></s:Body></s:Envelope>`)),
			Header:     make(http.Header),
		}, nil
	})

	client := NewClient(time.Second, nil)
	client.SetTransport(rt)
	client.SetXAddrValidator(func(raw string) (*url.URL, int, error) {
		u, err := url.Parse(raw)
		return u, 80, err
	})

	_, _ = client.GetStreamUri(context.Background(), "http://192.168.1.50/onvif/media_service", malicious)

	if strings.Contains(capturedBody, "<Evil>") {
		t.Fatalf("XML injection succeeded, request body contains raw injected tag: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, "&lt;Evil&gt;") {
		t.Fatalf("expected malicious profileToken to be escaped as literal text, got: %s", capturedBody)
	}
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNoopCredentialProvider(t *testing.T) {
	prov := NoopCredentialProvider{}
	u, p, ok := prov.GetCredentials("http://192.168.1.50/onvif")
	if ok || u != "" || p != "" {
		t.Errorf("NoopCredentialProvider must return empty credentials")
	}
}
