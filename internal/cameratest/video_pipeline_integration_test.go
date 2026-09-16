//go:build integration

// Real-camera acceptance test for Hito H (video pipeline). Runs the full
// chain — RTSP -> RTP -> depacketize -> H.264 decode (real ffmpeg
// subprocess) -> sample -> resize -> bounded ring buffer -> route to a
// debug sink — against the live Tapo TC70 used throughout this project's
// hardware validation, with zero YOLO/PyTorch/Vision-Worker/Cloud
// involvement (H11).
//
// Skipped by default (requires the "integration" build tag) and skipped
// again if TAPO_ONVIF_USER / TAPO_ONVIF_PASS are unset. Credentials are
// read only via os.Getenv and are never logged, printed, or written
// anywhere — only counts, dimensions, booleans, and state strings are.
//
//	source ~/.tapo_test_creds.env   # sets TAPO_ONVIF_USER / TAPO_ONVIF_PASS
//	go test -tags integration ./internal/cameratest/... -run TestIntegration_VideoPipeline -v -timeout 90s
package cameratest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/cameracreds"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery/onvif"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsptest"
)

type capturingHealthSink struct {
	last processing.VideoPipelineSummary
}

func (c *capturingHealthSink) SetVideoPipeline(s processing.VideoPipelineSummary) { c.last = s }

func TestIntegration_VideoPipeline(t *testing.T) {
	user := os.Getenv("TAPO_ONVIF_USER")
	pass := os.Getenv("TAPO_ONVIF_PASS")
	if user == "" || pass == "" {
		t.Skip("TAPO_ONVIF_USER / TAPO_ONVIF_PASS not set; skipping real-camera integration test")
	}
	host := os.Getenv("TAPO_ONVIF_HOST")
	if host == "" {
		host = "192.168.0.6:2020"
	}

	dir := t.TempDir()
	key := make([]byte, cameracreds.MasterKeySize)
	store, err := cameracreds.OpenStore(dir, key)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	const candidateKey = "integration-test-video-pipeline"
	if _, err := store.Apply([]cameracreds.Credential{{
		ID:            "integration-cred",
		Scope:         cameracreds.ScopeDevice,
		CandidateKeys: []string{candidateKey},
		Username:      user,
		Password:      pass,
		Revision:      1,
	}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	provider := cameracreds.NewProvider(store)
	cred, ok := provider.Resolve(candidateKey, "")
	if !ok {
		t.Fatal("no credential resolved for test candidate")
	}

	client := onvif.NewClient(5*time.Second, nil)
	client.SetXAddrValidator(func(raw string) (*url.URL, int, error) {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, 0, err
		}
		return u, 2020, nil
	})

	xaddr := "http://" + host + "/onvif/device_service"
	discoverCtx, discoverCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer discoverCancel()

	if _, err := client.GetDeviceInformationAuth(discoverCtx, xaddr, cred.Username, cred.Password); err != nil {
		t.Fatalf("GetDeviceInformationAuth: %v", err)
	}
	mediaXAddr, capErr := client.GetCapabilitiesAuth(discoverCtx, xaddr, cred.Username, cred.Password)
	if capErr != nil || mediaXAddr == "" {
		mediaXAddr = xaddr
	}
	profiles, err := client.GetProfilesAuth(discoverCtx, mediaXAddr, cred.Username, cred.Password)
	if err != nil {
		t.Fatalf("GetProfilesAuth: %v", err)
	}
	if len(profiles) == 0 {
		t.Fatal("camera returned zero media profiles")
	}

	for _, p := range profiles {
		t.Logf("profile available: token=%s role=%s onvif_reported_codec=%s %dx%d fps=%g", p.Token, p.Role, p.Codec, p.Width, p.Height, p.FPS)
	}

	// Substream is Hito H's default recommended role. Selection here does
	// NOT gate on ONVIF's reported Codec field: this camera model's ONVIF
	// GetProfiles response mismaps video/audio encoder metadata (observed:
	// both profiles report "G711" regardless of actual video codec) — a
	// pre-existing Hito E discovery concern, not a Hito H bug. The video
	// pipeline itself doesn't trust that field either; it uses the SDP
	// a=rtpmap codec instead (see Supervisor.run in internal/rtsp), which
	// is the RFC-mandated on-the-wire source of truth.
	profile := profiles[0]
	for _, p := range profiles {
		if p.Role == "sub" {
			profile = p
			break
		}
	}
	t.Logf("selected profile: role=%s codec=%s %dx%d fps=%g", profile.Role, profile.Codec, profile.Width, profile.Height, profile.FPS)

	streamURI, err := client.GetStreamUriAuth(discoverCtx, mediaXAddr, profile.Token, cred.Username, cred.Password)
	if err != nil {
		t.Fatalf("GetStreamUriAuth: %v", err)
	}
	addr, rtspPath, err := rtsptest.ParseTarget(streamURI)
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}

	rtspCfg := rtsp.Config{
		StreamRole:     "sub",
		PacketTimeout:  5 * time.Second,
		InitialBackoff: 1 * time.Second,
		MaxBackoff:     10 * time.Second,
		DialTimeout:    5 * time.Second,
		Enabled:        true,
	}
	rtspMgr := rtsp.NewManager(rtspCfg, nil, nil)

	procCfg := processing.Config{
		Enabled:                true,
		TargetFPS:              2,
		OutputWidth:            320,
		OutputHeight:           180,
		RingBufferSize:         10,
		QueueDepth:             64,
		DecodeQueueDepth:       4,
		MaxConcurrentPipelines: 2,
		FFmpegPath:             "ffmpeg",
		DecodeTimeout:          10 * time.Second,
	}
	health := &capturingHealthSink{}
	procMgr := processing.NewManager(procCfg, rtspMgr, health, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := rtspMgr.Start(ctx); err != nil {
		t.Fatalf("rtspMgr.Start: %v", err)
	}
	if err := procMgr.Start(ctx); err != nil {
		t.Fatalf("procMgr.Start: %v", err)
	}

	rtspMgr.SetTargets([]rtsp.CameraTarget{{
		CandidateKey: candidateKey,
		Addr:         addr,
		RTSPPath:     rtspPath,
		Username:     cred.Username,
		Password:     cred.Password,
		StreamRole:   profile.Role,
		Codec:        profile.Codec,
		Width:        profile.Width,
		Height:       profile.Height,
		FPS:          profile.FPS,
	}})

	var memStart runtime.MemStats
	runtime.ReadMemStats(&memStart)

	const targetFrames = 100
	const maxWait = 40 * time.Second
	deadline := time.Now().Add(maxWait)
	start := time.Now()

	var lastStatus processing.PipelineStatus
	var maxReconnects int64
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)

		for _, s := range rtspMgr.Snapshot() {
			if s.CandidateKey == candidateKey && s.ReconnectCount > maxReconnects {
				maxReconnects = s.ReconnectCount
			}
		}

		snap := health.last
		for _, s := range snap.Cameras {
			if s.CandidateKey == candidateKey {
				lastStatus = s
			}
		}
		t.Logf("elapsed=%s state=%s frames_received=%d frames_decoded=%d frames_sampled=%d frames_dropped=%d queue_depth=%d buffer_usage=%d decode_latency_ms=%.1f",
			time.Since(start).Round(time.Second), lastStatus.State, lastStatus.FramesReceived, lastStatus.FramesDecoded,
			lastStatus.FramesSampled, lastStatus.FramesDropped, lastStatus.QueueDepth, lastStatus.BufferUsage, lastStatus.DecodeLatencyMs)

		if lastStatus.FramesDecoded >= targetFrames || (time.Since(start) >= 30*time.Second && lastStatus.FramesDecoded > 0) {
			break
		}
	}

	var memEnd runtime.MemStats
	runtime.ReadMemStats(&memEnd)
	t.Logf("process heap: start=%dKB end=%dKB", memStart.HeapAlloc/1024, memEnd.HeapAlloc/1024)

	if lastStatus.FramesDecoded == 0 {
		t.Fatalf("no frames decoded within %s — decode PASS acceptance not met (last status: %+v)", maxWait, lastStatus)
	}
	if lastStatus.FramesSampled == 0 {
		t.Error("frames_sampled = 0 — sampling stage produced nothing")
	}
	if maxReconnects > 1 {
		t.Errorf("reconnect_count = %d during the run — looks like a reconnect loop, not a stable connection", maxReconnects)
	}

	t.Logf("ACCEPTANCE: codec=%s source=%dx%d output=320x180 frames_decoded=%d frames_sampled=%d frames_dropped=%d reconnects=%d",
		profile.Codec, profile.Width, profile.Height, lastStatus.FramesDecoded, lastStatus.FramesSampled, lastStatus.FramesDropped, maxReconnects)

	// Save one diagnostic frame OUTSIDE the repo for manual visual
	// spot-check — never committed.
	saveOneDiagnosticFrame(t, procMgr, candidateKey)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := procMgr.Stop(stopCtx); err != nil {
		t.Errorf("procMgr.Stop: %v", err)
	}
	if err := rtspMgr.Stop(stopCtx); err != nil {
		t.Errorf("rtspMgr.Stop: %v", err)
	}
}

// saveOneDiagnosticFrame is a best-effort save of the ring buffer's most
// recent frame to os.TempDir() (never inside the repo, never committed) so
// a human can visually spot-check decode/resize correctness with e.g.
// `ffplay -f rawvideo -pixel_format yuv420p -video_size 320x180 <file>`.
func saveOneDiagnosticFrame(t *testing.T, procMgr *processing.Manager, candidateKey string) {
	t.Helper()
	sink := procMgr.DebugSink()
	if sink == nil || sink.Count() == 0 {
		t.Log("no frame available to save for diagnostics")
		return
	}
	f := sink.Last()
	if f.CandidateKey != candidateKey || len(f.Data) == 0 {
		t.Log("last debug frame does not match this candidate; skipping diagnostic save")
		return
	}
	path := filepath.Join(os.TempDir(), fmt.Sprintf("geocam-h-tc70-diagnostic-%dx%d-%d.yuv420p", f.OutputWidth, f.OutputHeight, time.Now().Unix()))
	if err := os.WriteFile(path, f.Data, 0o600); err != nil {
		t.Logf("failed to save diagnostic frame: %v", err)
		return
	}
	t.Logf("saved 1 diagnostic frame OUTSIDE the repo (not committed): %s (%dx%d, inspect with: ffplay -f rawvideo -pixel_format yuv420p -video_size %dx%d %s)",
		path, f.OutputWidth, f.OutputHeight, f.OutputWidth, f.OutputHeight, path)
}
