package processing

import "time"

const (
	h265NALTypeAP   = 48
	h265NALTypeFU   = 49
	h265NALTypePACI = 50
)

// H265Depacketizer reconstructs HEVC access units from RTP payloads per RFC 7798.
// It supports single NAL units, Aggregation Packets (AP, type 48), and
// Fragmentation Units (FU, type 49). PACI (type 50) is intentionally rejected
// and counted. DONL/DOND are not supported because GEO CAM does not negotiate
// sprop-max-don-diff; Hikvision recorder substreams use the common no-DON form.
type H265Depacketizer struct {
	haveLastSeq bool
	lastSeq     uint16

	auNALUs      [][]byte
	auBytes      int
	auReceivedAt time.Time
	auTimestamp  uint32
	auOpen       bool
	auCorrupt    bool
	auOverflow   bool

	fuActive bool
	fuHeader [2]byte
	fuBuf    []byte

	ready []*AccessUnit

	IncompleteAUsDropped int64
	ReassemblyErrors     int64
	UnsupportedNALTypes  int64
	OversizedAUsDropped  int64
}

func NewH265Depacketizer() *H265Depacketizer { return &H265Depacketizer{} }

func (d *H265Depacketizer) Stats() DepacketizerStats {
	return DepacketizerStats{
		IncompleteAUsDropped: d.IncompleteAUsDropped,
		ReassemblyErrors:     d.ReassemblyErrors,
		UnsupportedNALTypes:  d.UnsupportedNALTypes,
		OversizedAUsDropped:  d.OversizedAUsDropped,
	}
}

func (d *H265Depacketizer) Push(hdr RTPHeader, payload []byte, recvAt time.Time) (*AccessUnit, error) {
	if len(payload) < 2 {
		d.ReassemblyErrors++
		return d.popReady(), nil
	}

	if d.checkSequenceGap(hdr.SequenceNumber) {
		d.fuActive = false
		d.fuBuf = nil
		if d.auOpen {
			d.auCorrupt = true
		}
	}
	if d.auOpen && hdr.Timestamp != d.auTimestamp {
		if au := d.closeAU(); au != nil {
			d.ready = append(d.ready, au)
		}
	}

	nalType := (payload[0] >> 1) & 0x3f
	switch {
	case nalType <= 47:
		d.appendNALU(payload, hdr.Timestamp, recvAt)
		d.maybeCloseOnMarker(hdr)
	case nalType == h265NALTypeAP:
		nalus, ok := parseH265AP(payload)
		if !ok {
			d.ReassemblyErrors++
			d.maybeCloseOnMarker(hdr)
			break
		}
		for _, n := range nalus {
			d.appendNALU(n, hdr.Timestamp, recvAt)
		}
		d.maybeCloseOnMarker(hdr)
	case nalType == h265NALTypeFU:
		d.handleFU(payload, hdr, recvAt)
	case nalType == h265NALTypePACI:
		d.UnsupportedNALTypes++
		d.maybeCloseOnMarker(hdr)
	default:
		d.UnsupportedNALTypes++
		d.maybeCloseOnMarker(hdr)
	}
	return d.popReady(), nil
}

func (d *H265Depacketizer) maybeCloseOnMarker(hdr RTPHeader) {
	if !hdr.Marker {
		return
	}
	if au := d.closeAU(); au != nil {
		d.ready = append(d.ready, au)
	}
}

func (d *H265Depacketizer) popReady() *AccessUnit {
	if len(d.ready) == 0 {
		return nil
	}
	au := d.ready[0]
	d.ready = d.ready[1:]
	return au
}

func (d *H265Depacketizer) checkSequenceGap(seq uint16) bool {
	if !d.haveLastSeq {
		d.haveLastSeq = true
		d.lastSeq = seq
		return false
	}
	expected := d.lastSeq + 1
	d.lastSeq = seq
	return seq != expected
}

func (d *H265Depacketizer) appendNALU(nalu []byte, timestamp uint32, recvAt time.Time) {
	if !d.auOpen {
		d.auOpen = true
		d.auReceivedAt = recvAt
		d.auTimestamp = timestamp
	}
	if d.auOverflow {
		return
	}
	if d.auBytes+len(nalu) > maxAccessUnitBytes || len(d.auNALUs) >= maxAccessUnitNALUs {
		d.abandonOversizedAU()
		return
	}
	d.auNALUs = append(d.auNALUs, append([]byte(nil), nalu...))
	d.auBytes += len(nalu)
}

func (d *H265Depacketizer) abandonOversizedAU() {
	d.auOpen = true
	d.auOverflow = true
	d.auNALUs = nil
	d.auBytes = 0
}

func (d *H265Depacketizer) closeAU() *AccessUnit {
	defer d.resetAUState()
	if d.auOverflow {
		d.OversizedAUsDropped++
		return nil
	}
	if d.auCorrupt || len(d.auNALUs) == 0 {
		if d.auCorrupt {
			d.IncompleteAUsDropped++
		}
		return nil
	}
	return &AccessUnit{NALUs: d.auNALUs, ReceivedAt: d.auReceivedAt}
}

func (d *H265Depacketizer) resetAUState() {
	d.auNALUs = nil
	d.auBytes = 0
	d.auOpen = false
	d.auCorrupt = false
	d.auOverflow = false
	d.auTimestamp = 0
}

func (d *H265Depacketizer) handleFU(payload []byte, hdr RTPHeader, recvAt time.Time) {
	// 2-byte HEVC payload header + 1-byte FU header + fragment.
	if len(payload) < 4 {
		d.ReassemblyErrors++
		d.maybeCloseOnMarker(hdr)
		return
	}
	fuHeader := payload[2]
	start := fuHeader&0x80 != 0
	end := fuHeader&0x40 != 0
	fuType := fuHeader & 0x3f
	fragment := payload[3:]

	if start {
		if d.fuActive {
			d.ReassemblyErrors++
		}
		d.fuActive = true
		// Reconstruct the original two-byte HEVC NAL header. Preserve F,
		// nuh_layer_id and temporal_id_plus1 from the FU payload header,
		// replacing only the 6-bit nal_unit_type.
		d.fuHeader[0] = (payload[0] & 0x81) | (fuType << 1)
		d.fuHeader[1] = payload[1]
		d.fuBuf = append(d.fuBuf[:0], fragment...)
	} else {
		if !d.fuActive {
			d.ReassemblyErrors++
			d.maybeCloseOnMarker(hdr)
			return
		}
		if len(d.fuBuf)+len(fragment)+2 > maxAccessUnitBytes {
			d.fuActive = false
			d.fuBuf = nil
			d.ReassemblyErrors++
			d.abandonOversizedAU()
			d.maybeCloseOnMarker(hdr)
			return
		}
		d.fuBuf = append(d.fuBuf, fragment...)
	}

	if end {
		if !d.fuActive {
			d.ReassemblyErrors++
			d.maybeCloseOnMarker(hdr)
			return
		}
		nalu := make([]byte, 2+len(d.fuBuf))
		nalu[0], nalu[1] = d.fuHeader[0], d.fuHeader[1]
		copy(nalu[2:], d.fuBuf)
		d.fuActive = false
		d.fuBuf = nil
		d.appendNALU(nalu, hdr.Timestamp, recvAt)
		d.maybeCloseOnMarker(hdr)
	}
}

func parseH265AP(payload []byte) ([][]byte, bool) {
	// AP payload header is two bytes. With sprop-max-don-diff == 0 (our
	// supported mode) the first NALU length starts immediately at offset 2.
	offset := 2
	var nalus [][]byte
	for offset+2 <= len(payload) {
		sz := int(payload[offset])<<8 | int(payload[offset+1])
		offset += 2
		if sz <= 0 || offset+sz > len(payload) {
			return nil, false
		}
		nalus = append(nalus, append([]byte(nil), payload[offset:offset+sz]...))
		offset += sz
	}
	return nalus, len(nalus) > 0 && offset == len(payload)
}
