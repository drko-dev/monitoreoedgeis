// Package wsdiscovery implements ONVIF WS-Discovery (Probe / ProbeMatches) over UDP multicast.
package wsdiscovery

import (
	"fmt"

	"github.com/drko-dev/monitoreoedgeis/internal/identity"
)

const (
	// MulticastGroup is the standard WS-Discovery IPv4 multicast group.
	MulticastGroup = "239.255.255.250"
	// MulticastPort is the standard WS-Discovery UDP port.
	MulticastPort = 3702
	// MulticastAddress is the combined destination address.
	MulticastAddress = "239.255.255.250:3702"
)

const (
	ActionProbe    = "http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe"
	AddressingTo   = "urn:schemas-xmlsoap-org:ws:2005:04:discovery"
	ONVIFTypeNVT   = "dn:NetworkVideoTransmitter"
	ONVIFWSDLNS    = "http://www.onvif.org/ver10/network/wsdl"
	SoapEnvelopeNS = "http://www.w3.org/2003/05/soap-envelope"
	WSAddressingNS = "http://schemas.xmlsoap.org/ws/2004/08/addressing"
	WSDiscoveryNS  = "http://schemas.xmlsoap.org/ws/2005/04/discovery"
)

// BuildProbe creates a WS-Discovery Probe envelope with a fresh UUIDv4 MessageID.
func BuildProbe() ([]byte, string, error) {
	msgUUID, err := identity.NewUUIDv4()
	if err != nil {
		return nil, "", fmt.Errorf("wsdiscovery: generate message id: %w", err)
	}

	payload := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="%s" xmlns:a="%s" xmlns:d="%s">
  <s:Header>
    <a:Action>%s</a:Action>
    <a:MessageID>urn:uuid:%s</a:MessageID>
    <a:To>%s</a:To>
  </s:Header>
  <s:Body>
    <d:Probe>
      <d:Types xmlns:dn="%s">%s</d:Types>
    </d:Probe>
  </s:Body>
</s:Envelope>`,
		SoapEnvelopeNS,
		WSAddressingNS,
		WSDiscoveryNS,
		ActionProbe,
		msgUUID,
		AddressingTo,
		ONVIFWSDLNS,
		ONVIFTypeNVT,
	)

	return []byte(payload), msgUUID, nil
}
