package wsdiscovery

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

type fakePacketConn struct {
	mu        sync.Mutex
	packets   [][]byte
	index     int
	writtenTo []byte
	closed    bool
}

func (f *fakePacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writtenTo = append(f.writtenTo, p...)
	return len(p), nil
}

func (f *fakePacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.index >= len(f.packets) {
		return 0, nil, io.EOF
	}
	pkt := f.packets[f.index]
	f.index++
	copy(p, pkt)
	return len(pkt), &net.UDPAddr{IP: net.ParseIP("192.168.1.50"), Port: 3702}, nil
}

func (f *fakePacketConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (f *fakePacketConn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func TestScanInterface_Success(t *testing.T) {
	validProbeMatch := `<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope"
               xmlns:wsa="http://schemas.xmlsoap.org/ws/2004/08/addressing"
               xmlns:wsdd="http://schemas.xmlsoap.org/ws/2005/04/discovery">
  <soap:Body>
    <wsdd:ProbeMatches>
      <wsdd:ProbeMatch>
        <wsa:EndpointReference>
          <wsa:Address>urn:uuid:11111111-2222-3333-4444-555555555555</wsa:Address>
        </wsa:EndpointReference>
        <wsdd:Types>dn:NetworkVideoTransmitter</wsdd:Types>
        <wsdd:Scopes>onvif://www.onvif.org/hardware/IPC-TEST-01 onvif://www.onvif.org/type/video_encoder</wsdd:Scopes>
        <wsdd:XAddrs>http://192.168.1.100:80/onvif/device_service</wsdd:XAddrs>
      </wsdd:ProbeMatch>
    </wsdd:ProbeMatches>
  </soap:Body>
</soap:Envelope>`

	fakeConn := &fakePacketConn{
		packets: [][]byte{[]byte(validProbeMatch)},
	}

	scanner := NewScanner(func(ip net.IP) (PacketConn, error) {
		return fakeConn, nil
	})

	scope := NetworkScope{
		Interface: net.Interface{Name: "eth0"},
		IPv4:      net.ParseIP("192.168.1.10"),
	}

	cands, err := scanner.ScanInterface(context.Background(), scope, 1*time.Second)
	if err != nil {
		t.Fatalf("ScanInterface failed: %v", err)
	}

	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}

	c := cands[0]
	if c.EPRAddress != "urn:uuid:11111111-2222-3333-4444-555555555555" {
		t.Errorf("unexpected EPR: %s", c.EPRAddress)
	}
	if len(c.XAddrs) != 1 || c.XAddrs[0] != "http://192.168.1.100:80/onvif/device_service" {
		t.Errorf("unexpected XAddrs: %v", c.XAddrs)
	}
	if c.Model != "IPC-TEST-01" {
		t.Errorf("unexpected model: %s", c.Model)
	}
	if !fakeConn.closed {
		t.Errorf("expected socket to be closed")
	}
}

func TestScanInterface_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	fakeConn := &fakePacketConn{}
	scanner := NewScanner(func(ip net.IP) (PacketConn, error) {
		return fakeConn, nil
	})

	scope := NetworkScope{
		Interface: net.Interface{Name: "eth0"},
		IPv4:      net.ParseIP("192.168.1.10"),
	}

	_, err := scanner.ScanInterface(ctx, scope, 1*time.Second)
	if err == nil || err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}
