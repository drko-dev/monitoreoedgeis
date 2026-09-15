package discovery

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Operational bounds and security limits.
// Directly mirrors and enforces the proven limits from legacy core.py DiscoveryLimits.
const (
	DefaultScanTimeout         = 4 * time.Second
	MaxDatagramsPerScan        = 200
	MaxCandidatesPerScan       = 64
	MaxDatagramBytes           = 16384 // 16 KB
	MaxProbeMatchesPerDatagram = 16
	MaxXAddrsPerMatch          = 8
	MaxURLLength               = 512
	MaxHostLength              = 255
	MaxPathLength              = 512
	MaxEPRLength               = 512
	MaxTypesLength             = 512
	MaxScopesLength            = 2048
	MaxMetadataLength          = 128
	MaxInventoryDevices        = 512            // Hard cap on total devices held across scans
	DeviceTTL                  = 24 * time.Hour // Devices not seen within this window are evicted
)

var (
	ErrXAddrTooLong         = errors.New("security: XAddr exceeds maximum length")
	ErrXAddrControlChars    = errors.New("security: XAddr contains illegal control characters")
	ErrXAddrInvalidScheme   = errors.New("security: XAddr scheme must be http or https")
	ErrXAddrUserinfoDenied  = errors.New("security: XAddr must not contain userinfo")
	ErrXAddrInvalidPort     = errors.New("security: XAddr port out of valid range (1-65535)")
	ErrXAddrHostTooLong     = errors.New("security: XAddr host exceeds maximum length")
	ErrXAddrDisallowedHost  = errors.New("security: XAddr host is not an allowed private IPv4 destination")
	ErrXAddrLoopbackDenied  = errors.New("security: loopback addresses are prohibited")
	ErrXAddrCloudMetaDenied = errors.New("security: cloud metadata endpoint is prohibited")
)

// Allowed private IPv4 blocks according to RFC 1918 and RFC 3927 (link-local).
var allowedPrivateNets = []*net.IPNet{
	mustParseCIDR("10.0.0.0/8"),
	mustParseCIDR("172.16.0.0/12"),
	mustParseCIDR("192.168.0.0/16"),
	mustParseCIDR("169.254.0.0/16"),
}

func mustParseCIDR(s string) *net.IPNet {
	_, ipnet, err := net.ParseCIDR(s)
	if err != nil {
		panic(fmt.Sprintf("invalid static CIDR %q: %v", s, err))
	}
	return ipnet
}

// Credential-shaped tokens to purge from announced scopes (anti-leak protection).
var credentialPattern = regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|credential|apikey|api[_-]key|authorization)`)

// ValidateXAddr inspects and validates an ONVIF XAddr URL.
// Fail-closed rules:
//  1. Total length <= MaxURLLength.
//  2. No ASCII control characters (0x00-0x1F, 0x7F).
//  3. Scheme strictly "http" or "https".
//  4. No userinfo (@) allowed under any circumstances.
//  5. Port must be 1..65535 (default 80 for http, 443 for https).
//  6. Destination must be a valid private IPv4 address (10/8, 172.16/12, 192.168/16, 169.254/16).
//  7. Loopback and cloud metadata service (169.254.169.254) are rejected.
func ValidateXAddr(raw string) (*url.URL, int, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, 0, errors.New("security: empty XAddr")
	}
	if len(raw) > MaxURLLength {
		return nil, 0, ErrXAddrTooLong
	}

	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if ch < 0x20 || ch == 0x7F {
			return nil, 0, ErrXAddrControlChars
		}
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, 0, fmt.Errorf("security: malformed XAddr: %w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, 0, ErrXAddrInvalidScheme
	}

	// Reject userinfo
	if u.User != nil || strings.Contains(u.Host, "@") {
		return nil, 0, ErrXAddrUserinfoDenied
	}

	host := u.Hostname()
	if len(host) == 0 || len(host) > MaxHostLength {
		return nil, 0, ErrXAddrHostTooLong
	}

	port := 80
	if scheme == "https" {
		port = 443
	}
	if rawPort := u.Port(); rawPort != "" {
		p, err := strconv.Atoi(rawPort)
		if err != nil || p < 1 || p > 65535 {
			return nil, 0, ErrXAddrInvalidPort
		}
		port = p
	}

	// Validate target IP address against allowed private ranges
	ip := net.ParseIP(host)
	if ip == nil {
		// Attempt literal without brackets if IPv6
		cleanHost := strings.Trim(host, "[]")
		ip = net.ParseIP(cleanHost)
	}

	if ip == nil {
		// Hostname provided: reject non-private or unqualified hostnames for local safety
		return nil, 0, fmt.Errorf("%w: host %q is not an IP literal", ErrXAddrDisallowedHost, host)
	}

	// Reject loopback (127.0.0.1, ::1)
	if ip.IsLoopback() {
		return nil, 0, ErrXAddrLoopbackDenied
	}

	// Reject cloud metadata endpoints specifically. The rest of 169.254.0.0/16
	// (RFC 3927 link-local) stays allowed because self-assigned cameras that
	// failed DHCP legitimately announce addresses in that range during
	// initial discovery — see docs/PROJECT_STATUS.md. Only the fixed IPs
	// actually used by cloud metadata services are denylisted:
	//  - 169.254.169.254: AWS EC2/ECS, GCP, Azure, DigitalOcean, Oracle Cloud
	//  - 169.254.170.2:   AWS ECS/Fargate task metadata (v2/v3/v4)
	switch ip.String() {
	case "169.254.169.254", "169.254.170.2":
		return nil, 0, ErrXAddrCloudMetaDenied
	}

	// Must be IPv4 private network
	ipv4 := ip.To4()
	if ipv4 == nil {
		return nil, 0, fmt.Errorf("%w: IPv6 not permitted for residential discovery", ErrXAddrDisallowedHost)
	}

	isAllowed := false
	for _, network := range allowedPrivateNets {
		if network.Contains(ipv4) {
			isAllowed = true
			break
		}
	}
	if !isAllowed {
		return nil, 0, fmt.Errorf("%w: %s is not in RFC 1918/3927 private range", ErrXAddrDisallowedHost, ipv4.String())
	}

	return u, port, nil
}

// SanitizeText removes non-printable / control characters, collapses whitespace,
// and truncates string to maxLen.
func SanitizeText(s string, maxLen int) string {
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
	// Collapse consecutive whitespace
	fields := strings.Fields(trimmed)
	collapsed := strings.Join(fields, " ")
	if maxLen > 0 && len(collapsed) > maxLen {
		return collapsed[:maxLen]
	}
	return collapsed
}

// CleanScopes cleans the raw scopes string, purges any credential-like tokens,
// and returns both the sanitized string and slice of individual scope tokens.
func CleanScopes(raw string) (string, []string) {
	sanitized := SanitizeText(raw, MaxScopesLength)
	if sanitized == "" {
		return "", nil
	}

	rawTokens := strings.Fields(sanitized)
	cleanTokens := make([]string, 0, len(rawTokens))
	for _, tok := range rawTokens {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		// If token contains credentials, strip it
		if credentialPattern.MatchString(tok) {
			continue
		}
		cleanTokens = append(cleanTokens, tok)
	}

	return strings.Join(cleanTokens, " "), cleanTokens
}

// SanitizeRTSPURI removes userinfo (username:password@) from RTSP URIs.
// Never persist, log, or report passwords in RTSP URIs.
func SanitizeRTSPURI(raw string) string {
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
