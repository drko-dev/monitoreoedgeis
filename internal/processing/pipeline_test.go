package processing

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
)

// TestPipeline_EndToEndWithFakeDecoder_H11 is the Hito H acceptance test:
// camera packets in -> depacketize -> decode -> sample -> resize -> ring
// buffer -> route to a debug sink -> valid frames out, entirely without
// YOLO, PyTorch, a Vision Worker, or Cloud Vision — and without spawning a
// real ffmpeg process (a fake decoder stands in; the real subprocess path
// is covered separately, ffmpeg-gated, in decoder_test.go).
func TestPipeline_EndToEndWithFakeDecoder_H11(t *testing.T) {
	cfg := Config{
		TargetFPS:      0, // sampling disabled: every decoded frame should pass through
		OutputWidth:    0,
		OutputHeight:   0,
		RingBufferSize: 10,
		QueueDepth:     16,
	}
	desc := rtsp.StreamDescriptor{CandidateKey: "cam1", Codec: "H264", Width: 4, Height: 4, FPS: 15, StreamRole: "sub"}

	debug := NewDebugSink()
	router := NewRouter([]Sink{debug}, 8, nil)
	defer router.Stop()

	p := newCameraPipeline("cam1", desc, cfg, router, nil)
	fd := newFakeDecoder(4, 4)
	p.decoderFactory = func() (VideoDecoder, error) { return fd, nil }

	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)

	const n = 5
	for i := 0; i < n; i++ {
		pkt := buildRTPPacket(true, 96, uint16(i), uint32(i*3000), 1, nil, 0, 0, []byte{0x65, byte(i)})
		p.OnPacket(pkt, time.Now())
		time.Sleep(5 * time.Millisecond)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && debug.Count() < n {
		time.Sleep(10 * time.Millisecond)
	}
	if debug.Count() != n {
		t.Fatalf("debug sink received %d frames, want %d — H11 requires the full pipeline to produce valid frames", debug.Count(), n)
	}

	last := debug.Last()
	if last.CandidateKey != "cam1" || last.Codec != "H264" || len(last.Data) == 0 {
		t.Fatalf("unexpected last frame metadata: %+v", last)
	}

	status := p.Status()
	if status.FramesReceived != n {
		t.Fatalf("FramesReceived = %d, want %d", status.FramesReceived, n)
	}
	if status.FramesDecoded != n {
		t.Fatalf("FramesDecoded = %d, want %d", status.FramesDecoded, n)
	}
	if status.FramesSampled != n {
		t.Fatalf("FramesSampled = %d, want %d", status.FramesSampled, n)
	}

	cancel()
	p.Wait()
}

func TestPipeline_RestartsDecoderAfterCrash(t *testing.T) {
	cfg := Config{RingBufferSize: 5, QueueDepth: 16}
	desc := rtsp.StreamDescriptor{CandidateKey: "cam1", Codec: "H264", Width: 4, Height: 4}

	debug := NewDebugSink()
	router := NewRouter([]Sink{debug}, 8, nil)
	defer router.Stop()

	p := newCameraPipeline("cam1", desc, cfg, router, nil)

	var attempts atomic.Int32
	firstCh := make(chan *fakeDecoder, 1)
	p.decoderFactory = func() (VideoDecoder, error) {
		n := attempts.Add(1)
		fd := newFakeDecoder(4, 4)
		if n == 1 {
			firstCh <- fd
		}
		return fd, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	var first *fakeDecoder
	select {
	case first = <-firstCh:
	case <-time.After(1 * time.Second):
		t.Fatal("first decoder never started")
	}
	first.crash()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && attempts.Load() < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if attempts.Load() < 2 {
		t.Fatal("pipeline did not restart the decoder after it crashed")
	}

	cancel()
	p.Wait()
}
