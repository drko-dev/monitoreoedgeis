package processing

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/rtsp"
)

// TestH264Depacketizer_OversizedAUBytesAreBounded is the regression test for the
// unbounded auNALUs accumulator. A sender that keeps sending NAL-1..23 packets
// with a constant RTP timestamp and never sets the marker bit used to grow the
// slice until the process ran out of memory; one such pipeline exists per
// camera and nothing else bounded it.
func TestH264Depacketizer_OversizedAUBytesAreBounded(t *testing.T) {
	d := NewH264Depacketizer()
	// A single-NALU payload of just under 64 KiB: 129 of them exceed 8 MiB.
	const chunk = 60 * 1024
	payload := make([]byte, chunk)
	payload[0] = 0x41 // NAL type 1, non-IDR slice

	var delivered int
	// 200 pushes push well past the 8 MiB ceiling.
	for i := 0; i < 200; i++ {
		au, err := d.Push(hdr(uint16(i+1), 9000, false), payload, time.Now())
		if err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
		if au != nil {
			delivered++
		}
		if d.auBytes > maxAccessUnitBytes {
			t.Fatalf("push %d: accumulated %d bytes, ceiling is %d", i, d.auBytes, maxAccessUnitBytes)
		}
		if len(d.auNALUs) > maxAccessUnitNALUs {
			t.Fatalf("push %d: accumulated %d NALUs, ceiling is %d", i, len(d.auNALUs), maxAccessUnitNALUs)
		}
	}

	// Past the ceiling the accumulator must be empty, not merely capped: a
	// ceiling that still holds 8 MiB per camera is not a bound worth having.
	if len(d.auNALUs) != 0 || d.auBytes != 0 {
		t.Fatalf("after exceeding the ceiling the accumulator holds %d NALUs / %d bytes, want 0/0", len(d.auNALUs), d.auBytes)
	}
	if !d.auOverflow {
		t.Error("auOverflow = false, want true once the ceiling was exceeded")
	}
	if delivered != 0 {
		t.Errorf("delivered %d access units from an overlong run, want 0", delivered)
	}
	if d.OversizedAUsDropped != 0 {
		t.Errorf("OversizedAUsDropped = %d before the boundary, want 0 (the AU is still open)", d.OversizedAUsDropped)
	}

	// The boundary resolves it, exactly once, and it is counted.
	au, err := d.Push(hdr(5000, 9000, true), payload, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if au != nil {
		t.Fatal("an oversized, truncated access unit must never be delivered to the decoder")
	}
	if d.OversizedAUsDropped != 1 {
		t.Errorf("OversizedAUsDropped = %d after the boundary, want 1", d.OversizedAUsDropped)
	}
	if d.IncompleteAUsDropped != 0 {
		t.Errorf("IncompleteAUsDropped = %d, want 0: an oversized AU is not packet loss", d.IncompleteAUsDropped)
	}
	if d.auOverflow {
		t.Error("auOverflow stayed set after the boundary closed the AU")
	}
}

// TestH264Depacketizer_ManySmallNALUsAreBounded covers the count ceiling, which
// is the one a stream of tiny NAL units hits before the byte ceiling.
func TestH264Depacketizer_ManySmallNALUsAreBounded(t *testing.T) {
	d := NewH264Depacketizer()
	payload := []byte{0x41, 0x00}

	for i := 0; i < maxAccessUnitNALUs+50; i++ {
		if _, err := d.Push(hdr(uint16(i+1), 700, false), payload, time.Now()); err != nil {
			t.Fatal(err)
		}
		if len(d.auNALUs) > maxAccessUnitNALUs {
			t.Fatalf("push %d: %d NALUs accumulated, ceiling is %d", i, len(d.auNALUs), maxAccessUnitNALUs)
		}
	}
	if len(d.auNALUs) != 0 {
		t.Fatalf("%d NALUs still accumulated at the count ceiling, want 0", len(d.auNALUs))
	}
	if au, _ := d.Push(hdr(65000, 700, true), payload, time.Now()); au != nil {
		t.Fatal("an AU that hit the NALU-count ceiling must not be delivered")
	}
	if d.OversizedAUsDropped != 1 {
		t.Errorf("OversizedAUsDropped = %d, want 1", d.OversizedAUsDropped)
	}
}

// TestH264Depacketizer_OversizedFURunIsBounded is the same property for the
// FU-A reassembly buffer: a fragment run that never sets its end bit grew
// fuBuf with no bound at all.
func TestH264Depacketizer_OversizedFURunIsBounded(t *testing.T) {
	d := NewH264Depacketizer()
	// FU indicator + FU header (start, then continuation) + payload.
	const chunk = 60 * 1024
	start := make([]byte, chunk)
	start[0] = 0x7C
	start[1] = 0x85
	mid := make([]byte, chunk)
	mid[0] = 0x7C
	mid[1] = 0x05

	if _, err := d.Push(hdr(1, 400, false), start, time.Now()); err != nil {
		t.Fatal(err)
	}
	seq := uint16(2)
	for i := 0; i < 200; i++ {
		if _, err := d.Push(hdr(seq, 400, false), mid, time.Now()); err != nil {
			t.Fatal(err)
		}
		seq++
		if len(d.fuBuf) > maxAccessUnitBytes {
			t.Fatalf("push %d: fuBuf grew to %d bytes, ceiling is %d", i, len(d.fuBuf), maxAccessUnitBytes)
		}
	}
	if len(d.fuBuf) != 0 {
		t.Fatalf("fuBuf holds %d bytes after exceeding the ceiling, want 0", len(d.fuBuf))
	}
	if d.fuActive {
		t.Error("fuActive = true after the run was abandoned")
	}
	if d.ReassemblyErrors == 0 {
		t.Error("ReassemblyErrors = 0: abandoning the run must be counted")
	}
	// The abandoned run leaves the AU truncated, so the boundary must drop it.
	if au, _ := d.Push(hdr(seq, 400, true), mid, time.Now()); au != nil {
		t.Fatal("a truncated FU-A run must not produce a delivered access unit")
	}
	if d.OversizedAUsDropped != 1 {
		t.Errorf("OversizedAUsDropped = %d, want 1", d.OversizedAUsDropped)
	}
}

// TestH264Depacketizer_LegitimateAUIsUnaffected guards against the ceiling
// becoming a correctness regression: a large-but-real frame — several NAL
// units including a big IDR slice — must still be delivered intact.
func TestH264Depacketizer_LegitimateAUIsUnaffected(t *testing.T) {
	d := NewH264Depacketizer()
	sps := []byte{0x67, 0x42, 0xC0, 0x1E}
	pps := []byte{0x68, 0xCE, 0x3C, 0x80}
	// 4 MiB of IDR slice data: half the ceiling, i.e. well beyond any real
	// access unit at the resolutions this Edge ingests, but still legal.
	idr := make([]byte, 4<<20)
	idr[0] = 0x65

	if au, _ := d.Push(hdr(1, 100, false), sps, time.Now()); au != nil {
		t.Fatal("no AU expected yet")
	}
	if au, _ := d.Push(hdr(2, 100, false), pps, time.Now()); au != nil {
		t.Fatal("no AU expected yet")
	}
	au, err := d.Push(hdr(3, 100, true), idr, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if au == nil {
		t.Fatal("a legitimate 4 MiB access unit must still be delivered")
	}
	if len(au.NALUs) != 3 {
		t.Fatalf("delivered %d NALUs, want 3", len(au.NALUs))
	}
	if !bytes.Equal(au.NALUs[2], idr) {
		t.Error("the delivered IDR NALU does not match what was pushed")
	}
	if d.OversizedAUsDropped != 0 || d.IncompleteAUsDropped != 0 || d.ReassemblyErrors != 0 {
		t.Errorf("a legitimate frame produced counters: oversized=%d incomplete=%d reassembly=%d",
			d.OversizedAUsDropped, d.IncompleteAUsDropped, d.ReassemblyErrors)
	}
	// The ceiling must not have cost anything in memory retained afterwards.
	if len(d.auNALUs) != 0 || d.auBytes != 0 {
		t.Errorf("AU state retained after delivery: %d NALUs / %d bytes", len(d.auNALUs), d.auBytes)
	}
}

// TestH264Depacketizer_OverflowStateRecovers verifies the accumulator returns
// to normal after one abandoned AU, so a single bad frame does not wedge the
// camera's stream for the rest of the process lifetime.
func TestH264Depacketizer_OverflowStateRecovers(t *testing.T) {
	d := NewH264Depacketizer()
	payload := make([]byte, 60*1024)
	payload[0] = 0x41

	// Drive one AU past the ceiling, then close it.
	for i := 0; i < 200; i++ {
		d.Push(hdr(uint16(i+1), 11, false), payload, time.Now())
	}
	d.Push(hdr(300, 11, true), payload, time.Now())
	if d.OversizedAUsDropped != 1 {
		t.Fatalf("OversizedAUsDropped = %d, want 1", d.OversizedAUsDropped)
	}

	// A normal access unit on a new timestamp must be delivered untouched.
	good := []byte{0x65, 0xAA, 0xBB}
	au, err := d.Push(hdr(400, 22, true), good, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if au == nil || len(au.NALUs) != 1 || !bytes.Equal(au.NALUs[0], good) {
		t.Fatalf("the stream did not recover after an oversized AU: %+v", au)
	}
	if d.OversizedAUsDropped != 1 || d.IncompleteAUsDropped != 0 {
		t.Errorf("counters after recovery: oversized=%d incomplete=%d, want 1/0", d.OversizedAUsDropped, d.IncompleteAUsDropped)
	}
}

// TestPipelineRingBufferDroppedIsObservable pins the fix for the silent ring
// buffer drop. The counter existed and was incremented, but no production code
// ever read it, so history loss for clip/debug snapshots was invisible in
// /status and in logs. It is now folded into FramesDropped and also reported on
// its own.
func TestPipelineRingBufferDroppedIsObservable(t *testing.T) {
	// A ring buffer of 2 with more sampled frames than that must overflow.
	cfg := Config{RingBufferSize: 2, QueueDepth: 16}
	desc := rtsp.StreamDescriptor{CandidateKey: "cam1", Codec: "H264", Width: 4, Height: 4}

	router := NewRouter([]Sink{NewDebugSink()}, 8, nil)
	defer router.Stop()

	p := newCameraPipeline("cam1", desc, cfg, router, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	// Push the two channels the pipeline consumes directly, bypassing RTP
	// parsing: what is under test is the ring buffer accounting, not the
	// depacketizer.
	if p.ring == nil {
		t.Fatal("pipeline has no ring buffer")
	}
	for i := 0; i < 6; i++ {
		p.ring.Push(Frame{Seq: uint64(i)})
	}
	p.ringDropped.Store(p.ring.Dropped())

	st := p.Status()
	if st.RingBufferDropped != 4 {
		t.Errorf("RingBufferDropped = %d, want 4", st.RingBufferDropped)
	}
	if st.FramesDropped < st.RingBufferDropped {
		t.Errorf("FramesDropped = %d must fold in RingBufferDropped = %d", st.FramesDropped, st.RingBufferDropped)
	}
}
