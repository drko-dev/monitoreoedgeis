package perf

import (
	"bytes"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

// TestPacketizer_RoundTripsThroughProductionDepacketizer is the single most
// important test in this package: it proves the harness's synthetic RTP
// stream is byte-compatible with the depacketizer the Edge actually runs, so
// a decode measurement can never be measuring a stream the product would not
// accept.
//
// It deliberately exercises all three RFC 6184 paths the production
// depacketizer implements — single-NAL packets, FU-A fragmentation of an
// oversize NAL, and a multi-slice access unit closed by the marker bit.
func TestPacketizer_RoundTripsThroughProductionDepacketizer(t *testing.T) {
	aus := []AccessUnit{
		{NALUs: []NALUnit{
			{Type: NALTypeAUD, Bytes: nal(NALTypeAUD, 0x00, 3)},
			{Type: NALTypeSPS, Bytes: nal(NALTypeSPS, 0xA5, 9)},
			{Type: NALTypePPS, Bytes: nal(NALTypePPS, 0xB6, 6)},
			{Type: NALTypeSliceIDR, Bytes: nal(NALTypeSliceIDR, 0xC7, 500)},
		}},
		{NALUs: []NALUnit{
			{Type: NALTypeAUD, Bytes: nal(NALTypeAUD, 0x00, 3)},
			// Larger than the MTU, so it must be FU-A fragmented.
			{Type: NALTypeSlice, Bytes: nal(NALTypeSlice, 0xD8, DefaultRTPMTU*3)},
		}},
		{NALUs: []NALUnit{
			{Type: NALTypeAUD, Bytes: nal(NALTypeAUD, 0x00, 3)},
			{Type: NALTypeSlice, Bytes: nal(NALTypeSlice, 0xE1, 64)},
			{Type: NALTypeSlice, Bytes: nal(NALTypeSlice, 0xE2, 64)},
			{Type: NALTypeSlice, Bytes: nal(NALTypeSlice, 0xE3, 64)},
		}},
	}

	pkt := NewPacketizer(DefaultRTPSSRC, DefaultRTPMTU)
	depack := processing.NewH264Depacketizer()

	var reconstructed [][][]byte
	var fusSeen bool
	for i, au := range aus {
		packets, err := pkt.PacketizeAU(au, FrameTimestamp(i, 15))
		if err != nil {
			t.Fatalf("PacketizeAU(%d): %v", i, err)
		}
		if len(packets) == 0 {
			t.Fatalf("access unit %d produced no packets", i)
		}
		// Every packet must fit the MTU.
		for j, p := range packets {
			if len(p) > DefaultRTPMTU {
				t.Fatalf("access unit %d packet %d is %d bytes, over the %d-byte MTU", i, j, len(p), DefaultRTPMTU)
			}
		}
		for _, p := range packets {
			hdr, err := processing.ParseRTPHeader(p)
			if err != nil {
				t.Fatalf("the production RTP parser rejected a harness packet: %v", err)
			}
			if hdr.Version != 2 {
				t.Fatalf("RTP version = %d, want 2", hdr.Version)
			}
			if hdr.PayloadType != RTPPayloadTypeH264 {
				t.Fatalf("payload type = %d, want %d", hdr.PayloadType, RTPPayloadTypeH264)
			}
			body := hdr.Payload(p)
			if len(body) > 0 && body[0]&0x1F == NALTypeFUA {
				fusSeen = true
			}
			out, err := depack.Push(hdr, body, time.Now())
			if err != nil {
				t.Fatalf("depacketizer Push: %v", err)
			}
			if out != nil {
				reconstructed = append(reconstructed, out.NALUs)
			}
		}
	}

	if !fusSeen {
		t.Fatal("no FU-A fragmentation was produced: the oversize-NAL path was not exercised")
	}
	if depack.ReassemblyErrors != 0 || depack.IncompleteAUsDropped != 0 || depack.UnsupportedNALTypes != 0 {
		t.Fatalf("the production depacketizer reported errors on the harness stream: reassembly=%d incomplete=%d unsupported=%d",
			depack.ReassemblyErrors, depack.IncompleteAUsDropped, depack.UnsupportedNALTypes)
	}
	if len(reconstructed) != len(aus) {
		t.Fatalf("depacketizer reconstructed %d access units, want %d", len(reconstructed), len(aus))
	}
	for i, got := range reconstructed {
		want := nalBytes(aus[i])
		if len(got) != len(want) {
			t.Fatalf("access unit %d: %d NAL units reconstructed, want %d", i, len(got), len(want))
		}
		for j := range want {
			if !bytes.Equal(got[j], want[j]) {
				t.Fatalf("access unit %d NAL %d: reconstructed %d bytes, want %d bytes (byte-identical)",
					i, j, len(got[j]), len(want[j]))
			}
		}
	}
}

func TestPacketizer_MarkerBitOnlyOnTheLastPacketOfAnAccessUnit(t *testing.T) {
	au := AccessUnit{NALUs: []NALUnit{
		{Type: NALTypeAUD, Bytes: nal(NALTypeAUD, 0, 3)},
		{Type: NALTypeSlice, Bytes: nal(NALTypeSlice, 0x77, DefaultRTPMTU*2)},
	}}
	pkt := NewPacketizer(DefaultRTPSSRC, DefaultRTPMTU)
	packets, err := pkt.PacketizeAU(au, 9000)
	if err != nil {
		t.Fatal(err)
	}
	if len(packets) < 3 {
		t.Fatalf("expected the oversize NAL to fragment into several packets, got %d", len(packets))
	}
	for i, p := range packets {
		hdr, err := processing.ParseRTPHeader(p)
		if err != nil {
			t.Fatal(err)
		}
		wantMarker := i == len(packets)-1
		if hdr.Marker != wantMarker {
			t.Errorf("packet %d marker = %t, want %t (the marker is the access-unit boundary signal)", i, hdr.Marker, wantMarker)
		}
	}
}

func TestPacketizer_SequenceNumbersAreContiguousAcrossAccessUnits(t *testing.T) {
	pkt := NewPacketizer(DefaultRTPSSRC, DefaultRTPMTU)
	var seqs []uint16
	for i := 0; i < 3; i++ {
		au := AccessUnit{NALUs: []NALUnit{{Type: NALTypeSlice, Bytes: nal(NALTypeSlice, byte(i), 100)}}}
		packets, err := pkt.PacketizeAU(au, FrameTimestamp(i, 15))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range packets {
			hdr, _ := processing.ParseRTPHeader(p)
			seqs = append(seqs, hdr.SequenceNumber)
		}
	}
	if len(seqs) != 3 {
		t.Fatalf("expected 3 packets, got %d", len(seqs))
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			t.Fatalf("sequence gap between packets: %d -> %d; the production depacketizer treats a gap as packet loss and discards the whole access unit",
				seqs[i-1], seqs[i])
		}
	}
}

func TestPacketizer_RefusesWireOnlyNALTypes(t *testing.T) {
	pkt := NewPacketizer(DefaultRTPSSRC, DefaultRTPMTU)
	for _, typ := range []byte{NALTypeSTAPA, 25, 26, 27} {
		au := AccessUnit{NALUs: []NALUnit{{Type: typ, Bytes: []byte{typ & 0x1F, 1, 2, 3}}}}
		if _, err := pkt.PacketizeAU(au, 0); err == nil {
			t.Errorf("NAL type %d must be refused: it is a wire-only type, not a bitstream NAL unit", typ)
		}
	}
}

func TestFrameTimestamp_IsMonotonicAtTheNominalRate(t *testing.T) {
	// 15 fps on the 90 kHz clock is a 6000-tick step; every frame must map to
	// a distinct timestamp so the depacketizer's timestamp-change fallback
	// never merges two pictures.
	prev := FrameTimestamp(0, 15)
	for i := 1; i < 150; i++ {
		cur := FrameTimestamp(i, 15)
		if cur <= prev {
			t.Fatalf("frame %d timestamp %d is not greater than the previous %d", i, cur, prev)
		}
		prev = cur
	}
	if got, want := FrameTimestamp(15, 15), uint32(RTPClockRate); got != want {
		t.Fatalf("frame 15 at 15fps = %d ticks, want %d (one second)", got, want)
	}
}
