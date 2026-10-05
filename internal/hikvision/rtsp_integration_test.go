package hikvision

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsptest"
)

func TestDiscoveredStreamNegotiatesWithRTSPSimulator(t *testing.T) {
	inventory := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != streamingChannelsPath {
			t.Errorf("unexpected ISAPI request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(inventoryXML))
	}))
	defer inventory.Close()

	adapter := NewAdapter(time.Second)
	sources, err := adapter.Discover(context.Background(), inventory.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	stream := sources[0].Profiles[0]
	if stream.StreamID != 101 || !strings.HasSuffix(stream.StreamURI, "/ISAPI/Streaming/channels/101") {
		t.Fatalf("unexpected discovered stream: %+v", stream)
	}

	sim, err := rtsptest.NewSimulator(rtsptest.Options{
		Username: "operator", Password: "synthetic-secret", AutoPacketCount: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sim.Close()

	// The simulator listens on an ephemeral port. Preserve the adapter's
	// discovered stream ID and Hikvision path while substituting only that
	// local endpoint; this is not a physical recorder compatibility test.
	uri, err := url.Parse(stream.StreamURI)
	if err != nil {
		t.Fatal(err)
	}
	if uri.Path != "/ISAPI/Streaming/channels/101" {
		t.Fatalf("adapter stream path = %q", uri.Path)
	}
	uri.Host = sim.Addr()
	addr, path, err := rtsp.ParseTarget(uri.String())
	if err != nil || addr != sim.Addr() || path != "/ISAPI/Streaming/channels/101" {
		t.Fatalf("parsed simulator target addr=%q path=%q err=%v", addr, path, err)
	}
	session, err := rtsp.Dial(context.Background(), addr, path, "operator", "synthetic-secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Teardown(time.Second)
	if session.Codec() != "H264" {
		t.Fatalf("negotiated codec = %q, want H264", session.Codec())
	}
	channel, packet, err := session.ReadPacket(time.Second)
	if err != nil || channel != 0 || len(packet) == 0 {
		t.Fatalf("first RTP packet channel=%d bytes=%d err=%v", channel, len(packet), err)
	}
}
