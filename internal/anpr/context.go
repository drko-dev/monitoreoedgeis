package anpr

import "github.com/drko-dev/monitoreoedgeis/internal/processing"

// PreEventContext returns up to maxFrames frames from history that were
// decoded at or before triggerSeq, oldest-first, most-recent-last. It is a
// thin adapter over the EXISTING processing.FrameHistory / RingBuffer
// (Manager.FrameHistory), reusing its safe, already-bounded Snapshot()
// read surface rather than creating a second frame-history ring buffer
// (spec item 17).
//
// history may be nil (no ring configured / camera not found) — returns nil
// then, never panics.
//
// Extension point (documented per spec item 17's fallback instruction):
// RingBuffer.Snapshot() copies the WHOLE ring on every call, which is fine
// at ring sizes this pipeline already uses (single-digit-to-low-tens of
// frames) but does not expose a "give me the N frames immediately before
// seq X" query directly — this adapter does that filtering here instead of
// asking processing to add a new method, so the ring buffer's encapsulation
// is not broken just to serve this PREP milestone. If a future milestone
// needs cheaper access (e.g. very large rings), the right change is a
// bounded lookup method on RingBuffer itself, not a second buffer here.
func PreEventContext(history processing.FrameHistory, triggerSeq uint64, maxFrames int) []processing.Frame {
	if history == nil || maxFrames <= 0 {
		return nil
	}
	all := history.Snapshot()
	eligible := make([]processing.Frame, 0, len(all))
	for _, f := range all {
		if f.Seq <= triggerSeq {
			eligible = append(eligible, f)
		}
	}
	if len(eligible) > maxFrames {
		eligible = eligible[len(eligible)-maxFrames:]
	}
	return eligible
}
