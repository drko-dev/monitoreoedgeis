package agent

import (
	"bytes"
	"encoding/json"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/cloudsink"
	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

func edgeVideoTestDeps(t *testing.T, mode config.ProcessingMode, saasURL string) (*config.Config, credentials.Credentials, *health.Reporter) {
	t.Helper()
	cfg := testConfig(t)
	cfg.ProcessingMode = mode
	cfg.SaaSURL = saasURL
	cfg.AllowInsecureHTTP = true
	reporter := health.New("test", cfg, identity.Identity{}, platform.Info{})
	creds := credentials.Credentials{DeviceID: "d1", Credential: "secret-cred", Status: credentials.StatusEnrolled}
	return cfg, creds, reporter
}

// Sink selection per mode: edge gets the display-only video sink and never a
// CloudSink; cloud and hybrid keep the CloudSink and never the video sink, so
// no mode double-sends a frame.
func TestEdgeVideoSink_SelectionPerMode(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	for _, tc := range []struct {
		mode      config.ProcessingMode
		wantCloud bool
		wantVideo bool
	}{
		{config.ModeEdge, false, true},
		{config.ModeCloud, true, false},
		{config.ModeHybrid, true, false},
	} {
		cfg, creds, reporter := edgeVideoTestDeps(t, tc.mode, "https://example.invalid")
		cs := newCloudSink(cfg, creds, reporter, log)
		vs := newEdgeVideoSink(cfg, creds, reporter, log)
		if (cs != nil) != tc.wantCloud {
			t.Errorf("%s: cloud sink present = %v, want %v", tc.mode, cs != nil, tc.wantCloud)
		}
		if (vs != nil) != tc.wantVideo {
			t.Errorf("%s: edge video sink present = %v, want %v", tc.mode, vs != nil, tc.wantVideo)
		}
		if cs != nil && cs.Name() != "cloud" {
			t.Errorf("%s: cloud sink name = %q", tc.mode, cs.Name())
		}
		if vs != nil && vs.Name() != edgeVideoSinkName {
			t.Errorf("%s: video sink name = %q, want %q", tc.mode, vs.Name(), edgeVideoSinkName)
		}
	}
}

func TestEdgeVideoSink_NotEnrolledIsNil(t *testing.T) {
	cfg, _, reporter := edgeVideoTestDeps(t, config.ModeEdge, "https://example.invalid")
	if vs := newEdgeVideoSink(cfg, credentials.Credentials{}, reporter, slog.New(slog.DiscardHandler)); vs != nil {
		t.Fatal("edge video sink built without enrollment")
	}
}

// End to end over real HTTP: valid JPEG, display-only path, edge mode header,
// candidate key, failures counted under edge_video (never cloud), and the
// credential never leaks into the reported status.
func TestEdgeVideoSink_UploadsDisplayOnlyFrames(t *testing.T) {
	type got struct {
		path, mode, key, auth string
		body                  []byte
	}
	var (
		mu       sync.Mutex
		requests []got
		fail     bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, got{r.URL.Path, r.Header.Get("X-Processing-Mode"), r.Header.Get("X-Candidate-Key"), r.Header.Get("Authorization"), body})
		f := fail
		mu.Unlock()
		if f {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	cfg, creds, reporter := edgeVideoTestDeps(t, config.ModeEdge, srv.URL)
	sink := newEdgeVideoSink(cfg, creds, reporter, slog.New(slog.DiscardHandler))
	if sink == nil {
		t.Fatal("edge video sink is nil")
	}
	defer sink.(*cloudsink.CloudSink).Close()

	frame := processing.Frame{
		CandidateKey: "local:m4:synthetic", Seq: 1, Timestamp: time.Now(),
		OutputWidth: 4, OutputHeight: 2, Data: bytes.Repeat([]byte{0x80}, 4*2*3/2), // yuv420p
	}
	if err := sink.Route(frame); err != nil {
		t.Fatalf("route ok frame: %v", err)
	}
	mu.Lock()
	fail = true
	mu.Unlock()
	frame.Seq = 2
	_ = sink.Route(frame)

	waitFor(t, func() bool {
		st := reporter.Snapshot()
		return st.EdgeVideo != nil && st.EdgeVideo.FramesUploadSucceeded == 1 && st.EdgeVideo.FramesUploadFailed == 1
	})

	mu.Lock()
	defer mu.Unlock()
	first := requests[0]
	if first.path != transport.VideoFramesPath {
		t.Errorf("path = %q, want %q (never %q)", first.path, transport.VideoFramesPath, transport.FramesPath)
	}
	if first.mode != "edge" {
		t.Errorf("X-Processing-Mode = %q, want edge", first.mode)
	}
	if first.key != "local:m4:synthetic" {
		t.Errorf("X-Candidate-Key = %q", first.key)
	}
	if first.auth != "Bearer secret-cred" {
		t.Errorf("Authorization = %q", first.auth)
	}
	if _, err := jpeg.Decode(bytes.NewReader(first.body)); err != nil {
		t.Errorf("body is not a valid JPEG: %v", err)
	}
	if cfgImg, err := jpeg.DecodeConfig(bytes.NewReader(first.body)); err == nil && (cfgImg.Width != 4 || cfgImg.Height != 2) {
		t.Errorf("jpeg size = %dx%d", cfgImg.Width, cfgImg.Height)
	}

	st := reporter.Snapshot()
	if st.Cloud != nil {
		t.Error("edge video traffic was reported under cloud")
	}
	raw, _ := json.Marshal(st)
	if bytes.Contains(raw, []byte("secret-cred")) {
		t.Error("status leaks the device credential")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}
