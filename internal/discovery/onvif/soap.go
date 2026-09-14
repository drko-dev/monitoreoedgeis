// Package onvif provides a lightweight standard library ONVIF SOAP client
// for passive and unauthenticated public device metadata enrichment (Milestone E).
package onvif

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrAuthRequired = errors.New("onvif: authentication required by device")
	ErrSoapFault    = errors.New("onvif: soap fault returned")
)

const (
	StreamRoleMain    = "main"
	StreamRoleSub     = "sub"
	StreamRoleUnknown = "unknown"
)

// VideoSource represents a video source channel parsed from ONVIF GetVideoSources.
type VideoSource struct {
	SourceToken  string
	Label        string
	Profiles     []MediaProfile
	Capabilities []string
}

// MediaProfile represents an ONVIF Media Profile.
type MediaProfile struct {
	Token     string
	Name      string
	Codec     string
	Width     int
	Height    int
	FPS       float64
	StreamURI string
	Role      string
}

// DeviceInfo holds hardware identity returned by GetDeviceInformation.
type DeviceInfo struct {
	Manufacturer    string
	Model           string
	FirmwareVersion string
	SerialNumber    string
}

// CredentialProvider supplies credentials for authenticated ONVIF operations.
// In Milestone E, only the Noop implementation is used — no camera passwords are tried.
type CredentialProvider interface {
	GetCredentials(xaddr string) (username, password string, ok bool)
}

// NoopCredentialProvider returns no credentials, preserving the unauthenticated boundary of Milestone E.
type NoopCredentialProvider struct{}

func (NoopCredentialProvider) GetCredentials(string) (string, string, bool) {
	return "", "", false
}

// Client performs scoped, bounded ONVIF SOAP queries.
type Client struct {
	httpClient *http.Client
	credProv   CredentialProvider
}

// NewClient creates an ONVIF SOAP client with strict bounded request timeouts.
func NewClient(timeout time.Duration, credProv CredentialProvider) *Client {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	if credProv == nil {
		credProv = NoopCredentialProvider{}
	}
	return &Client{
		httpClient: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// Refuse to follow redirects to prevent SSRF hopping
				return http.ErrUseLastResponse
			},
		},
		credProv: credProv,
	}
}

// SetTransport overrides the HTTP transport (used in tests).
func (c *Client) SetTransport(rt http.RoundTripper) {
	c.httpClient.Transport = rt
}

// PostSOAP executes one bounded SOAP POST request against an XAddr URL.
func (c *Client) PostSOAP(ctx context.Context, xaddr, action, bodyXML string) ([]byte, error) {
	u, err := url.Parse(xaddr)
	if err != nil {
		return nil, fmt.Errorf("onvif: invalid destination %q: %w", xaddr, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("onvif: invalid scheme %q: only http/https allowed", scheme)
	}
	if u.User != nil || strings.Contains(u.Host, "@") {
		return nil, fmt.Errorf("onvif: destination contains prohibited userinfo")
	}

	envelope := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>%s</s:Body>
</s:Envelope>`, bodyXML)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(envelope))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8; action=\""+action+"\"")
	req.Header.Set("User-Agent", "geocam-edge-discovery/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrAuthRequired
	}

	// Limit response size to 64 KB to prevent DoS from hostile devices
	body, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		return nil, err
	}

	// Detect SOAP fault auth failure
	if isAuthFault(body) {
		return nil, ErrAuthRequired
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%w: status %d", ErrSoapFault, resp.StatusCode)
	}

	return body, nil
}

// GetDeviceInformation queries the device for manufacturer, model, serial, and firmware.
func (c *Client) GetDeviceInformation(ctx context.Context, xaddr string) (*DeviceInfo, error) {
	body := `<GetDeviceInformation xmlns="http://www.onvif.org/ver10/device/wsdl"/>`
	respBytes, err := c.PostSOAP(ctx, xaddr, "http://www.onvif.org/ver10/device/wsdl/GetDeviceInformation", body)
	if err != nil {
		return nil, err
	}

	info := &DeviceInfo{}
	dec := xml.NewDecoder(bytes.NewReader(respBytes))
	dec.Strict = false

	for {
		t, err := dec.Token()
		if err != nil {
			break
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
			}
		case "model":
			var text string
			if dec.DecodeElement(&text, &st) == nil {
				info.Model = sanitizeText(text, 128)
			}
		case "firmwareversion":
			var text string
			if dec.DecodeElement(&text, &st) == nil {
				info.FirmwareVersion = sanitizeText(text, 128)
			}
		case "serialnumber":
			var text string
			if dec.DecodeElement(&text, &st) == nil {
				info.SerialNumber = sanitizeText(text, 128)
			}
		}
	}

	return info, nil
}

// GetCapabilities queries the device capabilities to discover Media Service XAddr.
func (c *Client) GetCapabilities(ctx context.Context, xaddr string) (mediaXAddr string, err error) {
	body := `<GetCapabilities xmlns="http://www.onvif.org/ver10/device/wsdl"><Category>All</Category></GetCapabilities>`
	respBytes, err := c.PostSOAP(ctx, xaddr, "http://www.onvif.org/ver10/device/wsdl/GetCapabilities", body)
	if err != nil {
		return "", err
	}

	dec := xml.NewDecoder(bytes.NewReader(respBytes))
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
					if u, err := url.Parse(cleanURL); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
						return cleanURL, nil
					}
				}
				inMedia = false
			}
		case xml.EndElement:
			if strings.EqualFold(elem.Name.Local, "Media") {
				inMedia = false
			}
		}
	}

	return "", nil
}

// GetVideoSources queries video sources to count channels (identifies NVR/DVR vs single camera).
func (c *Client) GetVideoSources(ctx context.Context, mediaXAddr string) ([]VideoSource, error) {
	body := `<GetVideoSources xmlns="http://www.onvif.org/ver10/media/wsdl"/>`
	respBytes, err := c.PostSOAP(ctx, mediaXAddr, "http://www.onvif.org/ver10/media/wsdl/GetVideoSources", body)
	if err != nil {
		return nil, err
	}

	var sources []VideoSource
	dec := xml.NewDecoder(bytes.NewReader(respBytes))
	dec.Strict = false

	for {
		t, err := dec.Token()
		if err != nil {
			break
		}
		st, ok := t.(xml.StartElement)
		if !ok {
			continue
		}
		if strings.EqualFold(st.Name.Local, "VideoSources") {
			token := ""
			for _, attr := range st.Attr {
				if strings.EqualFold(attr.Name.Local, "token") {
					token = attr.Value
					break
				}
			}
			if token != "" {
				sources = append(sources, VideoSource{
					SourceToken: token,
					Label:       fmt.Sprintf("Channel %d", len(sources)+1),
				})
			}
		}
	}

	return sources, nil
}

// GetProfiles queries media profiles on the media service.
func (c *Client) GetProfiles(ctx context.Context, mediaXAddr string) ([]MediaProfile, error) {
	body := `<GetProfiles xmlns="http://www.onvif.org/ver10/media/wsdl"/>`
	respBytes, err := c.PostSOAP(ctx, mediaXAddr, "http://www.onvif.org/ver10/media/wsdl/GetProfiles", body)
	if err != nil {
		return nil, err
	}

	var profiles []MediaProfile
	dec := xml.NewDecoder(bytes.NewReader(respBytes))
	dec.Strict = false

	var current *MediaProfile
	for {
		t, err := dec.Token()
		if err != nil {
			break
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

	return profiles, nil
}

// GetStreamUri queries the RTSP stream URI for a media profile.
// CRITICAL RULE: The returned URI is always sanitized to strip userinfo.
// No video connection or decode is initiated.
func (c *Client) GetStreamUri(ctx context.Context, mediaXAddr, profileToken string) (string, error) {
	body := fmt.Sprintf(`<GetStreamUri xmlns="http://www.onvif.org/ver10/media/wsdl">
  <StreamSetup xmlns="http://www.onvif.org/ver10/schema">
    <Stream>RTP-Unicast</Stream>
    <Transport><Protocol>RTSP</Protocol></Transport>
  </StreamSetup>
  <ProfileToken>%s</ProfileToken>
</GetStreamUri>`, profileToken)

	respBytes, err := c.PostSOAP(ctx, mediaXAddr, "http://www.onvif.org/ver10/media/wsdl/GetStreamUri", body)
	if err != nil {
		return "", err
	}

	dec := xml.NewDecoder(bytes.NewReader(respBytes))
	dec.Strict = false

	for {
		t, err := dec.Token()
		if err != nil {
			break
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

	return "", nil
}

func isAuthFault(body []byte) bool {
	lower := bytes.ToLower(body)
	if !bytes.Contains(lower, []byte("fault")) {
		return false
	}
	return bytes.Contains(lower, []byte("notauthorized")) ||
		bytes.Contains(lower, []byte("failedauthentication")) ||
		bytes.Contains(lower, []byte("unauthorized")) ||
		bytes.Contains(lower, []byte("security"))
}

func sanitizeText(s string, maxLen int) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= 0x20 && r != 0x7F {
			b.WriteRune(r)
		}
	}
	trimmed := strings.TrimSpace(b.String())
	fields := strings.Fields(trimmed)
	collapsed := strings.Join(fields, " ")
	if maxLen > 0 && len(collapsed) > maxLen {
		return collapsed[:maxLen]
	}
	return collapsed
}

func sanitizeRTSPURI(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	schemeEnd := strings.Index(raw, "://")
	if schemeEnd == -1 {
		return raw
	}
	prefix := raw[:schemeEnd+3]
	remainder := raw[schemeEnd+3:]

	pathStart := strings.Index(remainder, "/")
	authority := remainder
	path := ""
	if pathStart != -1 {
		authority = remainder[:pathStart]
		path = remainder[pathStart:]
	}

	atIdx := strings.LastIndex(authority, "@")
	if atIdx != -1 {
		authority = authority[atIdx+1:]
	}

	return prefix + authority + path
}
