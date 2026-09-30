package discovery

import (
	"context"
	"testing"
)

type recorderAdapterStub struct {
	channels []VideoSource
	err      error
	endpoint string
	username string
	password string
}

func (a *recorderAdapterStub) Vendor() string { return "Hikvision" }
func (a *recorderAdapterStub) Discover(_ context.Context, endpoint, username, password string) ([]VideoSource, error) {
	a.endpoint, a.username, a.password = endpoint, username, password
	return a.channels, a.err
}

func TestRecorderAdapterUsesStableIdentityCredentialsAndMergesChannel(t *testing.T) {
	adapter := &recorderAdapterStub{channels: []VideoSource{{
		SourceToken: "hikvision:channel:4", ChannelNumber: 4,
		Availability: ChannelAvailabilityEnabled,
		Profiles:     []MediaProfile{{Token: "main-401", StreamID: 401, StreamURI: "rtsp://10.0.0.4/ISAPI/Streaming/channels/401"}},
	}}}
	engine := NewEngine(nil, nil, nil, nil, 0, nil)
	engine.SetRecorderAdapter(adapter)
	engine.SetCredentialResolver(func(key string) (string, string, bool) {
		if key != "epr:recorder-uuid" {
			t.Fatalf("credential key = %q", key)
		}
		return "operator", "secret", true
	})
	dev := engine.enrichWithRecorderAdapter(context.Background(), DiscoveredDevice{
		StableIdentity: "epr:recorder-uuid", Manufacturer: "Hikvision", XAddr: "http://10.0.0.4/ISAPI",
		VideoSources: []VideoSource{{SourceToken: "4", Profiles: []MediaProfile{{Token: "onvif", StreamURI: "rtsp://10.0.0.4/ISAPI/Streaming/channels/401"}}}},
	})
	if adapter.endpoint != "http://10.0.0.4/ISAPI" || adapter.username != "operator" || adapter.password != "secret" {
		t.Fatalf("adapter received endpoint/credentials %q/%q/%q", adapter.endpoint, adapter.username, adapter.password)
	}
	if len(dev.VideoSources) != 1 || dev.VideoSources[0].ChannelNumber != 4 || dev.VideoSources[0].Availability != ChannelAvailabilityEnabled || len(dev.VideoSources[0].Profiles) != 2 {
		t.Fatalf("merged sources = %+v", dev.VideoSources)
	}
}

func TestRecorderAdapterAuthFailureDoesNotExposeCredentials(t *testing.T) {
	adapter := &recorderAdapterStub{err: ErrRecorderAuthRequired}
	engine := NewEngine(nil, nil, nil, nil, 0, nil)
	engine.SetRecorderAdapter(adapter)
	engine.SetCredentialResolver(func(string) (string, string, bool) { return "operator", "secret", true })
	dev := engine.enrichWithRecorderAdapter(context.Background(), DiscoveredDevice{StableIdentity: "recorder", Manufacturer: "Hikvision"})
	if !dev.AuthRequired {
		t.Fatal("authentication rejection did not mark recorder as requiring authentication")
	}
	if adapter.password != "secret" {
		t.Fatalf("credential resolver value was not passed to adapter")
	}
}

func TestRecorderAdapterIgnoresOtherVendors(t *testing.T) {
	adapter := &recorderAdapterStub{}
	engine := NewEngine(nil, nil, nil, nil, 0, nil)
	engine.SetRecorderAdapter(adapter)
	dev := engine.enrichWithRecorderAdapter(context.Background(), DiscoveredDevice{Manufacturer: "Other"})
	if adapter.endpoint != "" || dev.AuthRequired {
		t.Fatalf("other vendor was unexpectedly processed: %+v", dev)
	}
}
