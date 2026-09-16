package processing

import "testing"

func buildRTPPacket(marker bool, pt uint8, seq uint16, ts uint32, ssrc uint32, csrc []uint32, extWords int, padding int, payload []byte) []byte {
	b0 := byte(2 << 6) // version 2
	if padding > 0 {
		b0 |= 0x20
	}
	if extWords > 0 {
		b0 |= 0x10
	}
	b0 |= byte(len(csrc) & 0x0F)

	b1 := pt & 0x7F
	if marker {
		b1 |= 0x80
	}

	buf := []byte{b0, b1, byte(seq >> 8), byte(seq)}
	buf = append(buf, byte(ts>>24), byte(ts>>16), byte(ts>>8), byte(ts))
	buf = append(buf, byte(ssrc>>24), byte(ssrc>>16), byte(ssrc>>8), byte(ssrc))
	for _, c := range csrc {
		buf = append(buf, byte(c>>24), byte(c>>16), byte(c>>8), byte(c))
	}
	if extWords > 0 {
		buf = append(buf, 0xBE, 0xDE, byte(extWords>>8), byte(extWords))
		for i := 0; i < extWords; i++ {
			buf = append(buf, 0, 0, 0, 0)
		}
	}
	buf = append(buf, payload...)
	if padding > 0 {
		for i := 1; i < padding; i++ {
			buf = append(buf, 0)
		}
		buf = append(buf, byte(padding))
	}
	return buf
}

func TestParseRTPHeader_MinimalFixedHeader(t *testing.T) {
	payload := []byte{0x67, 0xAA, 0xBB}
	pkt := buildRTPPacket(true, 96, 1000, 90000, 0xDEADBEEF, nil, 0, 0, payload)

	hdr, err := ParseRTPHeader(pkt)
	if err != nil {
		t.Fatalf("ParseRTPHeader: %v", err)
	}
	if hdr.Version != 2 {
		t.Errorf("version = %d, want 2", hdr.Version)
	}
	if !hdr.Marker {
		t.Error("expected marker bit set")
	}
	if hdr.PayloadType != 96 {
		t.Errorf("payload type = %d, want 96", hdr.PayloadType)
	}
	if hdr.SequenceNumber != 1000 {
		t.Errorf("seq = %d, want 1000", hdr.SequenceNumber)
	}
	if hdr.Timestamp != 90000 {
		t.Errorf("timestamp = %d, want 90000", hdr.Timestamp)
	}
	if hdr.SSRC != 0xDEADBEEF {
		t.Errorf("ssrc = %x, want deadbeef", hdr.SSRC)
	}
	got := hdr.Payload(pkt)
	if string(got) != string(payload) {
		t.Errorf("payload = %v, want %v", got, payload)
	}
}

func TestParseRTPHeader_CSRCList(t *testing.T) {
	payload := []byte{0x01, 0x02}
	csrc := []uint32{0x11111111, 0x22222222, 0x33333333}
	pkt := buildRTPPacket(false, 96, 1, 1, 1, csrc, 0, 0, payload)

	hdr, err := ParseRTPHeader(pkt)
	if err != nil {
		t.Fatalf("ParseRTPHeader: %v", err)
	}
	if len(hdr.CSRC) != 3 {
		t.Fatalf("CSRC count = %d, want 3", len(hdr.CSRC))
	}
	for i, c := range csrc {
		if hdr.CSRC[i] != c {
			t.Errorf("CSRC[%d] = %x, want %x", i, hdr.CSRC[i], c)
		}
	}
	if string(hdr.Payload(pkt)) != string(payload) {
		t.Errorf("payload mismatch with CSRC present")
	}
}

func TestParseRTPHeader_ExtensionHeader(t *testing.T) {
	payload := []byte{0xAA, 0xBB, 0xCC}
	pkt := buildRTPPacket(false, 96, 5, 5, 5, nil, 2, 0, payload)

	hdr, err := ParseRTPHeader(pkt)
	if err != nil {
		t.Fatalf("ParseRTPHeader: %v", err)
	}
	if !hdr.Extension {
		t.Error("expected Extension true")
	}
	if string(hdr.Payload(pkt)) != string(payload) {
		t.Errorf("payload after extension mismatch: got %v want %v", hdr.Payload(pkt), payload)
	}
}

func TestParseRTPHeader_Padding(t *testing.T) {
	payload := []byte{0x11, 0x22, 0x33, 0x44}
	pkt := buildRTPPacket(false, 96, 5, 5, 5, nil, 0, 4, payload)

	hdr, err := ParseRTPHeader(pkt)
	if err != nil {
		t.Fatalf("ParseRTPHeader: %v", err)
	}
	got := hdr.Payload(pkt)
	if string(got) != string(payload) {
		t.Errorf("payload with padding trimmed = %v, want %v", got, payload)
	}
}

func TestParseRTPHeader_TooShort(t *testing.T) {
	if _, err := ParseRTPHeader([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected error for packet shorter than fixed header")
	}
}

func TestParseRTPHeader_WrongVersion(t *testing.T) {
	pkt := []byte{0x00, 0x60, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1, 0xAB}
	if _, err := ParseRTPHeader(pkt); err == nil {
		t.Fatal("expected error for non-v2 rtp packet")
	}
}

func TestParseRTPHeader_TruncatedCSRC(t *testing.T) {
	// CC=2 declared but packet has no room for 8 bytes of CSRC.
	pkt := []byte{0x82, 0x60, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0}
	if _, err := ParseRTPHeader(pkt); err == nil {
		t.Fatal("expected error for truncated CSRC list")
	}
}

func TestParseRTPHeader_TruncatedExtension(t *testing.T) {
	pkt := buildRTPPacket(false, 96, 1, 1, 1, nil, 0, 0, nil)
	pkt[0] |= 0x10 // claim an extension is present, but don't append one
	if _, err := ParseRTPHeader(pkt); err == nil {
		t.Fatal("expected error for declared-but-missing header extension")
	}
}

func TestParseRTPHeader_InvalidPadding(t *testing.T) {
	pkt := buildRTPPacket(false, 96, 1, 1, 1, nil, 0, 0, []byte{1, 2})
	pkt[0] |= 0x20      // set padding bit
	pkt[len(pkt)-1] = 0 // but declare zero padding length — invalid
	if _, err := ParseRTPHeader(pkt); err == nil {
		t.Fatal("expected error for invalid (zero) padding length")
	}
}
