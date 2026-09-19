package onviftest

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/discovery/wsdiscovery"
)

// redirectConn wraps a real UDP PacketConn and rewrites every WriteTo
// destination to target's address, so the real wsdiscovery.Scanner can be
// pointed at a DiscoveryResponder over loopback without needing actual
// multicast support in the test environment.
type redirectConn struct {
	*net.UDPConn
	target *net.UDPAddr
}

func (r *redirectConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	return r.UDPConn.WriteToUDP(p, r.target)
}

func TestDiscoveryResponder_AnsweredByRealScanner(t *testing.T) {
	device := testDevice()
	soapSim := NewSimulator(device)
	defer soapSim.Close()

	responder, err := NewDiscoveryResponder(device, soapSim.DeviceXAddr())
	if err != nil {
		t.Fatalf("NewDiscoveryResponder: %v", err)
	}
	defer responder.Close()

	scanner := wsdiscovery.NewScanner(func(localIP net.IP) (wsdiscovery.PacketConn, error) {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			return nil, err
		}
		return &redirectConn{UDPConn: conn, target: responder.Addr()}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	scope := wsdiscovery.NetworkScope{IPv4: net.IPv4(127, 0, 0, 1)}
	candidates, err := scanner.ScanInterface(ctx, scope, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("ScanInterface: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	cand := candidates[0]
	if cand.EPRAddress != device.EPRAddress {
		t.Errorf("unexpected EPRAddress: %q", cand.EPRAddress)
	}
	if len(cand.XAddrs) != 1 || cand.XAddrs[0] != soapSim.DeviceXAddr() {
		t.Errorf("unexpected XAddrs: %v", cand.XAddrs)
	}
	if cand.Model != device.Model {
		t.Errorf("expected model extracted from scopes %q, got %q", device.Scopes, cand.Model)
	}
}
