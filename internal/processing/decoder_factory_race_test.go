package processing

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
)

// TestPipeline_SetDecoderFactoryOnRunningPipeline pins the concurrency contract
// SetDecoderFactory's own mutex already implies: substituting the decoder
// constructor while the pipeline is running must be race-free, including while
// run() is between decoder generations.
//
// Hito X's measurement harness substitutes the ffmpeg subprocess on a live
// pipeline — that is how its decode benchmark runs in CI without ffmpeg — and
// `go test -race` reported a write inside SetDecoderFactory racing an
// unsynchronized read of the same field in run(). The field was written under
// p.mu and read without it, so the guard was one-sided.
//
// Run with -race for this test to have any teeth: without the detector it only
// proves the pipeline still starts and stops.
func TestPipeline_SetDecoderFactoryOnRunningPipeline(t *testing.T) {
	cfg := Config{QueueDepth: 16, RingBufferSize: 8, DecodeTimeout: 0}
	desc := rtsp.StreamDescriptor{CandidateKey: "cam-factory", Codec: "H264", Width: 4, Height: 4, FPS: 15, StreamRole: "sub"}

	router := NewRouter([]Sink{NewDebugSink()}, 8, nil)
	defer router.Stop()

	// Every decoder this pipeline builds is already dead, so run() keeps
	// looping back to the factory field instead of reading it once — which is
	// what turns a narrow window into a reproducible one.
	newDeadDecoder := func() (VideoDecoder, error) {
		f := newFakeDecoder(4, 4)
		f.crash()
		return f, nil
	}

	p := newCameraPipeline("cam-factory", desc, cfg, router, nil)
	p.decoderFactory = newDeadDecoder

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	// Wait for the first generation to be entered, so run() has already read
	// the field once; the substitutions below therefore have something to race
	// with even on the very first write.
	waitForPipelineState(t, p, "error", 2*time.Second)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				p.SetDecoderFactory(newDeadDecoder)
				time.Sleep(time.Millisecond)
			}
		}()
	}

	// Give run() time to cycle through more than one generation while the
	// factory is being replaced underneath it.
	time.Sleep(600 * time.Millisecond)
	close(stop)
	wg.Wait()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := p.Stop(stopCtx); err != nil {
		t.Fatalf("Stop after concurrent factory substitution: %v", err)
	}
}

// waitForPipelineState blocks until the pipeline reports state, or fails.
func waitForPipelineState(t *testing.T, p *cameraPipeline, state string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if p.State() == state {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pipeline never reached state %q (last %q)", state, p.State())
}
