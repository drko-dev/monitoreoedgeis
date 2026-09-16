package processing

import (
	"sync"
	"testing"
)

func TestRingBuffer_WraparoundDropsOldest(t *testing.T) {
	rb := NewRingBuffer(3)
	for i := 0; i < 5; i++ {
		rb.Push(Frame{Seq: uint64(i)})
	}
	snap := rb.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot len = %d, want 3", len(snap))
	}
	// Oldest-first: frames 0 and 1 were overwritten, so 2,3,4 remain.
	want := []uint64{2, 3, 4}
	for i, f := range snap {
		if f.Seq != want[i] {
			t.Fatalf("snapshot[%d].Seq = %d, want %d", i, f.Seq, want[i])
		}
	}
	if rb.Dropped() != 2 {
		t.Fatalf("Dropped() = %d, want 2", rb.Dropped())
	}
}

func TestRingBuffer_UsageReflectsFillLevel(t *testing.T) {
	rb := NewRingBuffer(4)
	rb.Push(Frame{})
	rb.Push(Frame{})
	count, capacity := rb.Usage()
	if count != 2 || capacity != 4 {
		t.Fatalf("Usage() = %d/%d, want 2/4", count, capacity)
	}
}

func TestRingBuffer_ConcurrentPushIsSafe(t *testing.T) {
	rb := NewRingBuffer(16)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				rb.Push(Frame{Seq: uint64(id*1000 + i)})
			}
		}(g)
	}
	wg.Wait()
	count, capacity := rb.Usage()
	if count != capacity {
		t.Fatalf("Usage() = %d/%d, want full buffer %d/%d", count, capacity, capacity, capacity)
	}
}
