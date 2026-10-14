package processing

import (
	"context"
	"encoding/binary"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsptest"
	"testing"
	"time"
)

// TestLabRTSPRealH264Decode connects the actual RTSP and processing managers
// to a loopback-only simulator and sends valid single-NAL RTP H264 packets.
func TestLabRTSPRealH264Decode(t *testing.T) {
	requireFFmpeg(t)
	sim, err := rtsptest.NewSimulator(rtsptest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer sim.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rm := rtsp.NewManager(rtsp.DefaultConfig(), nil, nil)
	if err = rm.Start(ctx); err != nil {
		t.Fatal(err)
	}
	pm := NewManager(Config{Enabled: true, TargetFPS: 5, RingBufferSize: 10, QueueDepth: 16, DecodeQueueDepth: 8, MaxConcurrentPipelines: 2}, rm, &recordingHealthSink{}, nil)
	if err = pm.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		_ = pm.Stop(stop)
		_ = rm.Stop(stop)
	}()
	rm.SetTargets([]rtsp.CameraTarget{{CandidateKey: "h6d-lab", Addr: sim.Addr(), RTSPPath: "/live", StreamRole: "sub", Codec: "H264", Width: 64, Height: 64, FPS: 5}})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ss := rm.Snapshot()
		if len(ss) > 0 && ss[0].Status == rtsp.StateOnline {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	stream := generateAnnexB(t, 64, 64, 15)
	nalus := splitAnnexB(stream)
	if len(nalus) == 0 {
		t.Fatal("no H264 NALs")
	}
	for repeat := 0; repeat < 3; repeat++ {
		for i, nalu := range nalus {
			if len(nalu) == 0 || len(nalu) > 1200 {
				continue
			}
			rtp := make([]byte, 12+len(nalu))
			rtp[0] = 0x80
			rtp[1] = 96
			if i == len(nalus)-1 {
				rtp[1] |= 0x80
			}
			binary.BigEndian.PutUint16(rtp[2:4], uint16(repeat*len(nalus)+i+1))
			binary.BigEndian.PutUint32(rtp[4:8], uint32((repeat*len(nalus)+i)*3600))
			binary.BigEndian.PutUint32(rtp[8:12], 0x10203040)
			copy(rtp[12:], nalu)
			if err = sim.SendPacket(rtp); err != nil {
				t.Fatal(err)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		p := pm.Pipeline("h6d-lab")
		if p != nil {
			s := p.Status()
			if s.FramesDecoded > 0 {
				t.Logf("RTSP_DECODING_PASS packets=%d decoded=%d", s.RTPPacketsReceived, s.FramesDecoded)
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	p := pm.Pipeline("h6d-lab")
	if p == nil {
		t.Fatal("pipeline not created")
	}
	s := p.Status()
	t.Fatalf("RTSP H264 frames not decoded; packets=%d decoded=%d", s.RTPPacketsReceived, s.FramesDecoded)
}
