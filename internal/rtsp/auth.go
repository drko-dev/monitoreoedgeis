package rtsp

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

var digestParamRe = regexp.MustCompile(`(\w+)\s*=\s*(?:"([^"]*)"|([^,\s]+))`)

// ParseTarget splits a full rtsp:// URL into a dial address ("host:port")
// and a request path. Any userinfo in rawURL (rtsp://user:pass@host/...) is
// safely discarded.
//
// The query string is PRESERVED as part of the path. Many ONVIF StreamURIs
// carry the stream selector in the query rather than the path — e.g.
// "rtsp://10.0.0.20:554/stream?channel=1&subtype=0" — so dropping it would
// silently dial the wrong resource. client.go builds the request URI as
// "rtsp://" + addr + path, so the query must travel with the path for that
// concatenation to reproduce the camera's own URI.
//
// Only the plain "rtsp" scheme is supported. "rtsps://" is deliberately
// rejected: this client dials a plain TCP socket and implements no TLS
// transport, so accepting it would claim support that does not exist.
func ParseTarget(rawURL string) (addr, path string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("rtsp: parse RTSP URL: %w", err)
	}
	if u.Scheme != "rtsp" {
		return "", "", fmt.Errorf("rtsp: unsupported scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return "", "", errors.New("rtsp: RTSP URL missing host")
	}
	port := u.Port()
	if port == "" {
		port = "554"
	}
	uriPath := u.EscapedPath()
	if u.RawQuery != "" {
		uriPath += "?" + u.RawQuery
	}
	return net.JoinHostPort(host, port), uriPath, nil
}

// parseDigestChallenge extracts realm/nonce/qop/opaque from a WWW-Authenticate header.
func parseDigestChallenge(header string) (map[string]string, error) {
	if !strings.HasPrefix(strings.TrimSpace(header), "Digest") {
		return nil, errors.New("rtsp: WWW-Authenticate is not a Digest challenge")
	}
	params := map[string]string{}
	for _, m := range digestParamRe.FindAllStringSubmatch(header, -1) {
		v := m[2]
		if v == "" {
			v = m[3]
		}
		params[strings.ToLower(m[1])] = v
	}
	if params["realm"] == "" || params["nonce"] == "" {
		return nil, errors.New("rtsp: digest challenge missing realm or nonce")
	}
	return params, nil
}

// buildDigestAuth computes the RFC 2617 Authorization header value.
// The plaintext password is consumed only to compute HA1 and never appears
// in logs, return values, or error strings.
func buildDigestAuth(username, password, method, uri string, params map[string]string) (string, error) {
	realm := params["realm"]
	nonce := params["nonce"]
	qop := firstQop(params["qop"])

	ha1 := md5Hex(username + ":" + realm + ":" + password)
	ha2 := md5Hex(method + ":" + uri)

	var response, cnonce, nc string
	if qop != "" {
		cnonceBytes := make([]byte, 8)
		if _, err := rand.Read(cnonceBytes); err != nil {
			return "", fmt.Errorf("rtsp: generate cnonce: %w", err)
		}
		cnonce = hex.EncodeToString(cnonceBytes)
		nc = "00000001"
		response = md5Hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
	} else {
		response = md5Hex(ha1 + ":" + nonce + ":" + ha2)
	}

	var b strings.Builder
	fmt.Fprintf(&b, `Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`,
		username, realm, nonce, uri, response)
	if opaque := params["opaque"]; opaque != "" {
		fmt.Fprintf(&b, `, opaque="%s"`, opaque)
	}
	if qop != "" {
		fmt.Fprintf(&b, `, qop=%s, nc=%s, cnonce="%s"`, qop, nc, cnonce)
	}
	return b.String(), nil
}

func firstQop(qop string) string {
	if qop == "" {
		return ""
	}
	parts := strings.Split(qop, ",")
	return strings.TrimSpace(parts[0])
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}
