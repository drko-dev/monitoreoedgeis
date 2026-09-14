package wsdiscovery

import (
	"strings"
	"testing"
)

func TestParseProbeMatches_Standard(t *testing.T) {
	xmlData := []byte(`<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope"
               xmlns:wsa="http://schemas.xmlsoap.org/ws/2004/08/addressing"
               xmlns:wsdd="http://schemas.xmlsoap.org/ws/2005/04/discovery">
  <soap:Header>
    <wsa:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/ProbeMatches</wsa:Action>
    <wsa:MessageID>urn:uuid:12345678-1234-1234-1234-123456789abc</wsa:MessageID>
  </soap:Header>
  <soap:Body>
    <wsdd:ProbeMatches>
      <wsdd:ProbeMatch>
        <wsa:EndpointReference>
          <wsa:Address>urn:uuid:2419d68a-2dd2-21b2-a205-001234567890</wsa:Address>
        </wsa:EndpointReference>
        <wsdd:Types>dn:NetworkVideoTransmitter</wsdd:Types>
        <wsdd:Scopes>onvif://www.onvif.org/type/video_encoder onvif://www.onvif.org/hardware/Model123</wsdd:Scopes>
        <wsdd:XAddrs>http://192.168.1.50:80/onvif/device_service http://192.168.1.50:8080/onvif/device_service</wsdd:XAddrs>
        <wsdd:MetadataVersion>1</wsdd:MetadataVersion>
      </wsdd:ProbeMatch>
    </wsdd:ProbeMatches>
  </soap:Body>
</soap:Envelope>`)

	matches, err := ParseProbeMatches(xmlData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}

	m := matches[0]
	if m.EPRAddress != "urn:uuid:2419d68a-2dd2-21b2-a205-001234567890" {
		t.Errorf("unexpected EPR: %s", m.EPRAddress)
	}
	if m.Types != "dn:NetworkVideoTransmitter" {
		t.Errorf("unexpected Types: %s", m.Types)
	}
	if len(m.XAddrs) != 2 {
		t.Fatalf("expected 2 XAddrs, got %d", len(m.XAddrs))
	}
	if m.XAddrs[0] != "http://192.168.1.50:80/onvif/device_service" {
		t.Errorf("unexpected XAddr[0]: %s", m.XAddrs[0])
	}
}

func TestParseProbeMatches_OversizedRejected(t *testing.T) {
	oversized := make([]byte, MaxDatagramBytes+1)
	for i := range oversized {
		oversized[i] = 'a'
	}
	_, err := ParseProbeMatches(oversized)
	if err != ErrOversizedDatagram {
		t.Errorf("expected ErrOversizedDatagram, got %v", err)
	}
}

func TestParseProbeMatches_DTDRejected(t *testing.T) {
	xmlData := []byte(`<!DOCTYPE test [ <!ENTITY xxe SYSTEM "file:///etc/passwd"> ]><root>&xxe;</root>`)
	_, err := ParseProbeMatches(xmlData)
	if err != ErrDTDForbidden {
		t.Errorf("expected ErrDTDForbidden, got %v", err)
	}
}

func TestParseProbeMatches_MalformedNoPanic(t *testing.T) {
	malformed := []byte(`<soap:Envelope><soap:Body><wsdd:ProbeMatch><wsa:Address>unclosed`)
	matches, err := ParseProbeMatches(malformed)
	if err != nil {
		t.Logf("returned non-fatal error: %v", err)
	}
	_ = matches
}

func TestBuildProbe_ValidStructure(t *testing.T) {
	data, msgID, err := BuildProbe()
	if err != nil {
		t.Fatalf("BuildProbe failed: %v", err)
	}
	if msgID == "" {
		t.Fatal("empty msgID")
	}
	str := string(data)
	if !strings.Contains(str, msgID) {
		t.Errorf("probe does not contain msgID %s", msgID)
	}
	if !strings.Contains(str, ONVIFTypeNVT) {
		t.Errorf("probe does not contain ONVIFTypeNVT %s", ONVIFTypeNVT)
	}
}
