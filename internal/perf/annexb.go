package perf

import (
	"errors"
	"fmt"
)

// H.264 NAL unit types this harness needs to name. The full table is ITU-T
// H.264 Table 7-1; only the ones that matter for splitting an Annex-B byte
// stream into access units are listed.
const (
	NALTypeSlice    = 1  // non-IDR coded slice
	NALTypeSliceIDR = 5  // IDR coded slice
	NALTypeSEI      = 6  // supplemental enhancement information
	NALTypeSPS      = 7  // sequence parameter set
	NALTypePPS      = 8  // picture parameter set
	NALTypeAUD      = 9  // access unit delimiter
	NALTypeSTAPA    = 24 // RFC 6184 STAP-A (RTP only, never in a bitstream)
	NALTypeFUA      = 28 // RFC 6184 FU-A (RTP only, never in a bitstream)
)

// NALUnit is one H.264 NAL unit with its Annex-B start code removed.
type NALUnit struct {
	Type byte
	// Bytes is the full NAL unit including its one-byte NAL header, exactly
	// as it must be handed to processing.VideoDecoder.Push (which re-adds the
	// Annex-B start code itself) and to the RTP packetizer.
	Bytes []byte
}

// IsVCL reports whether this NAL unit carries coded picture data (H.264
// Table 7-1: types 1..5).
func (n NALUnit) IsVCL() bool { return n.Type >= 1 && n.Type <= 5 }

// AccessUnit is one coded picture: every NAL unit from one access unit
// delimiter up to (but not including) the next, and at minimum the VCL NAL
// unit(s) that carry the picture's slices.
type AccessUnit struct {
	NALUs []NALUnit
}

// ErrNoAccessUnits is returned when a bitstream yields no access unit at all
// (empty, truncated, or not H.264).
var ErrNoAccessUnits = errors.New("perf: no H.264 access units in bitstream")

// SplitAnnexB splits an Annex-B byte stream (start codes 00 00 01 or 00 00 00
// 01) into NAL units. Any leading bytes before the first start code are
// skipped, and a NAL unit that would be empty is dropped — so a stream that
// begins with a 4-byte start code does not yield a phantom zero-length NAL.
//
// Trailing zero bytes that precede the next start code are left attached to
// the preceding NAL unit rather than trimmed: H.264 allows cabac_zero_word
// padding there, and neither ffmpeg nor the RTP path cares. This keeps the
// splitter conservative — it never removes bytes it cannot prove are padding.
func SplitAnnexB(b []byte) []NALUnit {
	var nals []NALUnit
	i, n := 0, len(b)
	for i < n {
		sc := startCodeLenAt(b, i)
		if sc == 0 {
			i++
			continue
		}
		start := i + sc
		end := start
		for end < n && startCodeLenAt(b, end) == 0 {
			end++
		}
		if end > start {
			nals = append(nals, NALUnit{Type: b[start] & 0x1F, Bytes: b[start:end]})
		}
		i = end
	}
	return nals
}

// startCodeLenAt reports the length of the Annex-B start code beginning at i,
// or 0 if there is none.
func startCodeLenAt(b []byte, i int) int {
	if i+3 <= len(b) && b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
		return 3
	}
	if i+4 <= len(b) && b[i] == 0 && b[i+1] == 0 && b[i+2] == 0 && b[i+3] == 1 {
		return 4
	}
	return 0
}

// GroupAccessUnits groups NAL units into access units.
//
// The primary rule is the access unit delimiter (NAL type 9): every AUD
// starts a new access unit. That is the boundary H.264 itself defines and it
// is correct regardless of how many slices a picture is split into, which
// matters because the clip this harness generates (see DefaultClipSpec) uses
// several slices per frame — exactly like a real encoder configured for
// low-latency sliced threading.
//
// When the bitstream carries no AUD at all the fallback is one access unit
// per VCL NAL unit, which is only correct for single-slice pictures. Callers
// must not trust that fallback blindly: VerifyAccessUnitCount checks the
// result against an independent frame count and refuses to report numbers
// when the two disagree, rather than silently measuring the wrong unit.
func GroupAccessUnits(nals []NALUnit) ([]AccessUnit, bool) {
	sawAUD := false
	for _, nal := range nals {
		if nal.Type == NALTypeAUD {
			sawAUD = true
			break
		}
	}

	var aus []AccessUnit
	var cur *AccessUnit
	for _, nal := range nals {
		if sawAUD {
			if nal.Type == NALTypeAUD {
				if cur != nil {
					aus = append(aus, *cur)
				}
				cur = &AccessUnit{}
			} else if cur == nil {
				// NAL units preceding the first delimiter: keep them with
				// the picture they belong to rather than dropping them.
				cur = &AccessUnit{}
			}
			cur.NALUs = append(cur.NALUs, nal)
			continue
		}

		// No delimiter anywhere in the stream: one access unit per VCL NAL
		// unit. Only correct for single-slice pictures — see the doc comment.
		if cur == nil {
			cur = &AccessUnit{}
		}
		cur.NALUs = append(cur.NALUs, nal)
		if nal.IsVCL() {
			aus = append(aus, *cur)
			cur = nil
		}
	}
	if cur != nil && len(cur.NALUs) > 0 {
		aus = append(aus, *cur)
	}
	return aus, sawAUD
}

// ParseAnnexBStream splits and groups in one step.
func ParseAnnexBStream(b []byte) ([]AccessUnit, bool, error) {
	aus, sawAUD := GroupAccessUnits(SplitAnnexB(b))
	if len(aus) == 0 {
		return nil, sawAUD, ErrNoAccessUnits
	}
	return aus, sawAUD, nil
}

// ParameterSets returns the first SPS and PPS found in aus, in the raw
// (start-code-free) form processing.FFmpegDecoder wants for its sprop
// injection and the SDP wants as base64. Missing sets come back nil.
func ParameterSets(aus []AccessUnit) (sps, pps []byte) {
	for _, au := range aus {
		for _, nal := range au.NALUs {
			switch nal.Type {
			case NALTypeSPS:
				if sps == nil {
					sps = nal.Bytes
				}
			case NALTypePPS:
				if pps == nil {
					pps = nal.Bytes
				}
			}
		}
	}
	return sps, pps
}

// VerifyAccessUnitCount cross-checks the access units derived from the raw
// bitstream against an independently obtained coded-frame count (ffprobe
// -count_frames, in practice). It returns an error rather than a warning:
// a mismatch means the harness would report "decode FPS" over the wrong
// denominator, which is exactly the kind of invented number Hito X forbids.
func VerifyAccessUnitCount(aus []AccessUnit, codedFrames int64) error {
	if codedFrames <= 0 {
		return fmt.Errorf("perf: independent coded-frame count is %d, refusing to measure", codedFrames)
	}
	if int64(len(aus)) != codedFrames {
		return fmt.Errorf("perf: bitstream parsed into %d access units but ffprobe counted %d coded frames: "+
			"access-unit boundary derivation is unreliable for this bitstream", len(aus), codedFrames)
	}
	return nil
}
