package wsdiscovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

const (
	DefaultScanTimeout   = 4 * time.Second
	MaxDatagramsPerScan  = 200
	MaxCandidatesPerScan = 64
)

// NetworkScope pairs an active network interface with its primary IPv4 address.
type NetworkScope struct {
	Interface net.Interface
	IPv4      net.IP
}

// PacketConn abstracts UDP sockets for deterministic testing without physical network.
type PacketConn interface {
	WriteTo(p []byte, addr net.Addr) (n int, err error)
	ReadFrom(p []byte) (n int, addr net.Addr, err error)
	SetReadDeadline(t time.Time) error
	Close() error
}

// SocketFactory builds a PacketConn bound to the given local IP address.
type SocketFactory func(localIP net.IP) (PacketConn, error)

// DefaultSocketFactory creates a standard Go UDP socket bound to localIP on an ephemeral port.
func DefaultSocketFactory(localIP net.IP) (PacketConn, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: localIP, Port: 0})
	if err != nil {
		return nil, fmt.Errorf("wsdiscovery: socket bind: %w", err)
	}
	return conn, nil
}

// DiscoveredRawCandidate holds an unvalidated candidate parsed from a WS-Discovery ProbeMatch.
type DiscoveredRawCandidate struct {
	EPRAddress string
	Types      string
	Scopes     string
	XAddrs     []string
	Model      string
}

// Scanner performs WS-Discovery probes over local network interfaces.
type Scanner struct {
	socketFactory SocketFactory
}

// NewScanner creates a new WS-Discovery scanner.
func NewScanner(factory SocketFactory) *Scanner {
	if factory == nil {
		factory = DefaultSocketFactory
	}
	return &Scanner{socketFactory: factory}
}

// ScanInterface sends an ONVIF Probe on scope and collects responses within timeout.
func (s *Scanner) ScanInterface(ctx context.Context, scope NetworkScope, timeout time.Duration) ([]DiscoveredRawCandidate, error) {
	if timeout <= 0 {
		timeout = DefaultScanTimeout
	}

	probeData, _, err := BuildProbe()
	if err != nil {
		return nil, fmt.Errorf("build probe: %w", err)
	}

	sock, err := s.socketFactory(scope.IPv4)
	if err != nil {
		return nil, fmt.Errorf("socket factory on %s (%s): %w", scope.Interface.Name, scope.IPv4, err)
	}
	defer sock.Close()

	destAddr := &net.UDPAddr{
		IP:   net.ParseIP(MulticastGroup),
		Port: MulticastPort,
	}

	if _, err := sock.WriteTo(probeData, destAddr); err != nil {
		return nil, fmt.Errorf("send probe to %s: %w", destAddr, err)
	}

	deadline := time.Now().Add(timeout)
	buf := make([]byte, MaxDatagramBytes+1)

	var candidates []DiscoveredRawCandidate
	datagrams := 0

	for datagrams < MaxDatagramsPerScan && len(candidates) < MaxCandidatesPerScan {
		select {
		case <-ctx.Done():
			return candidates, ctx.Err()
		default:
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}

		chunkTimeout := remaining
		if chunkTimeout > 1*time.Second {
			chunkTimeout = 1 * time.Second
		}
		_ = sock.SetReadDeadline(time.Now().Add(chunkTimeout))

		n, _, err := sock.ReadFrom(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				if time.Now().After(deadline) {
					break
				}
				continue
			}
			// Connection closed or permanent error
			break
		}

		datagrams++
		if n > MaxDatagramBytes {
			// Discard oversized datagrams without truncation
			continue
		}

		matches, err := ParseProbeMatches(buf[:n])
		if err != nil {
			continue
		}

		for _, match := range matches {
			if len(candidates) >= MaxCandidatesPerScan {
				break
			}

			model := extractModelFromScopes(match.Scopes)

			candidates = append(candidates, DiscoveredRawCandidate{
				EPRAddress: match.EPRAddress,
				Types:      match.Types,
				Scopes:     match.Scopes,
				XAddrs:     match.XAddrs,
				Model:      model,
			})
		}
	}

	return candidates, nil
}

// extractModelFromScopes extracts the hardware model from onvif://www.onvif.org/hardware/<model> scopes.
func extractModelFromScopes(scopes string) string {
	for _, tok := range strings.Fields(scopes) {
		if strings.HasPrefix(tok, "onvif://www.onvif.org/hardware/") {
			model := strings.TrimPrefix(tok, "onvif://www.onvif.org/hardware/")
			return sanitizeText(model, MaxMetadataLength)
		}
	}
	return ""
}
