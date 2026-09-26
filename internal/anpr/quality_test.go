package anpr

import "testing"

func f(v float64) *float64 { return &v }

// B17: quality ranking deterministic — same inputs (regardless of map
// iteration order) always produce the same ranking.
func TestB17_QualityRankingDeterministic(t *testing.T) {
	items := map[string]QualityHints{
		"a": {VehicleConfidence: 0.5},
		"b": {VehicleConfidence: 0.9},
		"c": {VehicleConfidence: 0.9, PlateRegionConfidence: f(0.95)},
		"d": {VehicleConfidence: 0.1},
	}
	var first []string
	for i := 0; i < 20; i++ {
		ranked := RankQuality(items)
		if first == nil {
			first = ranked
			continue
		}
		if !equalStrings(first, ranked) {
			t.Fatalf("non-deterministic ranking: %v vs %v", first, ranked)
		}
	}
	if first[0] != "c" {
		t.Fatalf("expected highest plate-region confidence first, got %v", first)
	}
	if first[len(first)-1] != "d" {
		t.Fatalf("expected lowest confidence last, got %v", first)
	}
}

// B18: top-N selection bounded — never returns more than n, even with far
// more candidates than n.
func TestB18_TopNSelectionBounded(t *testing.T) {
	items := map[string]QualityHints{}
	for i := 0; i < 100; i++ {
		items[string(rune('A'+i%26))+string(rune(i))] = QualityHints{VehicleConfidence: float64(i) / 100}
	}
	ranked := RankQuality(items)
	top := TopN(ranked, 5)
	if len(top) != 5 {
		t.Fatalf("expected exactly 5, got %d", len(top))
	}
	// n larger than available never over-returns.
	small := RankQuality(map[string]QualityHints{"x": {}, "y": {}})
	if got := TopN(small, 10); len(got) != 2 {
		t.Fatalf("expected bounded to available items (2), got %d", len(got))
	}
	if got := TopN(ranked, 0); got != nil {
		t.Fatalf("expected nil for n<=0, got %v", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
