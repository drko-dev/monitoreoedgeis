package processing

import (
	"context"
	"sync"
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

// TestPipeline_WatchdogRestartsStalledDecoder is the regression test for
// review point 2: a decoder that keeps accepting access units (upstream IS
// flowing) but never produces a frame for cfg.DecodeTimeout must be
// detected as stalled and restarted — distinct from the camera simply not
// sending RTP, which must NOT trigger a restart.
func TestPipeline_WatchdogRestartsStalledDecoder(t *testing.T) {
	cfg := Config{RingBufferSize: 5, QueueDepth: 16, DecodeTimeout: 150 * time.Millisecond}
	desc := rtsp.StreamDescriptor{CandidateKey: "cam1", Codec: "H264", Width: 4, Height: 4}

	router := NewRouter([]Sink{NewDebugSink()}, 8, nil)
	defer router.Stop()

	p := newCameraPipeline("cam1", desc, cfg, router, nil)

	var attempts atomic.Int32
	var mu sync.Mutex
	var fakes []*fakeDecoder
	p.decoderFactory = func() (VideoDecoder, error) {
		attempts.Add(1)
		fd := newFakeDecoder(4, 4)
		fd.stalled.Store(true) // accepts input, never emits a frame
		mu.Lock()
		fakes = append(fakes, fd)
		mu.Unlock()
		return fd, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	// Keep feeding packets so upstream IS flowing throughout — the
	// watchdog must trigger because the decoder is stuck, not because the
	// camera went quiet.
	stop := make(chan struct{})
	go func() {
		i := uint16(0)
		for {
			select {
			case <-stop:
				return
			default:
				pkt := buildRTPPacket(true, 96, i, uint32(i)*3000, 1, nil, 0, 0, []byte{0x65, byte(i)})
				p.OnPacket(pkt, time.Now())
				i++
				time.Sleep(20 * time.Millisecond)
			}
		}
	}()
	defer close(stop)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && attempts.Load() < 2 {
		time.Sleep(20 * time.Millisecond)
	}
	if attempts.Load() < 2 {
		t.Fatal("watchdog never restarted a stalled (input-flowing, no-output) decoder")
	}

	cancel()
	p.Wait()
}

// TestPipeline_NoWatchdogRestartWhenUpstreamIdle proves the negative case:
// no access units arriving at all (camera not sending RTP, or nothing
// depacketized yet) must never be mistaken for a decoder stall.
func TestPipeline_NoWatchdogRestartWhenUpstreamIdle(t *testing.T) {
	cfg := Config{RingBufferSize: 5, QueueDepth: 16, DecodeTimeout: 100 * time.Millisecond}
	desc := rtsp.StreamDescriptor{CandidateKey: "cam1", Codec: "H264", Width: 4, Height: 4}

	router := NewRouter([]Sink{NewDebugSink()}, 8, nil)
	defer router.Stop()

	p := newCameraPipeline("cam1", desc, cfg, router, nil)

	var attempts atomic.Int32
	p.decoderFactory = func() (VideoDecoder, error) {
		attempts.Add(1)
		fd := newFakeDecoder(4, 4)
		fd.stalled.Store(true)
		return fd, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	// Deliberately send nothing: no AUs ever reach the decoder.
	time.Sleep(500 * time.Millisecond)

	if attempts.Load() != 1 {
		t.Fatalf("decoderFactory called %d times, want exactly 1 (idle upstream must never trigger a restart)", attempts.Load())
	}

	cancel()
	p.Wait()
}

// TestPipeline_MetricsCumulativeAcrossDecoderRestart is the regression test
// for review point 5: FramesDecoded must be cumulative across a decoder
// restart (never drop back toward zero), and DecodedFPS (a delta between
// two Status() calls) must never go negative right after one.
func TestPipeline_MetricsCumulativeAcrossDecoderRestart(t *testing.T) {
	cfg := Config{RingBufferSize: 5, QueueDepth: 16}
	desc := rtsp.StreamDescriptor{CandidateKey: "cam1", Codec: "H264", Width: 4, Height: 4}

	router := NewRouter([]Sink{NewDebugSink()}, 8, nil)
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

	send := func(seq uint16) {
		pkt := buildRTPPacket(true, 96, seq, uint32(seq)*3000, 1, nil, 0, 0, []byte{0x65, byte(seq)})
		p.OnPacket(pkt, time.Now())
	}
	for i := uint16(0); i < 5; i++ {
		send(i)
	}

	deadline := time.Now().Add(2 * time.Second)
	var statusA PipelineStatus
	for time.Now().Before(deadline) {
		statusA = p.Status()
		if statusA.FramesDecoded >= 5 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if statusA.FramesDecoded < 5 {
		t.Fatalf("decoder A: FramesDecoded = %d, want >= 5", statusA.FramesDecoded)
	}

	first.crash() // forces the outer run() loop to fold A's counts and start B

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && attempts.Load() < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if attempts.Load() < 2 {
		t.Fatal("decoder B never started after A crashed")
	}

	for i := uint16(5); i < 10; i++ {
		send(i)
	}

	deadline = time.Now().Add(2 * time.Second)
	var statusB PipelineStatus
	for time.Now().Before(deadline) {
		statusB = p.Status()
		if statusB.FramesDecoded >= statusA.FramesDecoded+5 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if statusB.FramesDecoded < statusA.FramesDecoded+5 {
		t.Fatalf("cumulative FramesDecoded after restart = %d, want >= %d (A's %d + 5 more from B)",
			statusB.FramesDecoded, statusA.FramesDecoded+5, statusA.FramesDecoded)
	}
	if statusB.DecodedFPS < 0 {
		t.Fatalf("DecodedFPS went negative after decoder restart: %v", statusB.DecodedFPS)
	}

	cancel()
	p.Wait()
}

// TestPipeline_ShutdownDoesNotDeadlockOnBlockedPush is the regression test
// for the shutdown-ordering deadlock found in review: if feedLoop is
// blocked inside dec.Push() (as it would be for a real FFmpegDecoder whose
// stdin Write blocks because ffmpeg stopped consuming input), shutdown
// must still complete — closing the decoder is what unblocks Push, and
// that must happen before waiting on the goroutine stuck inside it, not
// after. With the old ordering (wait, then close) this test would hang
// until its own timeout and fail; with the fix it returns quickly.
func TestPipeline_ShutdownDoesNotDeadlockOnBlockedPush(t *testing.T) {
	cfg := Config{RingBufferSize: 5, QueueDepth: 16}
	desc := rtsp.StreamDescriptor{CandidateKey: "cam1", Codec: "H264", Width: 4, Height: 4}

	router := NewRouter([]Sink{NewDebugSink()}, 8, nil)
	defer router.Stop()

	p := newCameraPipeline("cam1", desc, cfg, router, nil)
	bd := newBlockingPushDecoder()
	p.decoderFactory = func() (VideoDecoder, error) { return bd, nil }

	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)

	// Feed one packet so feedLoop pulls it and calls dec.Push(), which
	// blocks (by construction) until Close() releases it.
	pkt := buildRTPPacket(true, 96, 1, 1000, 1, nil, 0, 0, []byte{0x65, 0x01})
	p.OnPacket(pkt, time.Now())

	// Give feedLoop a moment to actually reach the blocking call.
	time.Sleep(100 * time.Millisecond)

	cancel()

	done := make(chan struct{})
	go func() {
		p.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Shutdown completed even though Push() was blocked — correct.
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline shutdown deadlocked: feedLoop was blocked in Push() and the decoder was never closed to release it")
	}
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
