package agent

import (
	"context"
	"encoding/binary"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/cameracreds"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery"
	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsptest"
)

// buildMinimalRTPPacket builds the smallest valid RTP fixed header (RFC 3550
// §5.1, no CSRC/extension/padding — the same shape
// internal/processing/rtp_test.go's buildRTPPacket covers as
// TestParseRTPHeader_MinimalFixedHeader) followed by payload. It cannot
// import that test-only helper across package boundaries (internal/agent
// importing internal/processing's _test.go would not compile, and
// internal/processing cannot import internal/agent without a cycle), so
// this reproduces only the minimal case this file actually needs.
func buildMinimalRTPPacket(marker bool, seq uint16, ts uint32, ssrc uint32, payload []byte) []byte {
	buf := make([]byte, 12+len(payload))
	buf[0] = 0x80 // version=2, padding=0, extension=0, CSRC count=0
	buf[1] = 96   // payload type 96 (dynamic), marker bit set below
	if marker {
		buf[1] |= 0x80
	}
	binary.BigEndian.PutUint16(buf[2:4], seq)
	binary.BigEndian.PutUint32(buf[4:8], ts)
	binary.BigEndian.PutUint32(buf[8:12], ssrc)
	copy(buf[12:], payload)
	return buf
}

// recordingHealthSink captures the latest processing.VideoPipelineSummary,
// mirroring internal/processing's own test fake of the same shape (again
// not importable across the package boundary).
type recordingHealthSink struct {
	mu      sync.Mutex
	summary processing.VideoPipelineSummary
}

func (s *recordingHealthSink) SetVideoPipeline(summary processing.VideoPipelineSummary) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.summary = summary
}

func (s *recordingHealthSink) snapshot() processing.VideoPipelineSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.summary
}

// TestG1B_RealRTSPToProcessingManager proves — through the real production
// wiring, not a stand-in — that a CameraTarget produced by this PR's
// reconciler reaches internal/processing.Manager, not just
// internal/rtsp.Manager:
//
//	discovery/inventory -> buildCameraTargets -> rtsp.Manager.SetTargets
//	-> Supervisor -> RTP packets -> processing.Manager.OnPacket
//	  (reached only because processing.Manager.Start() itself calls
//	   rtsp.Manager.SetPacketSink(m) — real code, not stubbed here)
//	-> lazy cameraPipeline creation
//
// TestG1B_FullPipeline_LateCredentialConvergence stops at the RTSP layer
// (CameraStreamStatus.PacketsReceived); this test is the one that actually
// exercises processing.Manager, per the correction to that gap.
func TestG1B_RealRTSPToProcessingManager(t *testing.T) {
	sim, err := rtsptest.NewSimulator(rtsptest.Options{
		AutoPacketCount:    20,
		AutoPacketInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("rtsptest.NewSimulator: %v", err)
	}
	defer sim.Close()

	engine := newG1BFakeCamera(t, sim.Addr())
	store, provider := newG1BStore(t)

	rtspMgr := rtsp.NewManager(rtsp.Config{
		StreamRole:     "sub",
		PacketTimeout:  1 * time.Second,
		InitialBackoff: 50 * time.Millisecond,
		MaxBackoff:     500 * time.Millisecond,
		DialTimeout:    1 * time.Second,
		Enabled:        true,
	}, nil, nil)

	health := &recordingHealthSink{}
	procMgr := processing.NewManager(processing.Config{
		Enabled:                true,
		TargetFPS:              15,
		OutputWidth:            640,
		OutputHeight:           360,
		RingBufferSize:         10,
		QueueDepth:             16,
		DecodeQueueDepth:       4,
		MaxConcurrentPipelines: 4,
		FFmpegPath:             "ffmpeg",
		DecodeTimeout:          2 * time.Second,
	}, rtspMgr, health, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rtspMgr.Start(ctx); err != nil {
		t.Fatalf("rtspMgr.Start: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = rtspMgr.Stop(stopCtx)
	}()

	// The wiring under test: processing.Manager.Start() must itself call
	// rtsp.Manager.SetPacketSink(procMgr) — nothing here does it manually.
	if err := procMgr.Start(ctx); err != nil {
		t.Fatalf("procMgr.Start: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = procMgr.Stop(stopCtx)
	}()

	var reconciler *cameraTargetReconciler
	discMod, err := discovery.NewModule(discovery.ModuleOptions{
		Engine:   engine,
		Interval: time.Hour,
		OnScanSuccess: func() {
			if reconciler != nil {
				reconciler.onDiscoverySuccess()
			}
		},
	})
	if err != nil {
		t.Fatalf("discovery.NewModule: %v", err)
	}
	engine.SetCredentialResolver(func(candidateKey string) (string, string, bool) {
		if reconciler == nil {
			return "", "", false
		}
		return reconciler.resolve(candidateKey)
	})
	reconciler = newCameraTargetReconciler(discMod, provider, rtspMgr, "sub", nil)

	if err := discMod.Rediscover(context.Background()); err != nil {
		t.Fatalf("Rediscover: %v", err)
	}
	reconciler.reconcile()
	candidateKey := discMod.Engine().Inventory().List()[0].StableIdentity

	// No credential resolves yet: buildCameraTargets must not have handed
	// processing anything to work with.
	if got := procMgr.ActivePipelines(); len(got) != 0 {
		t.Fatalf("ActivePipelines = %v before any credential exists, want none", got)
	}

	if _, err := store.Apply([]cameracreds.Credential{{
		ID:            "1",
		Scope:         cameracreds.ScopeDevice,
		CandidateKeys: []string{candidateKey},
		Username:      "admin",
		Password:      "s3cret",
		Revision:      1,
	}}); err != nil {
		t.Fatalf("Store.Apply: %v", err)
	}
	reconciler.onCredentialsSynced()

	g1bWaitFor(t, "RTSP supervisor to reach ONLINE", 3*time.Second, func() bool {
		for _, s := range rtspMgr.Snapshot() {
			if s.CandidateKey == candidateKey && s.Status == rtsp.StateOnline {
				return true
			}
		}
		return false
	})

	// This is the assertion the previous, RTSP-only test could not make:
	// the packets reached processing.Manager and it created a pipeline for
	// this exact candidate — proving rtsp.Manager.SetPacketSink(procMgr)
	// really is wired, not just that RTSP itself is online.
	g1bWaitFor(t, "processing.Manager to lazily create a pipeline for the camera", 3*time.Second, func() bool {
		for _, k := range procMgr.ActivePipelines() {
			if k == candidateKey {
				return true
			}
		}
		return false
	})

	// Push one properly RTP-framed, marker-terminated H.264 NAL (an IDR
	// slice header byte; the rest of the payload is not a real bitstream,
	// so ffmpeg will not successfully decode it — that requires a real
	// camera and stays NOT_VALIDATED here). This is enough for the real
	// depacketizer to assemble and emit one complete access unit.
	pkt := buildMinimalRTPPacket(true, 1000, 90000, 0xC0FFEE, append([]byte{0x65}, []byte("not-a-real-bitstream-but-one-nal-unit")...))
	if err := sim.SendPacket(pkt); err != nil {
		t.Fatalf("SendPacket: %v", err)
	}

	var summary processing.VideoPipelineSummary
	g1bWaitFor(t, "health summary to report the camera with received frames", 3*time.Second, func() bool {
		summary = health.snapshot()
		for _, c := range summary.Cameras {
			if c.CandidateKey == candidateKey && (c.RTPPacketsReceived > 0 || c.FramesReceived > 0) {
				return true
			}
		}
		return false
	})
	if summary.CameraCount < 1 {
		t.Errorf("video_pipeline.camera_count = %d, want >= 1", summary.CameraCount)
	}
	var found *processing.PipelineStatus
	for i := range summary.Cameras {
		if summary.Cameras[i].CandidateKey == candidateKey {
			found = &summary.Cameras[i]
		}
	}
	if found == nil {
		t.Fatalf("camera %s not present in video_pipeline.cameras: %+v", candidateKey, summary.Cameras)
	}
	if found.RTPPacketsReceived == 0 && found.FramesReceived == 0 && found.FramesSampled == 0 {
		t.Errorf("all of rtp_packets_received/frames_received/frames_sampled are 0: %+v", found)
	}
	t.Logf("processing pipeline status for %s: %+v", candidateKey, found)
}
