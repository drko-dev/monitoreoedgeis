package processing

import (
	"bytes"
	"testing"
	"time"
)

func h265Header(nalType byte) []byte { return []byte{nalType << 1, 1} }

func TestH265DepacketizerSingleNAL(t *testing.T) {
	d := NewH265Depacketizer()
	payload := append(h265Header(1), 0xaa, 0xbb)
	au, err := d.Push(RTPHeader{SequenceNumber: 10, Timestamp: 100, Marker: true}, payload, time.Now())
	if err != nil || au == nil || len(au.NALUs) != 1 || !bytes.Equal(au.NALUs[0], payload) {
		t.Fatalf("single NAL failed: au=%+v err=%v", au, err)
	}
}

func TestH265DepacketizerFU(t *testing.T) {
	d := NewH265Depacketizer()
	// FU payload header type=49, reconstructed original NAL type=19 (IDR_W_RADL).
	base := h265Header(49)
	start := append(append([]byte{}, base...), byte(0x80|19), 0xaa, 0xbb)
	end := append(append([]byte{}, base...), byte(0x40|19), 0xcc)
	if au, _ := d.Push(RTPHeader{SequenceNumber: 1, Timestamp: 777}, start, time.Now()); au != nil {
		t.Fatalf("unexpected AU on FU start: %+v", au)
	}
	au, _ := d.Push(RTPHeader{SequenceNumber: 2, Timestamp: 777, Marker: true}, end, time.Now())
	want := append(h265Header(19), 0xaa, 0xbb, 0xcc)
	if au == nil || len(au.NALUs) != 1 || !bytes.Equal(au.NALUs[0], want) {
		t.Fatalf("FU reassembly = %+v, want %x", au, want)
	}
}

func TestH265DepacketizerAP(t *testing.T) {
	d := NewH265Depacketizer()
	n1 := append(h265Header(32), 0x01)
	n2 := append(h265Header(33), 0x02, 0x03)
	payload := append([]byte{}, h265Header(48)...)
	payload = append(payload, 0, byte(len(n1)))
	payload = append(payload, n1...)
	payload = append(payload, 0, byte(len(n2)))
	payload = append(payload, n2...)
	au, _ := d.Push(RTPHeader{SequenceNumber: 3, Timestamp: 900, Marker: true}, payload, time.Now())
	if au == nil || len(au.NALUs) != 2 || !bytes.Equal(au.NALUs[0], n1) || !bytes.Equal(au.NALUs[1], n2) {
		t.Fatalf("AP reassembly = %+v", au)
	}
}
