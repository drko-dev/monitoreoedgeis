package hikvision

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/discovery"
)

const inventoryXML = `<?xml version="1.0"?>
<StreamingChannelList xmlns="http://www.hikvision.com/ver20/XMLSchema">
  <StreamingChannel><id>101</id><channelName>Front</channelName><enabled>true</enabled><videoCodecType>H.264</videoCodecType><videoResolutionWidth>1920</videoResolutionWidth><videoResolutionHeight>1080</videoResolutionHeight></StreamingChannel>
  <StreamingChannel><id>102</id><channelName>Front</channelName><enabled>true</enabled><videoCodecType>H.264</videoCodecType><videoResolutionWidth>640</videoResolutionWidth><videoResolutionHeight>360</videoResolutionHeight></StreamingChannel>
  <StreamingChannel><id>301</id><channelName>Garage</channelName><enabled>false</enabled></StreamingChannel>
  <StreamingChannel><id>302</id><channelName>Garage</channelName><enabled>false</enabled></StreamingChannel>
</StreamingChannelList>`

func TestDiscoverChannelsGroupsNonConsecutiveChannels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != streamingChannelsPath {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(inventoryXML))
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	channels, err := client.DiscoverChannels(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 2 || channels[0].ID != 1 || channels[1].ID != 3 {
		t.Fatalf("unexpected channels: %+v", channels)
	}
	if channels[0].Availability != AvailabilityEnabled || len(channels[0].Streams) != 2 {
		t.Fatalf("expected enabled main/sub channel, got %+v", channels[0])
	}
	if channels[1].Availability != AvailabilityDisabled || len(channels[1].Streams) != 2 {
		t.Fatalf("expected disabled channel, got %+v", channels[1])
	}
}

func TestDiscoverChannelsRetriesDigestChallenge(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("WWW-Authenticate", `Digest realm="cam", nonce="nonce", qop="auth"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Digest ") {
			t.Fatalf("expected digest authorization, got %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(inventoryXML))
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, time.Second)
	if _, err := client.DiscoverChannels(context.Background(), "operator", "secret"); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestDiscoverChannelsClassifiesUnavailableUnauthorizedAndMalformed(t *testing.T) {
	for name, test := range map[string]struct {
		handler http.HandlerFunc
		want    error
	}{
		"unavailable":  {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }, ErrUnavailable},
		"unauthorized": {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }, ErrUnauthorized},
		"malformed":    {func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<nope>")) }, ErrMalformed},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(test.handler)
			defer srv.Close()
			client, _ := NewClient(srv.URL, time.Second)
			_, err := client.DiscoverChannels(context.Background(), "operator", "secret")
			if !strings.Contains(err.Error(), test.want.Error()) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestStreamURI(t *testing.T) {
	uri, err := StreamURI("192.168.1.80:554", 302)
	if err != nil || uri != "rtsp://192.168.1.80:554/ISAPI/Streaming/channels/302" {
		t.Fatalf("uri=%q err=%v", uri, err)
	}
}

func TestAdapterMapsChannelsAndConstructedStreamEvidence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(inventoryXML))
	}))
	defer srv.Close()
	adapter := NewAdapter(time.Second)
	channels, err := adapter.Discover(context.Background(), srv.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 2 || channels[1].ChannelNumber != 3 || channels[1].Availability != discovery.ChannelAvailabilityDisabled {
		t.Fatalf("unexpected normalized channels: %+v", channels)
	}
	if channels[0].Profiles[0].StreamURIOrigin != "hikvision_constructed" || channels[0].Profiles[0].StreamID != 101 {
		t.Fatalf("missing constructed URI evidence: %+v", channels[0].Profiles[0])
	}
}
