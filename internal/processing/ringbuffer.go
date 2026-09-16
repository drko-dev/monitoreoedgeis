package processing

import "sync"

// RingBuffer is a fixed-capacity, slice-backed circular buffer of Frame.
// Push never blocks and never grows memory unboundedly: once full, it
// overwrites the oldest entry — the realtime-preference policy required by
// H7 (never stall camera ingestion to preserve history).
type RingBuffer struct {
	mu       sync.Mutex
	buf      []Frame
	head     int // index of the oldest element
	count    int
	capacity int
	dropped  int64
}

// NewRingBuffer creates a RingBuffer holding at most capacity frames.
// capacity <= 0 is treated as 1 (a buffer of zero capacity has no useful
// meaning here).
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 1
	}
	return &RingBuffer{buf: make([]Frame, capacity), capacity: capacity}
}

// Push appends f, overwriting the oldest frame and incrementing Dropped if
// the buffer is already at capacity.
func (r *RingBuffer) Push(f Frame) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.count < r.capacity {
		idx := (r.head + r.count) % r.capacity
		r.buf[idx] = f
		r.count++
		return
	}
	// Full: overwrite the oldest slot and advance head.
	r.buf[r.head] = f
	r.head = (r.head + 1) % r.capacity
	r.dropped++
}

// Snapshot returns a copy of the buffer's current contents, oldest-first.
func (r *RingBuffer) Snapshot() []Frame {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Frame, r.count)
	for i := 0; i < r.count; i++ {
		out[i] = r.buf[(r.head+i)%r.capacity]
	}
	return out
}

// Usage returns (current count, capacity).
func (r *RingBuffer) Usage() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count, r.capacity
}

// Dropped returns how many frames have been overwritten because the buffer
// was full.
func (r *RingBuffer) Dropped() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}
