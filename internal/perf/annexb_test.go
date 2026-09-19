package perf

import (
	"bytes"
	"testing"
)

// annexB builds an Annex-B byte stream from NAL unit payloads, alternating
// 3- and 4-byte start codes so the splitter's handling of both is exercised.
func annexB(nalus ...[]byte) []byte {
	var buf bytes.Buffer
	for i, n := range nalus {
		if i%2 == 0 {
			buf.Write([]byte{0, 0, 0, 1})
		} else {
			buf.Write([]byte{0, 0, 1})
		}
		buf.Write(n)
	}
	return buf.Bytes()
}

func nal(nalType byte, filler byte, size int) []byte {
	b := make([]byte, size)
	b[0] = nalType & 0x1F
	for i := 1; i < size; i++ {
		b[i] = filler
	}
	return b
}

func TestSplitAnnexB_HandlesBothStartCodeLengths(t *testing.T) {
	sps := nal(NALTypeSPS, 0xA1, 12)
	pps := nal(NALTypePPS, 0xB2, 6)
	idr := nal(NALTypeSliceIDR, 0xC3, 40)

	got := SplitAnnexB(annexB(sps, pps, idr))
	if len(got) != 3 {
		t.Fatalf("expected 3 NAL units, got %d", len(got))
	}
	want := []struct {
		typ  byte
		size int
	}{{NALTypeSPS, 12}, {NALTypePPS, 6}, {NALTypeSliceIDR, 40}}
	for i, w := range want {
		if got[i].Type != w.typ {
			t.Errorf("nal %d: type = %d, want %d", i, got[i].Type, w.typ)
		}
		if len(got[i].Bytes) != w.size {
			t.Errorf("nal %d: %d bytes, want %d (start code must be stripped exactly once)", i, len(got[i].Bytes), w.size)
		}
	}
	if !bytes.Equal(got[0].Bytes, sps) {
		t.Error("SPS bytes were altered by the splitter")
	}
	if !bytes.Equal(got[2].Bytes, idr) {
		t.Error("IDR bytes were altered by the splitter")
	}
}

func TestSplitAnnexB_SkipsLeadingGarbageAndEmptyNALs(t *testing.T) {
	body := nal(NALTypeSlice, 0x11, 8)
	stream := append([]byte{0xDE, 0xAD, 0xBE, 0xEF}, annexB(body)...)
	got := SplitAnnexB(stream)
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 NAL unit (leading garbage skipped), got %d", len(got))
	}
	if !bytes.Equal(got[0].Bytes, body) {
		t.Errorf("NAL bytes = %x, want %x", got[0].Bytes, body)
	}
}

func TestSplitAnnexB_EmptyInputYieldsNothing(t *testing.T) {
	if got := SplitAnnexB(nil); len(got) != 0 {
		t.Fatalf("expected no NAL units from nil input, got %d", len(got))
	}
	if _, _, err := ParseAnnexBStream(nil); err == nil {
		t.Fatal("expected ParseAnnexBStream to reject an empty stream")
	}
}

func TestGroupAccessUnits_AUDDelimitsMultiSlicePictures(t *testing.T) {
	// Two pictures, each three slices, each introduced by an access unit
	// delimiter — the shape a real encoder produces with sliced threading.
	// Grouping by "a slice ends a picture" would wrongly yield six units.
	var stream [][]byte
	for p := 0; p < 2; p++ {
		stream = append(stream, nal(NALTypeAUD, byte(p), 2))
		if p == 0 {
			stream = append(stream, nal(NALTypeSPS, 0x60, 5), nal(NALTypePPS, 0x61, 4))
		}
		for s := 0; s < 3; s++ {
			stream = append(stream, nal(NALTypeSlice, byte(0x80+s), 20))
		}
	}

	aus, sawAUD := GroupAccessUnits(SplitAnnexB(annexB(stream...)))
	if !sawAUD {
		t.Fatal("expected the access unit delimiter to be detected")
	}
	if len(aus) != 2 {
		t.Fatalf("expected 2 access units, got %d", len(aus))
	}
	if len(aus[0].NALUs) != 6 {
		t.Errorf("first access unit has %d NAL units, want 6 (AUD+SPS+PPS+3 slices)", len(aus[0].NALUs))
	}
	if len(aus[1].NALUs) != 4 {
		t.Errorf("second access unit has %d NAL units, want 4 (AUD+3 slices)", len(aus[1].NALUs))
	}
	if AccessUnitBoundaryLabel(sawAUD) != "aud" {
		t.Errorf("boundary label = %q, want aud", AccessUnitBoundaryLabel(sawAUD))
	}
}

func TestGroupAccessUnits_VCLFallbackWhenNoDelimiter(t *testing.T) {
	// Single-slice pictures with no access unit delimiter: the fallback is
	// correct here, and the label must say so explicitly.
	stream := [][]byte{
		nal(NALTypeSPS, 0x60, 5), nal(NALTypePPS, 0x61, 4),
		nal(NALTypeSliceIDR, 0x90, 30),
		nal(NALTypeSlice, 0x91, 30),
		nal(NALTypeSlice, 0x92, 30),
	}
	aus, sawAUD := GroupAccessUnits(SplitAnnexB(annexB(stream...)))
	if sawAUD {
		t.Fatal("no access unit delimiter was present")
	}
	if len(aus) != 3 {
		t.Fatalf("expected 3 access units (one per slice), got %d", len(aus))
	}
	if AccessUnitBoundaryLabel(sawAUD) != "vcl-fallback" {
		t.Errorf("boundary label = %q, want vcl-fallback", AccessUnitBoundaryLabel(sawAUD))
	}
}

func TestVerifyAccessUnitCount_RefusesAMismatch(t *testing.T) {
	aus := []AccessUnit{{}, {}}
	if err := VerifyAccessUnitCount(aus, 2); err != nil {
		t.Fatalf("matching counts must be accepted, got %v", err)
	}
	if err := VerifyAccessUnitCount(aus, 5); err == nil {
		t.Fatal("a mismatch must be refused: measuring decode FPS over the wrong denominator is exactly what Hito X forbids")
	}
	if err := VerifyAccessUnitCount(aus, 0); err == nil {
		t.Fatal("a zero/unknown independent frame count must be refused, not treated as a match")
	}
}

func TestParameterSets_PicksTheFirstSPSAndPPS(t *testing.T) {
	sps1 := nal(NALTypeSPS, 0x01, 5)
	pps1 := nal(NALTypePPS, 0x02, 4)
	sps2 := nal(NALTypeSPS, 0x03, 5)
	pps2 := nal(NALTypePPS, 0x04, 4)
	aus, _ := GroupAccessUnits(SplitAnnexB(annexB(
		nal(NALTypeAUD, 0, 2), sps1, pps1, nal(NALTypeSliceIDR, 0x90, 10),
		nal(NALTypeAUD, 0, 2), sps2, pps2, nal(NALTypeSliceIDR, 0x91, 10),
	)))
	gotSPS, gotPPS := ParameterSets(aus)
	if !bytes.Equal(gotSPS, sps1) || !bytes.Equal(gotPPS, pps1) {
		t.Fatalf("expected the first SPS/PPS pair: got sps=%x pps=%x", gotSPS, gotPPS)
	}
}
