package rtsp

import (
	"errors"
	"fmt"
	"net"
	"net/url"

	"github.com/drko-dev/monitoreoedgeis/internal/digestauth"
)

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
	return digestauth.ParseChallenge(header)
}

// buildDigestAuth computes the RFC 2617 Authorization header value.
// The plaintext password is consumed only to compute HA1 and never appears
// in logs, return values, or error strings.
func buildDigestAuth(username, password, method, uri string, params map[string]string) (string, error) {
	return digestauth.BuildAuthorization(username, password, method, uri, params)
}
