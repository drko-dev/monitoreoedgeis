package anpr

import "sort"

// QualityHints are technical hints only — never a final OCR/read-quality
// decision (that remains the SaaS/OCR pipeline's job). Optional fields are
// pointers so "not computed" is distinguishable from "computed as zero".
type QualityHints struct {
	Width, Height int
	Area          int
	// BrightnessHint/BlurHint/RelativePlateSize/PlateRegionConfidence are
	// optional adapter-supplied scores (no OpenCV dependency inside this
	// package — PREP never computes them itself).
	BrightnessHint        *float64
	BlurHint              *float64
	RelativePlateSize     *float64
	VehicleConfidence     float64
	PlateRegionConfidence *float64
}

// score returns a single deterministic ranking value from QualityHints.
// Higher is better. It is intentionally simple (PREP, not a tuned OCR
// heuristic): plate-region confidence first (when known), falling back to
// vehicle confidence, with area as a tiebreak-scale nudge so a larger crop
// of similar confidence ranks slightly higher.
func (q QualityHints) score() float64 {
	base := q.VehicleConfidence
	if q.PlateRegionConfidence != nil {
		base = *q.PlateRegionConfidence
	}
	return base + float64(q.Area)*1e-9
}

// rankable pairs a candidate with the identity needed for a fully
// deterministic sort (score ties broken by a stable, caller-supplied key —
// never map iteration order or insertion-order luck).
type rankable struct {
	key   string
	score float64
}

// RankQuality orders items (keyed by an arbitrary caller identity, e.g.
// PlateCandidateID) by descending QualityHints score, breaking ties by key
// ascending so the result is 100% deterministic for identical inputs
// regardless of input order (B17).
func RankQuality(items map[string]QualityHints) []string {
	rs := make([]rankable, 0, len(items))
	for k, q := range items {
		rs = append(rs, rankable{key: k, score: q.score()})
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].score != rs[j].score {
			return rs[i].score > rs[j].score
		}
		return rs[i].key < rs[j].key
	})
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.key
	}
	return out
}

// TopN returns at most n keys from ranked (RankQuality's output), never
// more — a bounded selection regardless of how many items were ranked
// (B18). n<=0 returns an empty slice.
func TopN(ranked []string, n int) []string {
	if n <= 0 || len(ranked) == 0 {
		return nil
	}
	if n > len(ranked) {
		n = len(ranked)
	}
	out := make([]string, n)
	copy(out, ranked[:n])
	return out
}
