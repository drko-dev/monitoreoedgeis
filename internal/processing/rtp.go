package processing

import "fmt"

// RTPHeader is the parsed form of an RTP packet header (RFC 3550 §5.1),
// with PayloadStart/PayloadEnd already computed so callers never assume a
// fixed 12-byte header or reach past the packet's real bounds.
type RTPHeader struct {
	Version        uint8
	Padding        bool
	Extension      bool
	CSRCCount      uint8
	Marker         bool
	PayloadType    uint8
	SequenceNumber uint16
	Timestamp      uint32
	SSRC           uint32
	CSRC           []uint32

	// PayloadStart/PayloadEnd bound the actual media payload within the
	// original packet slice: PayloadStart skips the fixed header, CSRC
	// list, and any header extension; PayloadEnd trims declared padding
	// bytes off the tail.
	PayloadStart int
	PayloadEnd   int
}

// Payload returns the media payload slice of packet as bounded by hdr.
func (h RTPHeader) Payload(packet []byte) []byte {
	return packet[h.PayloadStart:h.PayloadEnd]
}

// ParseRTPHeader parses packet as an RTP header per RFC 3550 §5.1, validating
// every length it depends on against len(packet) before reading it. It never
// reads out of bounds and never assumes a fixed 12-byte header — the CSRC
// list, header extension, and padding are all sized from the packet's own
// declared fields.
func ParseRTPHeader(packet []byte) (RTPHeader, error) {
	const fixedHeaderLen = 12
	if len(packet) < fixedHeaderLen {
		return RTPHeader{}, fmt.Errorf("processing: rtp packet too short: %d bytes", len(packet))
	}

	b0 := packet[0]
	b1 := packet[1]

	h := RTPHeader{
		Version:        b0 >> 6,
		Padding:        b0&0x20 != 0,
		Extension:      b0&0x10 != 0,
		CSRCCount:      b0 & 0x0F,
		Marker:         b1&0x80 != 0,
		PayloadType:    b1 & 0x7F,
		SequenceNumber: uint16(packet[2])<<8 | uint16(packet[3]),
		Timestamp:      uint32(packet[4])<<24 | uint32(packet[5])<<16 | uint32(packet[6])<<8 | uint32(packet[7]),
		SSRC:           uint32(packet[8])<<24 | uint32(packet[9])<<16 | uint32(packet[10])<<8 | uint32(packet[11]),
	}

	if h.Version != 2 {
		return RTPHeader{}, fmt.Errorf("processing: unsupported rtp version %d", h.Version)
	}

	offset := fixedHeaderLen

	csrcLen := int(h.CSRCCount) * 4
	if offset+csrcLen > len(packet) {
		return RTPHeader{}, fmt.Errorf("processing: rtp packet too short for %d CSRC entries", h.CSRCCount)
	}
	h.CSRC = make([]uint32, h.CSRCCount)
	for i := 0; i < int(h.CSRCCount); i++ {
		o := offset + i*4
		h.CSRC[i] = uint32(packet[o])<<24 | uint32(packet[o+1])<<16 | uint32(packet[o+2])<<8 | uint32(packet[o+3])
	}
	offset += csrcLen

	if h.Extension {
		// Header extension (RFC 3550 §5.3.1): 2 bytes profile id (ignored) +
		// 2 bytes length in 32-bit words, followed by that many words.
		if offset+4 > len(packet) {
			return RTPHeader{}, fmt.Errorf("processing: rtp packet too short for header extension")
		}
		extLenWords := int(packet[offset+2])<<8 | int(packet[offset+3])
		extTotal := 4 + extLenWords*4
		if offset+extTotal > len(packet) {
			return RTPHeader{}, fmt.Errorf("processing: rtp packet too short for declared header extension length")
		}
		offset += extTotal
	}

	if offset > len(packet) {
		return RTPHeader{}, fmt.Errorf("processing: rtp payload offset %d exceeds packet length %d", offset, len(packet))
	}

	end := len(packet)
	if h.Padding {
		if end == offset {
			return RTPHeader{}, fmt.Errorf("processing: rtp padding bit set but no payload bytes present")
		}
		padLen := int(packet[end-1])
		if padLen == 0 || offset+padLen > end {
			return RTPHeader{}, fmt.Errorf("processing: invalid rtp padding length %d", padLen)
		}
		end -= padLen
	}

	h.PayloadStart = offset
	h.PayloadEnd = end
	return h, nil
}
