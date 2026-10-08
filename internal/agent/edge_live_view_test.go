package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/livevideo"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// fakeLiveSaaS answers /edge/video-frames with the configured viewer count
// for one camera, like the SaaS does, and records each frame's seq.
type fakeLiveSaaS struct {
	mu      sync.Mutex
	viewers int
	status  int
	seqs    []uint64
}

func (f *fakeLiveSaaS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != transport.VideoFramesPath {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	seq, _ := strconv.ParseUint(r.Header.Get("X-Frame-Seq"), 10, 64)
	f.seqs = append(f.seqs, seq)
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "accepted", "live_requested": f.viewers > 0, "live_viewers": f.viewers})
}

func (f *fakeLiveSaaS) set(viewers, status int) {
	f.mu.Lock()
	f.viewers, f.status = viewers, status
	f.mu.Unlock()
}

func (f *fakeLiveSaaS) seen(seq uint64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.seqs {
		if s == seq {
			return true
		}
	}
	return false
}

func liveFrame(seq uint64) processing.Frame {
	return processing.Frame{
		CandidateKey: "onvif:cam-1", Seq: seq, Timestamp: time.Now(),
		OutputWidth: 4, OutputHeight: 2, Data: bytes.Repeat([]byte{0x80}, 4*2*3/2),
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(what)
}

// Opening "En vivo" (viewers>0 in the SaaS answer) switches the camera to
// the live uplink; closing it falls back to preview after the idle timeout.
// Credentials never reach logs or /status.
func TestEdgeLiveView_ViewerLifecycle(t *testing.T) {
	saas := &fakeLiveSaaS{}
	srv := httptest.NewServer(saas)
	defer srv.Close()

	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg, creds, reporter := edgeVideoTestDeps(t, config.ModeEdge, srv.URL)
	live := livevideo.New(15, config.MaxLiveTargetFPS, 300*time.Millisecond, reporter.SetLiveVideoStatus)
	defer live.Close()
	preview := newEdgeVideoSink(cfg, creds, reporter, log, live, nil)
	defer preview.(interface{ Close() }).Close()

	// Modal closed: preview uploads, live is off.
	if err := preview.Route(liveFrame(1)); err != nil {
		t.Fatal(err)
	}
	if live.Active("onvif:cam-1") || live.Wants("onvif:cam-1", time.Now()) {
		t.Fatal("live active with the modal closed")
	}

	// Modal opened: the next preview answer carries the viewer.
	saas.set(1, 0)
	if err := preview.Route(liveFrame(2)); err != nil {
		t.Fatal(err)
	}
	if !live.Active("onvif:cam-1") {
		t.Fatal("viewer did not activate live")
	}
	if !live.Wants("onvif:cam-1", time.Now()) {
		t.Fatal("live tap does not want frames while a viewer is open")
	}
	live.Offer(liveFrame(100))
	eventually(t, "live frame not uploaded", func() bool { return saas.seen(100) })
	// Preview pauses for that camera while live carries it.
	if err := preview.Route(liveFrame(3)); err != nil {
		t.Fatal(err)
	}
	if saas.seen(3) {
		t.Fatal("preview kept uploading during live view")
	}
	eventually(t, "status not live", func() bool {
		st := reporter.Snapshot().LiveVideo
		return st != nil && st.Active && st.ActiveViewers == 1 && st.FramesSent >= 1
	})

	// Modal closed: the live answer reports 0 viewers; live lapses after idle.
	saas.set(0, 0)
	live.Offer(liveFrame(101))
	eventually(t, "live frame not uploaded", func() bool { return saas.seen(101) })
	if !live.Active("onvif:cam-1") {
		t.Fatal("live stopped before the idle timeout")
	}
	eventually(t, "live did not fall back to preview after idle timeout", func() bool {
		return !live.Active("onvif:cam-1")
	})
	if live.Wants("onvif:cam-1", time.Now()) { // the pipeline's next frame
		t.Fatal("live tap still wants frames after the idle timeout")
	}
	if err := preview.Route(liveFrame(4)); err != nil {
		t.Fatal(err)
	}
	if !saas.seen(4) {
		t.Fatalf("preview did not resume after live: seqs=%v active=%v", saas.seqs, live.Active("onvif:cam-1"))
	}
	if st := reporter.Snapshot().LiveVideo; st == nil || st.Active {
		t.Fatalf("live_video status after close = %+v", st)
	}

	// Failure paths log, but never the credential.
	saas.set(1, http.StatusServiceUnavailable)
	_ = preview.Route(liveFrame(5))
	statusJSON, _ := json.Marshal(reporter.Snapshot())
	for _, leak := range []string{creds.Credential, "rtsp://"} {
		if strings.Contains(logs.String(), leak) || strings.Contains(string(statusJSON), leak) {
			t.Fatalf("%q leaked into logs or /status", leak)
		}
	}
	if !strings.Contains(string(statusJSON), `"live_video"`) {
		t.Fatal("/status has no live_video block")
	}
}

// Edge mode registers exactly edge-vision + edge-video: Live View adds no
// router sink, no CloudSink, and no second inference path.
func TestEdgeLiveView_NoCloudSinkInEdgeMode(t *testing.T) {
	cfg, creds, reporter := edgeVideoTestDeps(t, config.ModeEdge, "https://example.invalid")
	live := livevideo.New(15, config.MaxLiveTargetFPS, time.Second, nil)
	defer live.Close()
	log := slog.New(slog.DiscardHandler)
	if cs := newCloudSink(cfg, creds, reporter, log); cs != nil {
		t.Fatalf("cloud sink %q built in edge mode", cs.Name())
	}
	vs := newEdgeVideoSink(cfg, creds, reporter, log, live, nil)
	if vs == nil || vs.Name() != edgeVideoSinkName {
		t.Fatalf("edge video sink = %v", vs)
	}
	for _, mode := range []config.ProcessingMode{config.ModeCloud, config.ModeHybrid} {
		cfg.ProcessingMode = mode
		if s := newEdgeVideoSink(cfg, creds, reporter, log, live, nil); s != nil {
			t.Fatal(fmt.Sprintf("%s: display-only video sink built", mode))
		}
	}
}
