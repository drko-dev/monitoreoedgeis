package onviftest

import (
	"fmt"
	"net"
)

// DiscoveryResponder answers any WS-Discovery Probe datagram it receives
// with a ProbeMatch describing the Device fixture, over a real UDP socket.
// It is deliberately unicast (bound to 127.0.0.1) rather than joining the
// real multicast group, so tests stay deterministic and independent of the
// host's multicast configuration — callers redirect the client under test
// at this responder's Addr() instead of the real multicast address.
type DiscoveryResponder struct {
	conn   *net.UDPConn
	device Device
	xaddr  string
	done   chan struct{}
}

// NewDiscoveryResponder starts a UDP responder for the given device fixture
// on a dynamically assigned loopback port. xaddr is the ONVIF Device
// service URL advertised in every ProbeMatch (typically a Simulator's
// DeviceXAddr()).
func NewDiscoveryResponder(device Device, xaddr string) (*DiscoveryResponder, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return nil, fmt.Errorf("onviftest: listen udp: %w", err)
	}
	d := &DiscoveryResponder{conn: conn, device: device, xaddr: xaddr, done: make(chan struct{})}
	go d.serve()
	return d, nil
}

// Addr returns the UDP address the responder is listening on.
func (d *DiscoveryResponder) Addr() *net.UDPAddr { return d.conn.LocalAddr().(*net.UDPAddr) }

// Close stops the responder.
func (d *DiscoveryResponder) Close() {
	_ = d.conn.Close()
	<-d.done
}

func (d *DiscoveryResponder) serve() {
	defer close(d.done)
	buf := make([]byte, 16384)
	for {
		n, addr, err := d.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		resp := d.probeMatchXML()
		_, _ = d.conn.WriteToUDP([]byte(resp), addr)
	}
}

func (d *DiscoveryResponder) probeMatchXML() string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:wsa="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:wsdd="http://schemas.xmlsoap.org/ws/2005/04/discovery">
  <soap:Body>
    <wsdd:ProbeMatches>
      <wsdd:ProbeMatch>
        <wsa:EndpointReference>
          <wsa:Address>%s</wsa:Address>
        </wsa:EndpointReference>
        <wsdd:Types>%s</wsdd:Types>
        <wsdd:Scopes>%s</wsdd:Scopes>
        <wsdd:XAddrs>%s</wsdd:XAddrs>
        <wsdd:MetadataVersion>1</wsdd:MetadataVersion>
      </wsdd:ProbeMatch>
    </wsdd:ProbeMatches>
  </soap:Body>
</soap:Envelope>`,
		xmlEscape(d.device.EPRAddress), xmlEscape(d.device.Types), xmlEscape(d.device.Scopes), xmlEscape(d.xaddr))
}
