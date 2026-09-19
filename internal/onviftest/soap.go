package onviftest

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
)

// Simulator serves the ONVIF Device and Media SOAP services for one Device
// fixture over HTTP (via httptest.Server), and exposes DeviceXAddr/MediaXAddr
// for wiring a real onvif.Client against it.
type Simulator struct {
	*httptest.Server
	device Device
}

// NewSimulator starts an HTTP simulator for the given device fixture on a
// dynamically assigned loopback port.
func NewSimulator(device Device) *Simulator {
	s := &Simulator{device: device}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// DeviceXAddr returns the Device service endpoint (GetDeviceInformation /
// GetCapabilities).
func (s *Simulator) DeviceXAddr() string { return s.URL + "/onvif/device_service" }

// MediaXAddr returns the Media service endpoint (GetProfiles /
// GetStreamUri). The simulator uses the same endpoint for both services,
// which real devices frequently do too.
func (s *Simulator) MediaXAddr() string { return s.URL + "/onvif/media_service" }

func (s *Simulator) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}

	if s.device.RequireAuth && !validWSSecurity(string(body), s.device.Username, s.device.Password) {
		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(soapFault("NotAuthorized", "sender not authorized")))
		return
	}

	action, respBody := s.route(string(body))
	if action == "" {
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(respBody))
}

func (s *Simulator) route(body string) (action, response string) {
	switch {
	case strings.Contains(body, "GetDeviceInformation"):
		return "GetDeviceInformation", s.deviceInformationResponse()
	case strings.Contains(body, "GetCapabilities"):
		return "GetCapabilities", s.capabilitiesResponse()
	case strings.Contains(body, "GetStreamUri"):
		return "GetStreamUri", s.streamURIResponse(body)
	case strings.Contains(body, "GetProfiles"):
		return "GetProfiles", s.profilesResponse()
	default:
		return "", ""
	}
}

func (s *Simulator) deviceInformationResponse() string {
	return envelope(fmt.Sprintf(`<tds:GetDeviceInformationResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
  <tds:Manufacturer>%s</tds:Manufacturer>
  <tds:Model>%s</tds:Model>
  <tds:FirmwareVersion>%s</tds:FirmwareVersion>
  <tds:SerialNumber>%s</tds:SerialNumber>
</tds:GetDeviceInformationResponse>`,
		xmlEscape(s.device.Manufacturer), xmlEscape(s.device.Model),
		xmlEscape(s.device.FirmwareVersion), xmlEscape(s.device.SerialNumber)))
}

func (s *Simulator) capabilitiesResponse() string {
	return envelope(fmt.Sprintf(`<tds:GetCapabilitiesResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
  <tds:Capabilities>
    <tt:Media xmlns:tt="http://www.onvif.org/ver10/schema">
      <tt:XAddr>%s</tt:XAddr>
    </tt:Media>
  </tds:Capabilities>
</tds:GetCapabilitiesResponse>`, xmlEscape(s.MediaXAddr())))
}

func (s *Simulator) profilesResponse() string {
	var b strings.Builder
	b.WriteString(`<trt:GetProfilesResponse xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">`)
	for _, p := range s.device.Profiles {
		fmt.Fprintf(&b, `
  <trt:Profiles token="%s">
    <tt:Name>%s</tt:Name>
    <tt:VideoEncoderConfiguration>
      <tt:Encoding>%s</tt:Encoding>
      <tt:Resolution>
        <tt:Width>%d</tt:Width>
        <tt:Height>%d</tt:Height>
      </tt:Resolution>
      <tt:RateControl>
        <tt:FrameRateLimit>%g</tt:FrameRateLimit>
      </tt:RateControl>
    </tt:VideoEncoderConfiguration>
  </trt:Profiles>`,
			xmlEscape(p.Token), xmlEscape(p.Name), xmlEscape(p.Codec), p.Width, p.Height, p.FPS)
	}
	b.WriteString(`
</trt:GetProfilesResponse>`)
	return envelope(b.String())
}

var profileTokenRe = regexp.MustCompile(`<[\w:]*ProfileToken>([^<]*)</[\w:]*ProfileToken>`)

func (s *Simulator) streamURIResponse(requestBody string) string {
	token := ""
	if m := profileTokenRe.FindStringSubmatch(requestBody); len(m) == 2 {
		token = m[1]
	}
	uri := ""
	for _, p := range s.device.Profiles {
		if p.Token == token {
			uri = p.StreamURI
			break
		}
	}
	if uri == "" && len(s.device.Profiles) > 0 {
		uri = s.device.Profiles[0].StreamURI
	}
	return envelope(fmt.Sprintf(`<trt:GetStreamUriResponse xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">
  <trt:MediaUri>
    <tt:Uri>%s</tt:Uri>
  </trt:MediaUri>
</trt:GetStreamUriResponse>`, xmlEscape(uri)))
}

func envelope(bodyXML string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope">
  <soap:Body>
%s
  </soap:Body>
</soap:Envelope>`, bodyXML)
}

func soapFault(subcode, reason string) string {
	return envelope(fmt.Sprintf(`<soap:Fault>
    <soap:Code><soap:Value>soap:Sender</soap:Value><soap:Subcode><soap:Value>wsse:%s</soap:Value></soap:Subcode></soap:Code>
    <soap:Reason><soap:Text>%s</soap:Text></soap:Reason>
  </soap:Fault>`, subcode, xmlEscape(reason)))
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

var (
	usernameRe = regexp.MustCompile(`<wsse:Username>([^<]*)</wsse:Username>`)
	passwordRe = regexp.MustCompile(`<wsse:Password[^>]*>([^<]*)</wsse:Password>`)
	nonceRe    = regexp.MustCompile(`<wsse:Nonce[^>]*>([^<]*)</wsse:Nonce>`)
	createdRe  = regexp.MustCompile(`<wsu:Created>([^<]*)</wsu:Created>`)
)

// validWSSecurity validates a WS-Security UsernameToken (PasswordDigest)
// header against the expected username/password, per the ONVIF profile:
//
//	PasswordDigest == Base64(SHA1(RawNonce + Created + Password))
func validWSSecurity(body, expectedUser, expectedPassword string) bool {
	um := usernameRe.FindStringSubmatch(body)
	pm := passwordRe.FindStringSubmatch(body)
	nm := nonceRe.FindStringSubmatch(body)
	cm := createdRe.FindStringSubmatch(body)
	if um == nil || pm == nil || nm == nil || cm == nil {
		return false
	}
	if um[1] != expectedUser {
		return false
	}
	rawNonce, err := base64.StdEncoding.DecodeString(nm[1])
	if err != nil {
		return false
	}
	h := sha1.New()
	h.Write(rawNonce)
	h.Write([]byte(cm[1]))
	h.Write([]byte(expectedPassword))
	expectedDigest := base64.StdEncoding.EncodeToString(h.Sum(nil))
	return pm[1] == expectedDigest
}
