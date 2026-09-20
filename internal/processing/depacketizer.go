package processing

import (
	"errors"
	"time"
)

const (
	nalTypeSTAPA = 24
	nalTypeFUA   = 28
)

// maxAccessUnitBytes bounds the total size of one reassembled access unit, and
// maxAccessUnitNALUs bounds how many NAL units it may aggregate. Both are
// protocol-safety ceilings, not tuning knobs, and they follow the same pattern
// as maxPendingTimes and stderrTailMax in ffmpeg_decoder.go: an accumulator fed
// by the network must have a ceiling, or a broken or hostile sender can make it
// grow until the process runs out of memory.
//
// Before these existed, two accumulators grew with no bound at all:
// auNALUs grew for every NAL-1..23 packet carrying a constant RTP timestamp
// and no marker bit, and fuBuf grew for every FU-A fragment of a run that
// never set its end bit with contiguous sequence numbers. Neither needs a
// malicious camera: a sender that simply never signals a frame boundary is
// enough, and one such pipeline exists per camera.
//
// 8 MiB is deliberately far above any real access unit at the resolutions this
// Edge ingests (a 4K H.264 keyframe is on the order of 1-2 MB, and the default
// stream role is the camera substream), so a legitimate frame is never
// affected, while the ceiling keeps worst-case reassembly memory per camera
// bounded and small. 4096 NAL units is likewise far above any real frame.
const (
	maxAccessUnitBytes = 8 << 20
	maxAccessUnitNALUs = 4096
)

var (
	errSTAPABounds = errors.New("processing: stap-a nalu size exceeds remaining payload")
	errSTAPAEmpty  = errors.New("processing: stap-a with no aggregated NALUs")
)

// H264Depacketizer reconstructs H.264 access units from RTP packets (RFC
// 6184): single NALU, FU-A fragmentation, and STAP-A aggregation. STAP-B,
// MTAP16/24, FU-B and reserved types are explicitly unsupported — counted
// via UnsupportedNALTypes, never silently dropped without a trace.
//
// One instance is owned by exactly one camera pipeline (stateful across
// packets for FU-A reassembly and sequence tracking) — never shared across
// cameras or goroutines.
//
// Access-unit boundaries are primarily the RTP marker bit (RFC 6184 §5.1).
// ponytail: some cameras don't reliably set the marker bit, so a change in
// RTP timestamp while an AU is open is used as a fallback boundary. Ceiling:
// this heuristic can occasionally split/merge AUs on a camera with unusual
// timestamp behavior — delete it if TC70 validation shows the marker bit
// alone is reliable.
//
// Packet loss handling covers the whole access unit, not just FU-A
// reassembly: any RTP sequence gap detected while an AU is in progress
// marks it corrupt, and it is discarded whole at the next boundary rather
// than delivered partially to the decoder (see IncompleteAUsDropped).
type H264Depacketizer struct {
	haveLastSeq bool
	lastSeq     uint16

	auNALUs      [][]byte
	auBytes      int
	auReceivedAt time.Time
	auTimestamp  uint32
	auOpen       bool
	auCorrupt    bool
	// auOverflow records that this access unit was abandoned because it
	// exceeded a reassembly ceiling. While it is set, appendNALU is a no-op,
	// so the accumulator holds no memory until the boundary closes the AU.
	auOverflow bool

	fuActive    bool
	fuNALHeader byte
	fuBuf       []byte

	ready []*AccessUnit

	// IncompleteAUsDropped counts access units discarded whole because a
	// sequence gap was detected while they were open.
	IncompleteAUsDropped int64
	// ReassemblyErrors counts malformed/orphan fragments and aggregation
	// packets that could not be parsed.
	ReassemblyErrors int64
	// UnsupportedNALTypes counts NAL types this depacketizer does not
	// implement (FU-B, MTAP, STAP-B, reserved types).
	UnsupportedNALTypes int64
	// OversizedAUsDropped counts access units abandoned because reassembly
	// exceeded maxAccessUnitBytes or maxAccessUnitNALUs. It is separate from
	// IncompleteAUsDropped (packet loss) on purpose: "this sender never
	// closes a frame" and "packets were lost" need different operator
	// responses, and folding them together would hide a wedged camera behind
	// a packet-loss number.
	OversizedAUsDropped int64
}

// NewH264Depacketizer creates a depacketizer with fresh reassembly state.
func NewH264Depacketizer() *H264Depacketizer {
	return &H264Depacketizer{}
}

// Push feeds one already-parsed RTP header and its payload (video channel
// only — the caller must have already filtered out RTCP). recvAt is the
// wall-clock ingest time, used as AccessUnit.ReceivedAt.
//
// It never returns an error for malformed per-packet data: those are
// tracked via the depacketizer's counters and the offending fragment/packet
// is skipped, so one bad packet never stalls the stream. A non-nil
// AccessUnit is returned exactly when a boundary completed a valid,
// non-corrupt access unit.
func (d *H264Depacketizer) Push(hdr RTPHeader, payload []byte, recvAt time.Time) (*AccessUnit, error) {
	if len(payload) == 0 {
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

	nalType := payload[0] & 0x1F
	switch {
	case nalType == nalTypeSTAPA:
		nalus, err := parseSTAPA(payload)
		if err != nil {
			d.ReassemblyErrors++
			d.maybeCloseOnMarker(hdr)
			break
		}
		for _, n := range nalus {
			d.appendNALU(n, hdr.Timestamp, recvAt)
		}
		d.maybeCloseOnMarker(hdr)
	case nalType == nalTypeFUA:
		d.handleFUA(payload, hdr, recvAt)
	case nalType >= 1 && nalType <= 23:
		d.appendNALU(payload, hdr.Timestamp, recvAt)
		d.maybeCloseOnMarker(hdr)
	default:
		d.UnsupportedNALTypes++
		d.maybeCloseOnMarker(hdr)
	}

	return d.popReady(), nil
}

// maybeCloseOnMarker closes the in-progress access unit if hdr carries the
// RTP marker bit — the frame boundary signal — regardless of whether the
// NAL unit this particular packet carried was itself usable. A marker on a
// packet whose payload we couldn't reassemble still means "this is where
// the frame ends": whatever was accumulated (or nothing, or a corrupt run)
// must be resolved now, never left open indefinitely.
func (d *H264Depacketizer) maybeCloseOnMarker(hdr RTPHeader) {
	if !hdr.Marker {
		return
	}
	if au := d.closeAU(); au != nil {
		d.ready = append(d.ready, au)
	}
}

func (d *H264Depacketizer) popReady() *AccessUnit {
	if len(d.ready) == 0 {
		return nil
	}
	au := d.ready[0]
	d.ready = d.ready[1:]
	return au
}

func (d *H264Depacketizer) checkSequenceGap(seq uint16) bool {
	if !d.haveLastSeq {
		d.haveLastSeq = true
		d.lastSeq = seq
		return false
	}
	expected := d.lastSeq + 1 // uint16 wraparound is intentional
	d.lastSeq = seq
	return seq != expected
}

func (d *H264Depacketizer) appendNALU(nalu []byte, timestamp uint32, recvAt time.Time) {
	if !d.auOpen {
		d.auOpen = true
		d.auReceivedAt = recvAt
		d.auTimestamp = timestamp
	}
	// Once the AU has been abandoned there is nothing to accumulate: staying
	// a no-op keeps the accumulator at zero bytes until the boundary arrives,
	// instead of freeing and immediately re-growing it packet after packet.
	if d.auOverflow {
		return
	}
	if d.auBytes+len(nalu) > maxAccessUnitBytes || len(d.auNALUs) >= maxAccessUnitNALUs {
		d.abandonOversizedAU()
		return
	}
	d.auNALUs = append(d.auNALUs, nalu)
	d.auBytes += len(nalu)
}

// abandonOversizedAU throws away an access unit that exceeded a reassembly
// ceiling. The frame cannot be decoded from a truncated AU, and these are live
// media frames — the safely-droppable class — so dropping it is correct; the
// point of the ceiling is that dropping it must be bounded AND counted rather
// than becoming an out-of-memory kill. Opening the AU if it was not open
// already keeps the accounting honest: data really was received for a frame
// that has now been discarded.
func (d *H264Depacketizer) abandonOversizedAU() {
	d.auOpen = true
	d.auOverflow = true
	d.auNALUs = nil
	d.auBytes = 0
}

func (d *H264Depacketizer) closeAU() *AccessUnit {
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

func (d *H264Depacketizer) resetAUState() {
	d.auNALUs = nil
	d.auBytes = 0
	d.auOpen = false
	d.auCorrupt = false
	d.auOverflow = false
	d.auTimestamp = 0
}

func (d *H264Depacketizer) handleFUA(payload []byte, hdr RTPHeader, recvAt time.Time) {
	if len(payload) < 2 {
		d.ReassemblyErrors++
		return
	}
	fuIndicator := payload[0]
	fuHeader := payload[1]
	start := fuHeader&0x80 != 0
	end := fuHeader&0x40 != 0
	fragType := fuHeader & 0x1F

	if start {
		if d.fuActive {
			// Previous fragment run never saw its end bit — discard it.
			d.ReassemblyErrors++
		}
		d.fuActive = true
		d.fuNALHeader = (fuIndicator & 0xE0) | fragType
		d.fuBuf = append([]byte{}, payload[2:]...)
	} else {
		if !d.fuActive {
			d.ReassemblyErrors++
			d.maybeCloseOnMarker(hdr)
			return
		}
		// Bound the reassembly buffer: a fragment run that never sets its end
		// bit would otherwise grow this slice for as long as the sender keeps
		// going, which is a memory-exhaustion path reachable from the wire.
		// Abandoning the run leaves the frame undecodable either way, since a
		// truncated NAL unit cannot be decoded — so the run is dropped, the
		// AU it belonged to is abandoned, and both are counted.
		if len(d.fuBuf)+len(payload)-2 > maxAccessUnitBytes {
			d.fuActive = false
			d.fuBuf = nil
			d.ReassemblyErrors++
			d.abandonOversizedAU()
			d.maybeCloseOnMarker(hdr)
			return
		}
		d.fuBuf = append(d.fuBuf, payload[2:]...)
	}

	if end {
		if !d.fuActive {
			d.ReassemblyErrors++
			d.maybeCloseOnMarker(hdr)
			return
		}
		nalu := append([]byte{d.fuNALHeader}, d.fuBuf...)
		d.fuActive = false
		d.fuBuf = nil
		d.appendNALU(nalu, hdr.Timestamp, recvAt)
		d.maybeCloseOnMarker(hdr)
	}
}

// parseSTAPA unpacks a STAP-A aggregation packet (RFC 6184 §5.7.1) into its
// constituent NAL units, each prefixed in the wire format by a 2-byte
// big-endian length.
func parseSTAPA(payload []byte) ([][]byte, error) {
	offset := 1 // skip the STAP-A header byte itself
	var nalus [][]byte
	for offset+2 <= len(payload) {
		size := int(payload[offset])<<8 | int(payload[offset+1])
		offset += 2
		if size <= 0 || offset+size > len(payload) {
			return nil, errSTAPABounds
		}
		nalus = append(nalus, payload[offset:offset+size])
		offset += size
	}
	if len(nalus) == 0 {
		return nil, errSTAPAEmpty
	}
	return nalus, nil
}
