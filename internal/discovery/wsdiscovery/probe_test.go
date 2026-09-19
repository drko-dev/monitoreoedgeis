package wsdiscovery

import (
	"strings"
	"testing"
)

func TestBuildProbe_WellFormed(t *testing.T) {
	payload, msgID, err := BuildProbe()
	if err != nil {
		t.Fatalf("BuildProbe: %v", err)
	}
	if msgID == "" {
		t.Fatal("expected non-empty message id")
	}

	body := string(payload)
	for _, want := range []string{
		SoapEnvelopeNS,
		WSAddressingNS,
		WSDiscoveryNS,
		ActionProbe,
		AddressingTo,
		ONVIFWSDLNS,
		ONVIFTypeNVT,
		msgID,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected probe payload to contain %q, got: %s", want, body)
		}
	}

	matches, err := ParseProbeMatches(payload)
	if err != nil {
		t.Fatalf("ParseProbeMatches on a Probe payload should not error: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("a Probe payload has no ProbeMatch elements, got %d", len(matches))
	}
}

func TestBuildProbe_FreshMessageIDPerCall(t *testing.T) {
	_, id1, err := BuildProbe()
	if err != nil {
		t.Fatalf("BuildProbe #1: %v", err)
	}
	_, id2, err := BuildProbe()
	if err != nil {
		t.Fatalf("BuildProbe #2: %v", err)
	}
	if id1 == id2 {
		t.Fatalf("expected a fresh UUID per call, got the same id twice: %s", id1)
	}
}
