package anpr

import (
	"fmt"
	"sync"
	"time"
)

// BurstState is CandidateBurst's lifecycle. Once CLOSED or EXPIRED it never
// transitions again — a "reopen" is always a new burst with a new BurstID
// (spec items 11/16).
type BurstState string

const (
	BurstOpen    BurstState = "OPEN"
	BurstFull    BurstState = "FULL"
	BurstExpired BurstState = "EXPIRED"
	BurstClosed  BurstState = "CLOSED"
)

// terminal reports whether s never accepts another frame.
func (s BurstState) terminal() bool { return s == BurstExpired || s == BurstClosed }

// BurstOutcome is what BurstManager.Process did with one VehicleCandidate.
type BurstOutcome string

const (
	OutcomeAccepted        BurstOutcome = "accepted"
	OutcomeDuplicate       BurstOutcome = "duplicate"
	OutcomeStaleOutOfOrder BurstOutcome = "stale_out_of_order"
	OutcomeCapacityFrames  BurstOutcome = "capacity_frames"
	OutcomeCapacityBursts  BurstOutcome = "capacity_bursts"
	OutcomeInvalidBBox     BurstOutcome = "invalid_bbox"
)

// FrameRecord is one accepted candidate frame within a burst. Bounded: a
// burst never stores more than its configured overflow cap (see
// newOverflowCap).
type FrameRecord struct {
	CandidateID PlateCandidateID
	FrameSeq    uint64
	Timestamp   time.Time
	Quality     QualityHints
}

// CandidateBurst groups the plate candidates for one (camera, group) window
// — spec item 11. All fields are read-only to callers outside this package;
// mutate only through BurstManager.
type CandidateBurst struct {
	BurstID     string
	CameraKey   string
	TrackID     string
	groupKey    string
	StartedAt   time.Time
	ExpiresAt   time.Time
	MaxFrames   int
	FramesAdded int
	State       BurstState

	selection   FrameSelectionPolicy
	frames      []FrameRecord // bounded by overflow cap, see newOverflowCap
	overflowCap int
	highestSeq  uint64
	hasHighest  bool
}

// FinalFrames returns the frames this burst will actually forward,
// respecting its FrameSelectionPolicy and MaxFrames bound (spec item 13/14):
// SelectFirst simply returns what was accepted (already capped at
// MaxFrames on the way in); SelectTopNQuality ranks everything retained
// (bounded by overflowCap) and returns the top MaxFrames. Never returns
// more than MaxFrames entries.
func (b *CandidateBurst) FinalFrames() []FrameRecord {
	if b.selection != SelectTopNQuality || len(b.frames) <= b.MaxFrames {
		if len(b.frames) > b.MaxFrames {
			return append([]FrameRecord(nil), b.frames[:b.MaxFrames]...)
		}
		return append([]FrameRecord(nil), b.frames...)
	}
	byKey := make(map[string]QualityHints, len(b.frames))
	byID := make(map[string]FrameRecord, len(b.frames))
	for _, f := range b.frames {
		byKey[f.CandidateID] = f.Quality
		byID[f.CandidateID] = f
	}
	ranked := RankQuality(byKey)
	top := TopN(ranked, b.MaxFrames)
	out := make([]FrameRecord, 0, len(top))
	for _, id := range top {
		out = append(out, byID[id])
	}
	return out
}

// newOverflowCap bounds how many candidate frames a SelectTopNQuality burst
// retains before final selection, so "wait to rank" never means unbounded
// memory. Ponytail: fixed 3x multiplier, generous enough for PREP's
// MaxFramesPerBurst scale (single-digit to low tens); revisit with a
// dedicated config knob if a deployment needs a materially wider window.
func newOverflowCap(maxFrames int) int {
	if maxFrames <= 0 {
		return 1
	}
	overflow := maxFrames * 3
	if overflow < maxFrames {
		return maxFrames
	}
	return overflow
}

// BurstManager owns every open/recently-closed CandidateBurst, deduplication
// state and per-camera capacity accounting. All time comes from an
// injectable clock (spec item 15: "sin sleeps, clock/timestamp
// inyectable") — there is no background goroutine or timer; expiry is
// evaluated lazily whenever Process or Cleanup is called.
type BurstManager struct {
	mu  sync.Mutex
	cfg Config
	now func() time.Time

	bursts       map[string]*CandidateBurst // keyed by groupKey; only the current (possibly terminal, until Cleanup) burst per group
	activeCount  map[string]int             // cameraKey -> count of non-terminal bursts
	burstCounter uint64

	dedupe      map[string]struct{}
	dedupeOrder []string // FIFO eviction order, bounds dedupe map size
}

// NewBurstManager creates a BurstManager. now defaults to time.Now when
// nil.
func NewBurstManager(cfg Config, now func() time.Time) *BurstManager {
	if now == nil {
		now = time.Now
	}
	return &BurstManager{
		cfg:         cfg,
		now:         now,
		bursts:      make(map[string]*CandidateBurst),
		activeCount: make(map[string]int),
		dedupe:      make(map[string]struct{}),
	}
}

// Process ingests one VehicleCandidate: dedupes, applies out-of-order
// policy, opens a new burst or reuses the current one for its group, and
// enforces every capacity bound. It never blocks and never grows any map
// beyond its configured bound.
//
// Returns the burst the candidate was accepted into (nil if not accepted),
// the assigned candidate id (empty if not accepted) and the outcome.
func (m *BurstManager) Process(v VehicleCandidate) (*CandidateBurst, PlateCandidateID, BurstOutcome) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	m.cleanupExpiredLocked(now)

	if !v.VehicleBBox.Valid() {
		return nil, "", OutcomeInvalidBBox
	}

	dk := dedupeKey(v)
	if _, dup := m.dedupe[dk]; dup {
		return nil, "", OutcomeDuplicate
	}

	gk := groupKey(v.CameraKey, v.TrackID, v.CorrelationID)
	burst := m.bursts[gk]

	if burst == nil || burst.State.terminal() {
		if m.activeCount[v.CameraKey] >= m.cfg.MaxActiveBurstsPerCamera {
			return nil, "", OutcomeCapacityBursts
		}
		burst = m.newBurstLocked(gk, v, now)
		m.bursts[gk] = burst
		m.activeCount[v.CameraKey]++
	}

	if burst.State == BurstFull {
		return nil, "", OutcomeCapacityFrames
	}

	// Out-of-order policy (spec item 24): accept strictly-increasing
	// FrameSeq only. A seq at or below the highest already accepted is
	// stale and ignored — deterministic regardless of arrival order.
	if burst.hasHighest && v.FrameSeq <= burst.highestSeq {
		return nil, "", OutcomeStaleOutOfOrder
	}

	if len(burst.frames) >= burst.overflowCap {
		// Overflow cap reached even under SelectTopNQuality: treat as full
		// rather than growing memory further.
		burst.State = BurstFull
		return nil, "", OutcomeCapacityFrames
	}

	candidateID := NewCandidateID(v.CameraKey, v.FrameSeq, burst.FramesAdded, burst.BurstID)
	burst.frames = append(burst.frames, FrameRecord{
		CandidateID: candidateID,
		FrameSeq:    v.FrameSeq,
		Timestamp:   v.Timestamp,
		Quality: QualityHints{
			VehicleConfidence: v.VehicleConfidence,
		},
	})
	burst.FramesAdded++
	burst.highestSeq = v.FrameSeq
	burst.hasHighest = true

	if burst.FramesAdded >= burst.MaxFrames {
		burst.State = BurstFull
	}

	m.recordDedupeLocked(dk)

	return burst, candidateID, OutcomeAccepted
}

func (m *BurstManager) newBurstLocked(gk string, v VehicleCandidate, now time.Time) *CandidateBurst {
	m.burstCounter++
	return &CandidateBurst{
		BurstID:     fmt.Sprintf("%s-burst-%d", gk, m.burstCounter),
		CameraKey:   v.CameraKey,
		TrackID:     v.TrackID,
		groupKey:    gk,
		StartedAt:   now,
		ExpiresAt:   now.Add(m.cfg.BurstTTL),
		MaxFrames:   m.cfg.MaxFramesPerBurst,
		State:       BurstOpen,
		selection:   m.cfg.FrameSelection,
		overflowCap: newOverflowCap(m.cfg.MaxFramesPerBurst),
	}
}

func (m *BurstManager) recordDedupeLocked(dk string) {
	if _, exists := m.dedupe[dk]; exists {
		return
	}
	m.dedupe[dk] = struct{}{}
	m.dedupeOrder = append(m.dedupeOrder, dk)
	limit := m.cfg.DedupeCacheSize
	if limit <= 0 {
		limit = 1
	}
	for len(m.dedupeOrder) > limit {
		oldest := m.dedupeOrder[0]
		m.dedupeOrder = m.dedupeOrder[1:]
		delete(m.dedupe, oldest)
	}
}

// cleanupExpiredLocked transitions any OPEN/FULL burst past its ExpiresAt
// into EXPIRED and decrements that camera's active count. Must be called
// with m.mu held.
func (m *BurstManager) cleanupExpiredLocked(now time.Time) {
	for _, b := range m.bursts {
		if !b.State.terminal() && !now.Before(b.ExpiresAt) {
			b.State = BurstExpired
			m.activeCount[b.CameraKey]--
		}
	}
}

// Close explicitly closes the current burst for (cameraKey, trackID,
// correlationID)'s group, if one is open/full. A closed burst never
// reopens — the next Process call for the same group starts a brand new
// BurstID (spec item 16).
func (m *BurstManager) Close(cameraKey, trackID, correlationID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	gk := groupKey(cameraKey, trackID, correlationID)
	b := m.bursts[gk]
	if b == nil || b.State.terminal() {
		return
	}
	b.State = BurstClosed
	m.activeCount[b.CameraKey]--
}

// Cleanup removes terminal bursts (CLOSED/EXPIRED) from memory — spec item
// 20's "cleanup explícito" and B20. It does not touch the dedupe cache
// (bounded separately, see recordDedupeLocked).
func (m *BurstManager) Cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.cleanupExpiredLocked(now)
	for gk, b := range m.bursts {
		if b.State.terminal() {
			delete(m.bursts, gk)
		}
	}
}

// ActiveBurstCount returns how many non-terminal bursts cameraKey currently
// has.
func (m *BurstManager) ActiveBurstCount(cameraKey string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeCount[cameraKey]
}

// TotalBurstCount returns how many bursts (any state) are currently held in
// memory — used by tests to assert Cleanup actually releases memory.
func (m *BurstManager) TotalBurstCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.bursts)
}

// DedupeCacheLen returns the current size of the bounded dedupe cache.
func (m *BurstManager) DedupeCacheLen() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.dedupe)
}
