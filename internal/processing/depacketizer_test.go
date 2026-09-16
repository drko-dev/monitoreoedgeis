package processing

import (
	"testing"
	"time"
)

func hdr(seq uint16, ts uint32, marker bool) RTPHeader {
	return RTPHeader{SequenceNumber: seq, Timestamp: ts, Marker: marker}
}

func TestDepacketizer_SingleNALU(t *testing.T) {
	d := NewH264Depacketizer()
	nalu := []byte{0x67, 0x01, 0x02, 0x03} // type 7 (SPS), arbitrary bytes

	au, err := d.Push(hdr(1, 1000, true), nalu, time.Unix(1, 0))
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if au == nil {
		t.Fatal("expected access unit on marker bit")
	}
	if len(au.NALUs) != 1 || string(au.NALUs[0]) != string(nalu) {
		t.Fatalf("unexpected NALUs: %v", au.NALUs)
	}
}

func TestDepacketizer_MultipleSingleNALUsBeforeMarker(t *testing.T) {
	d := NewH264Depacketizer()
	nalu1 := []byte{0x67, 0xAA}
	nalu2 := []byte{0x68, 0xBB}
	nalu3 := []byte{0x65, 0xCC} // IDR slice, marker set

	if au, _ := d.Push(hdr(1, 1000, false), nalu1, time.Now()); au != nil {
		t.Fatal("expected no AU before marker")
	}
	if au, _ := d.Push(hdr(2, 1000, false), nalu2, time.Now()); au != nil {
		t.Fatal("expected no AU before marker")
	}
	au, _ := d.Push(hdr(3, 1000, true), nalu3, time.Now())
	if au == nil || len(au.NALUs) != 3 {
		t.Fatalf("expected 3-NALU access unit, got %v", au)
	}
}

func TestDepacketizer_FUAReconstruction(t *testing.T) {
	d := NewH264Depacketizer()
	// Original NAL: type 5 (IDR), NRI=3 -> header byte 0x65.
	// FU indicator: F=0,NRI=3,Type=28 -> 0x7C. FU header S=1 -> 0x85, mid -> 0x05, end E=1 -> 0x45 (type 5).
	start := []byte{0x7C, 0x85, 0xAA, 0xBB}
	mid := []byte{0x7C, 0x05, 0xCC, 0xDD}
	end := []byte{0x7C, 0x45, 0xEE, 0xFF}

	if au, _ := d.Push(hdr(1, 500, false), start, time.Now()); au != nil {
		t.Fatal("expected no AU on FU-A start")
	}
	if au, _ := d.Push(hdr(2, 500, false), mid, time.Now()); au != nil {
		t.Fatal("expected no AU on FU-A middle")
	}
	au, _ := d.Push(hdr(3, 500, true), end, time.Now())
	if au == nil {
		t.Fatal("expected AU on FU-A end + marker")
	}
	if len(au.NALUs) != 1 {
		t.Fatalf("expected 1 reassembled NALU, got %d", len(au.NALUs))
	}
	want := []byte{0x65, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}
	got := au.NALUs[0]
	if string(got) != string(want) {
		t.Fatalf("reassembled NALU = %x, want %x", got, want)
	}
}

func TestDepacketizer_FUASequenceGapDiscardsAU(t *testing.T) {
	d := NewH264Depacketizer()
	sps := []byte{0x67, 0x01} // first NALU of the AU, opens it
	start := []byte{0x7C, 0x85, 0xAA}
	end := []byte{0x7C, 0x45, 0xFF}

	if au, _ := d.Push(hdr(9, 500, false), sps, time.Now()); au != nil {
		t.Fatal("expected no AU yet")
	}
	if au, _ := d.Push(hdr(10, 500, false), start, time.Now()); au != nil {
		t.Fatal("expected no AU on FU-A start")
	}
	// Sequence jumps from 10 to 13: a gap, one or more FU-A packets lost.
	// The whole AU (including the earlier SPS NALU) must be discarded, not
	// delivered with a missing/partial slice NALU.
	au, _ := d.Push(hdr(13, 500, true), end, time.Now())
	if au != nil {
		t.Fatalf("expected AU to be discarded after sequence gap, got %v", au)
	}
	if d.IncompleteAUsDropped != 1 {
		t.Fatalf("IncompleteAUsDropped = %d, want 1", d.IncompleteAUsDropped)
	}

	// The depacketizer must resync cleanly on the next access unit.
	nextIDR := []byte{0x65, 0x99}
	au2, _ := d.Push(hdr(14, 600, true), nextIDR, time.Now())
	if au2 == nil || len(au2.NALUs) != 1 || string(au2.NALUs[0]) != string(nextIDR) {
		t.Fatalf("expected clean AU after resync, got %v", au2)
	}
}

func TestDepacketizer_STAPA(t *testing.T) {
	d := NewH264Depacketizer()
	nalu1 := []byte{0x67, 0x01} // SPS
	nalu2 := []byte{0x68, 0x02} // PPS
	nalu3 := []byte{0x65, 0x03} // IDR

	payload := []byte{24} // STAP-A header
	for _, n := range [][]byte{nalu1, nalu2, nalu3} {
		payload = append(payload, byte(len(n)>>8), byte(len(n)))
		payload = append(payload, n...)
	}

	au, err := d.Push(hdr(1, 1000, true), payload, time.Now())
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if au == nil || len(au.NALUs) != 3 {
		t.Fatalf("expected 3 aggregated NALUs, got %v", au)
	}
	if string(au.NALUs[0]) != string(nalu1) || string(au.NALUs[1]) != string(nalu2) || string(au.NALUs[2]) != string(nalu3) {
		t.Fatalf("STAP-A unpack mismatch: %v", au.NALUs)
	}
}

func TestDepacketizer_TimestampFallbackBoundary(t *testing.T) {
	d := NewH264Depacketizer()
	nalu1 := []byte{0x67, 0xAA}
	nalu2 := []byte{0x65, 0xBB}

	// No marker bit set anywhere, but the RTP timestamp changes — the
	// documented fallback boundary must close the first AU.
	if au, _ := d.Push(hdr(1, 1000, false), nalu1, time.Now()); au != nil {
		t.Fatal("expected no AU yet")
	}
	au, _ := d.Push(hdr(2, 2000, false), nalu2, time.Now())
	if au == nil {
		t.Fatal("expected timestamp-change fallback to close the first AU")
	}
	if len(au.NALUs) != 1 || string(au.NALUs[0]) != string(nalu1) {
		t.Fatalf("unexpected fallback AU contents: %v", au.NALUs)
	}
}

func TestDepacketizer_UnsupportedNALTypeCounted(t *testing.T) {
	d := NewH264Depacketizer()
	// Type 29 (FU-B) is explicitly unsupported.
	payload := []byte{29, 0x00, 0x01}
	au, err := d.Push(hdr(1, 1000, true), payload, time.Now())
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if au != nil {
		t.Fatalf("expected no AU for unsupported NAL type, got %v", au)
	}
	if d.UnsupportedNALTypes != 1 {
		t.Fatalf("UnsupportedNALTypes = %d, want 1", d.UnsupportedNALTypes)
	}
}

func TestDepacketizer_ReceivedAtIsFirstPacketOfAU(t *testing.T) {
	d := NewH264Depacketizer()
	t0 := time.Unix(100, 0)
	t1 := time.Unix(200, 0)

	nalu1 := []byte{0x67, 0xAA}
	nalu2 := []byte{0x65, 0xBB}

	if au, _ := d.Push(hdr(1, 1000, false), nalu1, t0); au != nil {
		t.Fatal("expected no AU yet")
	}
	au, _ := d.Push(hdr(2, 1000, true), nalu2, t1)
	if au == nil {
		t.Fatal("expected AU on marker")
	}
	if !au.ReceivedAt.Equal(t0) {
		t.Fatalf("ReceivedAt = %v, want %v (first packet's time)", au.ReceivedAt, t0)
	}
}
