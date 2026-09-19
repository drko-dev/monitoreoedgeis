package perf

import (
	"encoding/binary"
	"fmt"
)

// RTP constants for the single H.264 video track this harness streams.
// PayloadType 96 is the dynamic payload type the production pipeline's own
// RTSP simulator (internal/rtsptest) and its default SDP already use, so the
// packetizer speaks exactly the wire format internal/rtsp expects.
const (
	RTPPayloadTypeH264 = 96
	RTPHeaderLen       = 12
	// DefaultRTPMTU is the maximum total RTP packet size (header included).
	// 1200 keeps each packet inside a typical 1500-byte Ethernet MTU once IP
	// and TCP interleaved framing are accounted for, which is also why real
	// cameras fragment this way.
	DefaultRTPMTU = 1200
	// DefaultRTPStartSeq is an arbitrary but fixed initial sequence number so
	// two runs of the harness produce identical wire bytes.
	DefaultRTPStartSeq = 1000
	// DefaultRTPSSRC is equally arbitrary and equally fixed.
	DefaultRTPSSRC = 0x47454341 // "GECA"
	// RTPClockRate is the fixed H.264 RTP clock (RFC 6184 §8.2).
	RTPClockRate = 90000
)

// Packetizer turns access units into RFC 6184 RTP packets: a single-NAL
// packet when the NAL fits the MTU, FU-A fragmentation when it does not. It
// is deliberately a small, deterministic, allocation-simple encoder — the
// fact that it round-trips through the production
// processing.H264Depacketizer is asserted by packetizer_test.go, so this can
// never drift from the wire format the Edge actually consumes.
//
// It is single-goroutine by design: one Packetizer per camera feed, exactly
// like the depacketizer it feeds.
type Packetizer struct {
	PayloadType byte
	SSRC        uint32
	// MTU is the maximum total RTP packet size, header included.
	MTU int

	seq     uint16
	started bool
}

// NewPacketizer creates a Packetizer with DefaultRTPMTU when mtu <= 0.
func NewPacketizer(ssrc uint32, mtu int) *Packetizer {
	if mtu <= 0 {
		mtu = DefaultRTPMTU
	}
	if mtu <= RTPHeaderLen+2 {
		mtu = RTPHeaderLen + 2 + 1
	}
	return &Packetizer{PayloadType: RTPPayloadTypeH264, SSRC: ssrc, MTU: mtu, seq: DefaultRTPStartSeq}
}

// NextSeq returns the next RTP sequence number, wrapping at 16 bits like a
// real sender.
func (p *Packetizer) NextSeq() uint16 {
	if !p.started {
		p.started = true
		return p.seq
	}
	p.seq++
	return p.seq
}

// PacketizeAU encodes one entire access unit as RTP packets carrying the
// given RTP timestamp. The marker bit is set on exactly the last packet of
// the access unit — the boundary signal processing.H264Depacketizer closes an
// access unit on (RFC 6184 §5.1).
//
// A NAL unit whose type is 24..27 (an aggregation/fragmentation packet type
// defined only on the wire) is rejected: those are not valid bitstream NAL
// units and re-wrapping them would produce a stream no decoder accepts.
func (p *Packetizer) PacketizeAU(au AccessUnit, timestamp uint32) ([][]byte, error) {
	var packets [][]byte
	for _, nal := range au.NALUs {
		if nal.Type >= NALTypeSTAPA && nal.Type <= 27 {
			return nil, fmt.Errorf("perf: refusing to packetize wire-only NAL type %d", nal.Type)
		}
		if len(nal.Bytes) == 0 {
			continue
		}
		if RTPHeaderLen+len(nal.Bytes) <= p.MTU {
			packets = append(packets, p.singleNALPacket(nal.Bytes, timestamp))
			continue
		}
		packets = append(packets, p.fragmentedPackets(nal.Bytes, timestamp)...)
	}
	if len(packets) == 0 {
		return nil, nil
	}
	// The marker bit flags "last packet of this access unit" (RFC 6184
	// §5.1); it can only be set once the whole unit has been encoded.
	packets[len(packets)-1][1] |= 0x80
	return packets, nil
}

func (p *Packetizer) singleNALPacket(nalu []byte, timestamp uint32) []byte {
	pkt := make([]byte, RTPHeaderLen+len(nalu))
	p.writeHeader(pkt, timestamp)
	copy(pkt[RTPHeaderLen:], nalu)
	return pkt
}

func (p *Packetizer) fragmentedPackets(nalu []byte, timestamp uint32) [][]byte {
	// RFC 6184 §5.8: FU-A carries the original NAL header's F/NRI bits in the
	// FU indicator and its type in the FU header; the original header byte
	// itself is not transmitted.
	indicator := (nalu[0] & 0xE0) | NALTypeFUA
	body := nalu[1:]
	perFragment := p.MTU - RTPHeaderLen - 2

	var out [][]byte
	for offset := 0; offset < len(body); offset += perFragment {
		end := offset + perFragment
		if end > len(body) {
			end = len(body)
		}
		pkt := make([]byte, RTPHeaderLen+2+(end-offset))
		p.writeHeader(pkt, timestamp)
		pkt[RTPHeaderLen] = indicator
		fuHeader := nalu[0] & 0x1F
		if offset == 0 {
			fuHeader |= 0x80 // start
		}
		if end == len(body) {
			fuHeader |= 0x40 // end
		}
		pkt[RTPHeaderLen+1] = fuHeader
		copy(pkt[RTPHeaderLen+2:], body[offset:end])
		out = append(out, pkt)
	}
	return out
}

func (p *Packetizer) writeHeader(pkt []byte, timestamp uint32) {
	pkt[0] = 0x80 // version 2, no padding/extension/CSRC
	pkt[1] = p.PayloadType & 0x7F
	binary.BigEndian.PutUint16(pkt[2:4], p.NextSeq())
	binary.BigEndian.PutUint32(pkt[4:8], timestamp)
	binary.BigEndian.PutUint32(pkt[8:12], p.SSRC)
}

// FrameTimestamp returns the RTP timestamp of frame index i for a stream at
// nominalFPS, on the fixed 90 kHz H.264 clock. Frames are assumed evenly
// spaced, which is what the harness's own generator produces.
func FrameTimestamp(frameIndex int, nominalFPS float64) uint32 {
	if nominalFPS <= 0 {
		return uint32(frameIndex)
	}
	return uint32(float64(frameIndex) * RTPClockRate / nominalFPS)
}
