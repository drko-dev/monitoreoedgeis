package onvif

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	wsseNS              = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd"
	wsuNS               = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd"
	passwordDigestType  = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest"
	nonceEncodingBase64 = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary"
)

// buildWSSecurityHeader builds a WS-Security UsernameToken header using
// PasswordDigest, per the ONVIF/WS-Security profile:
//
//	PasswordDigest = Base64(SHA1(RawNonce + Created + Password))
//
// The nonce is freshly generated (crypto/rand) for every call and never
// reused across requests. The plaintext password is consumed only to
// compute the digest: it is never present in the returned XML, and callers
// must not log it either.
func buildWSSecurityHeader(username, password string) (string, error) {
	nonce := make([]byte, 20)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("onvif: generate WS-Security nonce: %w", err)
	}
	created := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	h := sha1.New()
	h.Write(nonce)
	h.Write([]byte(created))
	h.Write([]byte(password))
	digest := base64.StdEncoding.EncodeToString(h.Sum(nil))
	nonceB64 := base64.StdEncoding.EncodeToString(nonce)

	var escapedUser bytes.Buffer
	if err := xml.EscapeText(&escapedUser, []byte(username)); err != nil {
		return "", fmt.Errorf("onvif: escape WS-Security username: %w", err)
	}

	return fmt.Sprintf(`<wsse:Security xmlns:wsse="%s" xmlns:wsu="%s">
    <wsse:UsernameToken>
      <wsse:Username>%s</wsse:Username>
      <wsse:Password Type="%s">%s</wsse:Password>
      <wsse:Nonce EncodingType="%s">%s</wsse:Nonce>
      <wsu:Created>%s</wsu:Created>
    </wsse:UsernameToken>
  </wsse:Security>`,
		wsseNS, wsuNS, escapedUser.String(), passwordDigestType, digest, nonceEncodingBase64, nonceB64, created), nil
}

// PostSOAPAuth is PostSOAP with a WS-Security UsernameToken (PasswordDigest)
// header injected, for authenticated ONVIF operations. The plaintext
// password never appears in the returned error.
func (c *Client) PostSOAPAuth(ctx context.Context, xaddr, action, bodyXML, username, password string) ([]byte, error) {
	header, err := buildWSSecurityHeader(username, password)
	if err != nil {
		return nil, err
	}
	return c.postSOAP(ctx, xaddr, action, header, bodyXML)
}

// GetDeviceInformationAuth is the WS-Security-authenticated counterpart of
// GetDeviceInformation. It is used only by the one-shot credential test
// flow (internal/cameratest) — never by the passive, unauthenticated Hito E
// discovery/enrichment path, which keeps using GetDeviceInformation.
func (c *Client) GetDeviceInformationAuth(ctx context.Context, xaddr, username, password string) (*DeviceInfo, error) {
	body := `<GetDeviceInformation xmlns="http://www.onvif.org/ver10/device/wsdl"/>`
	respBytes, err := c.PostSOAPAuth(ctx, xaddr, "http://www.onvif.org/ver10/device/wsdl/GetDeviceInformation", body, username, password)
	if err != nil {
		return nil, err
	}
	return parseDeviceInfoStrict(respBytes)
}

// GetCapabilitiesAuth is the WS-Security-authenticated counterpart of
// GetCapabilities, used only by internal/cameratest. Some devices (e.g. the
// Tapo TC70) require WS-Security on every ONVIF operation, including
// GetCapabilities, so the test flow cannot rely on the unauthenticated
// GetCapabilities to discover the Media service XAddr.
func (c *Client) GetCapabilitiesAuth(ctx context.Context, xaddr, username, password string) (mediaXAddr string, err error) {
	body := `<GetCapabilities xmlns="http://www.onvif.org/ver10/device/wsdl"><Category>All</Category></GetCapabilities>`
	respBytes, err := c.PostSOAPAuth(ctx, xaddr, "http://www.onvif.org/ver10/device/wsdl/GetCapabilities", body, username, password)
	if err != nil {
		return "", err
	}
	return parseMediaXAddr(respBytes), nil
}

// GetProfilesAuth is the WS-Security-authenticated counterpart of
// GetProfiles, used only by internal/cameratest.
func (c *Client) GetProfilesAuth(ctx context.Context, mediaXAddr, username, password string) ([]MediaProfile, error) {
	body := `<GetProfiles xmlns="http://www.onvif.org/ver10/media/wsdl"/>`
	respBytes, err := c.PostSOAPAuth(ctx, mediaXAddr, "http://www.onvif.org/ver10/media/wsdl/GetProfiles", body, username, password)
	if err != nil {
		return nil, err
	}
	return parseProfilesStrict(respBytes)
}

// GetStreamUriAuth is the WS-Security-authenticated counterpart of
// GetStreamUri, used only by internal/cameratest. Like GetStreamUri, the
// returned URI is always sanitized to strip userinfo before it is returned.
func (c *Client) GetStreamUriAuth(ctx context.Context, mediaXAddr, profileToken, username, password string) (string, error) {
	var escapedToken bytes.Buffer
	if err := xml.EscapeText(&escapedToken, []byte(profileToken)); err != nil {
		return "", fmt.Errorf("onvif: failed to escape profile token: %w", err)
	}

	body := fmt.Sprintf(`<GetStreamUri xmlns="http://www.onvif.org/ver10/media/wsdl">
  <StreamSetup xmlns="http://www.onvif.org/ver10/schema">
    <Stream>RTP-Unicast</Stream>
    <Transport><Protocol>RTSP</Protocol></Transport>
  </StreamSetup>
  <ProfileToken>%s</ProfileToken>
</GetStreamUri>`, escapedToken.String())

	respBytes, err := c.PostSOAPAuth(ctx, mediaXAddr, "http://www.onvif.org/ver10/media/wsdl/GetStreamUri", body, username, password)
	if err != nil {
		return "", err
	}
	return parseStreamURIStrict(respBytes)
}

// parseDeviceInfoStrict parses a GetDeviceInformation response, unlike the
// loose parsing in GetDeviceInformation: a genuine XML syntax error, or a
// response that never yields any of the expected fields, is reported as an
// error instead of silently returning a zero-value DeviceInfo. The
// authenticated test flow (internal/cameratest) needs to tell "device
// rejected the request" apart from "device returned garbage".
func parseDeviceInfoStrict(body []byte) (*DeviceInfo, error) {
	info := &DeviceInfo{}
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	found := false

	for {
		t, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("onvif: malformed GetDeviceInformation response: %w", err)
		}
		st, ok := t.(xml.StartElement)
		if !ok {
			continue
		}
		switch strings.ToLower(st.Name.Local) {
		case "manufacturer":
			var text string
			if dec.DecodeElement(&text, &st) == nil {
				info.Manufacturer = sanitizeText(text, 128)
				found = true
			}
		case "model":
			var text string
			if dec.DecodeElement(&text, &st) == nil {
				info.Model = sanitizeText(text, 128)
				found = true
			}
		case "firmwareversion":
			var text string
			if dec.DecodeElement(&text, &st) == nil {
				info.FirmwareVersion = sanitizeText(text, 128)
				found = true
			}
		case "serialnumber":
			var text string
			if dec.DecodeElement(&text, &st) == nil {
				info.SerialNumber = sanitizeText(text, 128)
				found = true
			}
		}
	}

	if !found {
		return nil, fmt.Errorf("onvif: GetDeviceInformation response missing expected fields")
	}
	return info, nil
}

// parseProfilesStrict is the strict counterpart to GetProfiles' parsing —
// see parseDeviceInfoStrict for why.
func parseProfilesStrict(body []byte) ([]MediaProfile, error) {
	var profiles []MediaProfile
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false

	var current *MediaProfile
	for {
		t, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("onvif: malformed GetProfiles response: %w", err)
		}
		switch elem := t.(type) {
		case xml.StartElement:
			local := strings.ToLower(elem.Name.Local)
			if local == "profiles" {
				token := ""
				for _, attr := range elem.Attr {
					if strings.EqualFold(attr.Name.Local, "token") {
						token = attr.Value
						break
					}
				}
				current = &MediaProfile{Token: token}
			} else if current != nil {
				switch local {
				case "name":
					var name string
					if dec.DecodeElement(&name, &elem) == nil {
						current.Name = sanitizeText(name, 64)
					}
				case "encoding":
					var enc string
					if dec.DecodeElement(&enc, &elem) == nil {
						current.Codec = sanitizeText(enc, 32)
					}
				case "width":
					var w int
					if dec.DecodeElement(&w, &elem) == nil {
						current.Width = w
					}
				case "height":
					var h int
					if dec.DecodeElement(&h, &elem) == nil {
						current.Height = h
					}
				case "frameratelimit":
					var fps float64
					if dec.DecodeElement(&fps, &elem) == nil {
						current.FPS = fps
					}
				}
			}
		case xml.EndElement:
			if strings.EqualFold(elem.Name.Local, "profiles") && current != nil {
				if current.Token != "" {
					nameLower := strings.ToLower(current.Name)
					if strings.Contains(nameLower, "sub") || strings.Contains(nameLower, "sec") || (current.Width > 0 && current.Width < 1280) {
						current.Role = StreamRoleSub
					} else {
						current.Role = StreamRoleMain
					}
					profiles = append(profiles, *current)
				}
				current = nil
			}
		}
	}

	if len(profiles) == 0 {
		return nil, fmt.Errorf("onvif: GetProfiles response contains no profiles")
	}
	return profiles, nil
}

// parseMediaXAddr extracts the Media service XAddr from a GetCapabilities
// response. Mirrors the parsing in the unauthenticated GetCapabilities.
func parseMediaXAddr(body []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	inMedia := false

	for {
		t, err := dec.Token()
		if err != nil {
			break
		}
		switch elem := t.(type) {
		case xml.StartElement:
			if strings.EqualFold(elem.Name.Local, "Media") {
				inMedia = true
			} else if inMedia && strings.EqualFold(elem.Name.Local, "XAddr") {
				var text string
				if dec.DecodeElement(&text, &elem) == nil {
					cleanURL := strings.TrimSpace(text)
					return cleanURL
				}
				inMedia = false
			}
		case xml.EndElement:
			if strings.EqualFold(elem.Name.Local, "Media") {
				inMedia = false
			}
		}
	}
	return ""
}

// parseStreamURIStrict is the strict counterpart to GetStreamUri's parsing
// — see parseDeviceInfoStrict for why. Like GetStreamUri, the returned URI
// is always sanitized to strip userinfo.
func parseStreamURIStrict(body []byte) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false

	for {
		t, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", fmt.Errorf("onvif: malformed GetStreamUri response: %w", err)
		}
		st, ok := t.(xml.StartElement)
		if !ok {
			continue
		}
		if strings.EqualFold(st.Name.Local, "Uri") {
			var uri string
			if dec.DecodeElement(&uri, &st) == nil {
				return sanitizeRTSPURI(uri), nil
			}
		}
	}

	return "", fmt.Errorf("onvif: GetStreamUri response missing Uri element")
}
