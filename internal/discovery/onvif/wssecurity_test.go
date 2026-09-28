package onvif

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const deviceInfoResponse = `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
  <s:Body>
    <tds:GetDeviceInformationResponse>
      <tds:Manufacturer>TP-LINK</tds:Manufacturer>
      <tds:Model>TC70</tds:Model>
    </tds:GetDeviceInformationResponse>
  </s:Body>
</s:Envelope>`

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(handler)
	client := NewClient(2*time.Second, nil)
	client.SetXAddrValidator(func(raw string) (*url.URL, int, error) {
		u, err := url.Parse(raw)
		return u, 80, err
	})
	return client, ts
}

func TestGetDeviceInformationAuth_ValidCredential(t *testing.T) {
	var capturedBody string
	client, ts := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		capturedBody = string(b)
		w.Header().Set("Content-Type", "application/soap+xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(deviceInfoResponse))
	})
	defer ts.Close()

	info, err := client.GetDeviceInformationAuth(context.Background(), ts.URL, "admin", "correct-password")
	if err != nil {
		t.Fatalf("expected VALID, got error: %v", err)
	}
	if info.Manufacturer != "TP-LINK" || info.Model != "TC70" {
		t.Errorf("unexpected device info: %+v", info)
	}
	if !strings.Contains(capturedBody, "<wsse:UsernameToken>") {
		t.Errorf("expected WS-Security UsernameToken in request body: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, `Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest"`) {
		t.Errorf("expected PasswordDigest type in request body: %s", capturedBody)
	}
	if strings.Contains(capturedBody, "correct-password") {
		t.Fatalf("plaintext password leaked into SOAP request body: %s", capturedBody)
	}
}

func TestGetDeviceInformationAuth_WrongPassword(t *testing.T) {
	client, ts := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	defer ts.Close()

	_, err := client.GetDeviceInformationAuth(context.Background(), ts.URL, "admin", "wrong-password")
	if err == nil {
		t.Fatal("expected an error for wrong password")
	}
	if !errors.Is(err, ErrAuthRequired) {
		t.Errorf("expected ErrAuthRequired (INVALID), got %v", err)
	}
}

func TestGetDeviceInformationAuth_MalformedResponse(t *testing.T) {
	client, ts := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not even xml <<<`))
	})
	defer ts.Close()

	_, err := client.GetDeviceInformationAuth(context.Background(), ts.URL, "admin", "pw")
	if err == nil {
		t.Fatal("expected an error for malformed response, got none (must not panic either)")
	}
	if errors.Is(err, ErrAuthRequired) {
		t.Errorf("malformed response must not classify as auth failure: %v", err)
	}
}

func TestGetDeviceInformationAuth_EmptyResponseIsError(t *testing.T) {
	client, ts := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body></s:Body></s:Envelope>`))
	})
	defer ts.Close()

	_, err := client.GetDeviceInformationAuth(context.Background(), ts.URL, "admin", "pw")
	if err == nil {
		t.Fatal("expected an error when response has none of the expected fields")
	}
}

func TestGetDeviceInformationAuth_Timeout(t *testing.T) {
	unblock := make(chan struct{})
	client, ts := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-unblock // never respond within the client's timeout
	})
	defer ts.Close()
	defer close(unblock)

	client.httpClient.Timeout = 50 * time.Millisecond
	_, err := client.GetDeviceInformationAuth(context.Background(), ts.URL, "admin", "pw")
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) {
		t.Errorf("expected a net.Error-shaped timeout (UNREACHABLE), got %T: %v", err, err)
	}
}

func TestWSSecurityHeader_EscapesUsernameXMLInjection(t *testing.T) {
	malicious := `admin</wsse:Username><Evil>injected</Evil><wsse:Username>`
	header, err := buildWSSecurityHeader(malicious, "pw")
	if err != nil {
		t.Fatalf("buildWSSecurityHeader failed: %v", err)
	}
	if strings.Contains(header, "<Evil>") {
		t.Fatalf("XML injection succeeded, header contains raw injected tag: %s", header)
	}
	if !strings.Contains(header, "&lt;/wsse:Username&gt;") {
		t.Fatalf("expected malicious username to be escaped: %s", header)
	}
}

func TestWSSecurityHeader_DigestMatchesFormula(t *testing.T) {
	header, err := buildWSSecurityHeader("admin", "secret")
	if err != nil {
		t.Fatalf("buildWSSecurityHeader failed: %v", err)
	}

	nonce := extractXMLText(t, header, "Nonce")
	created := extractXMLText(t, header, "Created")
	digest := extractXMLText(t, header, "Password")

	nonceBytes, err := base64.StdEncoding.DecodeString(nonce)
	if err != nil {
		t.Fatalf("nonce is not valid base64: %v", err)
	}
	if len(nonceBytes) < 16 {
		t.Errorf("expected nonce >= 16 bytes, got %d", len(nonceBytes))
	}

	sum := sha1.Sum(append(append(append([]byte{}, nonceBytes...), []byte(created)...), []byte("secret")...))
	want := base64.StdEncoding.EncodeToString(sum[:])
	if digest != want {
		t.Errorf("digest mismatch: got %s want %s", digest, want)
	}

	if strings.Contains(header, "secret") {
		t.Fatalf("plaintext password leaked into WS-Security header: %s", header)
	}
}

func TestWSSecurityHeader_NonceNotReused(t *testing.T) {
	h1, err := buildWSSecurityHeader("admin", "pw")
	if err != nil {
		t.Fatal(err)
	}
	h2, err := buildWSSecurityHeader("admin", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if extractXMLText(t, h1, "Nonce") == extractXMLText(t, h2, "Nonce") {
		t.Error("nonce was reused across two header builds")
	}
}

func TestGetDeviceInformationAuth_NoSecretLeakInLogs(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	client, ts := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(deviceInfoResponse))
	})
	defer ts.Close()

	info, err := client.GetDeviceInformationAuth(context.Background(), ts.URL, "admin", "super-secret-pw")
	logger.Info("test completed", "err", err, "manufacturer", info.Manufacturer)

	if strings.Contains(logBuf.String(), "super-secret-pw") {
		t.Fatalf("password leaked into logs: %s", logBuf.String())
	}
}

// extractXMLText finds the first element named localTag (ignoring its
// namespace prefix) inside xmlFragment and returns its text content.
func extractXMLText(t *testing.T, xmlFragment, localTag string) string {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(xmlFragment))
	for {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("failed to locate tag %s: %v", localTag, err)
		}
		st, isStart := tok.(xml.StartElement)
		if isStart && st.Name.Local == localTag {
			var text string
			if err := dec.DecodeElement(&text, &st); err == nil {
				return text
			}
		}
	}
}

// TestGetProfilesAuth_VideoCodecNotOverwrittenByAudioEncoding is the
// GetProfilesAuth counterpart to soap_test.go's regression test: same real
// bug (AudioEncoderConfiguration's <Encoding> clobbering the video codec),
// found against a physical TP-Link Tapo TC70 during UX5 commissioning,
// reproduced here on the authenticated (WS-Security) media profile parser
// that the installer's credential-based ONVIF flow actually uses.
func TestGetProfilesAuth_VideoCodecNotOverwrittenByAudioEncoding(t *testing.T) {
	client, ts := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/soap+xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<SOAP-ENV:Envelope xmlns:SOAP-ENV="http://www.w3.org/2003/05/soap-envelope" xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">
  <SOAP-ENV:Body>
    <trt:GetProfilesResponse>
      <trt:Profiles token="profile_1">
        <tt:Name>MainStream</tt:Name>
        <tt:VideoEncoderConfiguration>
          <tt:Encoding>H264</tt:Encoding>
          <tt:Resolution><tt:Width>1920</tt:Width><tt:Height>1080</tt:Height></tt:Resolution>
          <tt:RateControl><tt:FrameRateLimit>15</tt:FrameRateLimit></tt:RateControl>
        </tt:VideoEncoderConfiguration>
        <tt:AudioEncoderConfiguration>
          <tt:Encoding>G711</tt:Encoding>
          <tt:Bitrate>64</tt:Bitrate>
        </tt:AudioEncoderConfiguration>
      </trt:Profiles>
    </trt:GetProfilesResponse>
  </SOAP-ENV:Body>
</SOAP-ENV:Envelope>`))
	})
	defer ts.Close()

	profiles, err := client.GetProfilesAuth(context.Background(), ts.URL, "admin", "secret")
	if err != nil {
		t.Fatalf("GetProfilesAuth: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(profiles))
	}
	p := profiles[0]
	if p.Codec != "H264" {
		t.Fatalf("expected video Codec H264, got %q (audio encoding leaked into video codec)", p.Codec)
	}
	if p.Width != 1920 || p.Height != 1080 || p.FPS != 15 {
		t.Fatalf("expected 1920x1080@15, got %dx%d@%v", p.Width, p.Height, p.FPS)
	}
}

// TestGetProfilesAuth_FourChannelDVRSourceTokensDistinct is UX-6's minimum
// required fixture: a realistic 4-channel DVR/NVR response where each
// Profiles entry names a different VideoSourceConfiguration/SourceToken.
// Guards that per-channel identity survives the authenticated parser
// unmixed -- no channel's token leaks onto another's profile.
func TestGetProfilesAuth_FourChannelDVRSourceTokensDistinct(t *testing.T) {
	client, ts := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/soap+xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<SOAP-ENV:Envelope xmlns:SOAP-ENV="http://www.w3.org/2003/05/soap-envelope" xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">
  <SOAP-ENV:Body>
    <trt:GetProfilesResponse>
      <trt:Profiles token="profile_ch1">
        <tt:Name>Channel1</tt:Name>
        <tt:VideoSourceConfiguration token="vsc_ch1"><tt:SourceToken>ch1</tt:SourceToken></tt:VideoSourceConfiguration>
        <tt:VideoEncoderConfiguration>
          <tt:Encoding>H264</tt:Encoding>
          <tt:Resolution><tt:Width>1920</tt:Width><tt:Height>1080</tt:Height></tt:Resolution>
        </tt:VideoEncoderConfiguration>
      </trt:Profiles>
      <trt:Profiles token="profile_ch2">
        <tt:Name>Channel2</tt:Name>
        <tt:VideoSourceConfiguration token="vsc_ch2"><tt:SourceToken>ch2</tt:SourceToken></tt:VideoSourceConfiguration>
        <tt:VideoEncoderConfiguration>
          <tt:Encoding>H264</tt:Encoding>
          <tt:Resolution><tt:Width>1280</tt:Width><tt:Height>720</tt:Height></tt:Resolution>
        </tt:VideoEncoderConfiguration>
      </trt:Profiles>
      <trt:Profiles token="profile_ch3">
        <tt:Name>Channel3</tt:Name>
        <tt:VideoSourceConfiguration token="vsc_ch3"><tt:SourceToken>ch3</tt:SourceToken></tt:VideoSourceConfiguration>
        <tt:VideoEncoderConfiguration>
          <tt:Encoding>H265</tt:Encoding>
          <tt:Resolution><tt:Width>1920</tt:Width><tt:Height>1080</tt:Height></tt:Resolution>
        </tt:VideoEncoderConfiguration>
      </trt:Profiles>
      <trt:Profiles token="profile_ch4">
        <tt:Name>Channel4</tt:Name>
        <tt:VideoSourceConfiguration token="vsc_ch4"><tt:SourceToken>ch4</tt:SourceToken></tt:VideoSourceConfiguration>
        <tt:VideoEncoderConfiguration>
          <tt:Encoding>H264</tt:Encoding>
          <tt:Resolution><tt:Width>640</tt:Width><tt:Height>480</tt:Height></tt:Resolution>
        </tt:VideoEncoderConfiguration>
      </trt:Profiles>
    </trt:GetProfilesResponse>
  </SOAP-ENV:Body>
</SOAP-ENV:Envelope>`))
	})
	defer ts.Close()

	profiles, err := client.GetProfilesAuth(context.Background(), ts.URL, "admin", "secret")
	if err != nil {
		t.Fatalf("GetProfilesAuth: %v", err)
	}
	if len(profiles) != 4 {
		t.Fatalf("expected 4 channel profiles, got %d", len(profiles))
	}
	wantTokens := map[string]string{
		"profile_ch1": "ch1",
		"profile_ch2": "ch2",
		"profile_ch3": "ch3",
		"profile_ch4": "ch4",
	}
	seen := map[string]bool{}
	for _, p := range profiles {
		want, ok := wantTokens[p.Token]
		if !ok {
			t.Fatalf("unexpected profile token %q", p.Token)
		}
		if p.VideoSourceToken != want {
			t.Fatalf("profile %q: expected VideoSourceToken %q, got %q (channel identity leaked/mixed)", p.Token, want, p.VideoSourceToken)
		}
		seen[p.VideoSourceToken] = true
	}
	if len(seen) != 4 {
		t.Fatalf("expected 4 distinct VideoSourceTokens, got %d: %v", len(seen), seen)
	}
}
