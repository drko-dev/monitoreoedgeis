package cloudsink

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// fakeAnprSender implements both FrameSender and ANPRSender, so the SAME
// fake can be used to prove frame and ANPR candidates share one CloudSink
// (item #29: same limiter, same sink, never a parallel implementation).
type fakeAnprSender struct {
	mu sync.Mutex

	frameErr error
	anprErr  error

	frameCalls int
	anprCalls  int
	gotJPEGs   [][]byte
	gotMeta    [][]byte
}

func (f *fakeAnprSender) PostFrame(_ context.Context, _, _, _ string, _ uint64, _ time.Time, _ []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frameCalls++
	return f.frameErr
}

func (f *fakeAnprSender) PostANPRCandidate(_ context.Context, deviceID, credential string, metadataJSON, cropJPEG []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.anprCalls++
	f.gotMeta = append(f.gotMeta, append([]byte(nil), metadataJSON...))
	f.gotJPEGs = append(f.gotJPEGs, append([]byte(nil), cropJPEG...))
	if deviceID == "" || credential == "" {
		return fmt.Errorf("missing device credentials")
	}
	return f.anprErr
}

func (f *fakeAnprSender) anprCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.anprCalls
}

func candidateJSON(t *testing.T, id string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]string{"candidate_id": id, "schema_version": "anpr_candidate_v1"})
	if err != nil {
		t.Fatalf("marshal test candidate: %v", err)
	}
	return b
}

func TestEnqueueANPRCandidate_UploadOK_NeverBuffers(t *testing.T) {
	sender := &fakeAnprSender{}
	s, _ := newBufferedSink(t, sender, 10)

	err := s.EnqueueANPRCandidate("cam-1", time.Now(), candidateJSON(t, "c1"), []byte("crop-bytes"))
	if err != nil {
		t.Fatalf("EnqueueANPRCandidate() error = %v", err)
	}
	if sender.anprCallCount() != 1 {
		t.Fatalf("anprCalls = %d, want 1", sender.anprCallCount())
	}
	if got := s.CloudBufferStats().BufferedFrames; got != 0 {
		t.Fatalf("BufferedFrames = %d, want 0 (upload succeeded)", got)
	}
}

func TestEnqueueANPRCandidate_RecoverableError_BuffersThenReplays(t *testing.T) {
	drainPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { drainPollInterval = 2 * time.Second })

	sender := &fakeAnprSender{anprErr: transport.ErrSaaSUnavailable}
	s, _ := newBufferedSink(t, sender, 10)

	replayed := make(chan struct{}, 1)
	s.afterReplay = func() { replayed <- struct{}{} }

	if err := s.EnqueueANPRCandidate("cam-1", time.Now(), candidateJSON(t, "c1"), []byte("crop-bytes")); err != nil {
		t.Fatalf("EnqueueANPRCandidate() error = %v, want nil (buffered)", err)
	}
	if got := s.CloudBufferStats().BufferedFrames; got != 1 {
		t.Fatalf("BufferedFrames = %d, want 1", got)
	}

	sender.mu.Lock()
	sender.anprErr = nil
	sender.mu.Unlock()

	select {
	case <-replayed:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for ANPR candidate replay")
	}

	if got := s.CloudBufferStats().BufferedFrames; got != 0 {
		t.Fatalf("BufferedFrames after replay = %d, want 0", got)
	}
	if sender.anprCallCount() != 2 { // 1 failed direct attempt + 1 successful replay
		t.Fatalf("anprCalls = %d, want 2", sender.anprCallCount())
	}
	if len(sender.gotMeta) == 0 || string(sender.gotMeta[len(sender.gotMeta)-1]) == "" {
		t.Fatal("expected the replayed candidate's metadata JSON to survive the buffer round-trip")
	}
}

func TestEnqueueANPRCandidate_NonRecoverableError_NeverBuffers(t *testing.T) {
	sender := &fakeAnprSender{anprErr: transport.ErrUnauthorized}
	s, _ := newBufferedSink(t, sender, 10)

	err := s.EnqueueANPRCandidate("cam-1", time.Now(), candidateJSON(t, "c1"), []byte("crop-bytes"))
	if err == nil {
		t.Fatal("EnqueueANPRCandidate(): want error for a permanent auth failure, got nil")
	}
	if got := s.CloudBufferStats().BufferedFrames; got != 0 {
		t.Fatalf("BufferedFrames = %d, want 0 (unauthorized must never be buffered)", got)
	}
}

// Requirement #29: an ANPR crop must compete for the SAME token budget a
// frame would -- never a separate, unthrottled path.
func TestEnqueueANPRCandidate_SharesTokenBucketWithFrames(t *testing.T) {
	sender := &fakeAnprSender{}
	dir := t.TempDir()
	cfg := Config{MaxBytesPerSec: 1, BurstBytes: 10} // tiny budget, easy to exhaust
	s := New(sender, "dev-1", "cred-1", cfg, slog.Default(), nil, WithBuffer(dir, 1<<20, 10, 0))
	t.Cleanup(s.Close)

	bigCrop := make([]byte, 20) // bigger than the whole burst capacity
	if err := s.EnqueueANPRCandidate("cam-1", time.Now(), candidateJSON(t, "c1"), bigCrop); err != ErrThrottled {
		t.Fatalf("EnqueueANPRCandidate() error = %v, want ErrThrottled", err)
	}

	// A frame competing for the same (now exhausted) budget must also throttle.
	if err := s.Route(testFrame("cam-1", 1)); err != ErrThrottled {
		t.Fatalf("Route() error = %v, want ErrThrottled (shared budget with the ANPR attempt above)", err)
	}
}

func TestEnqueueANPRCandidate_SenderWithoutANPRSupportErrorsExplicitly(t *testing.T) {
	sender := &fakeSender{} // does NOT implement ANPRSender
	s, _ := newBufferedSink(t, sender, 10)

	err := s.EnqueueANPRCandidate("cam-1", time.Now(), candidateJSON(t, "c1"), []byte("crop"))
	if err == nil {
		t.Fatal("expected an explicit error when the sender does not support ANPR candidates")
	}
}

// Requirement #28: a legacy spool entry with no "kind" field (written by a
// pre-J6 CloudSink) must still replay as a frame, never misinterpreted as
// an ANPR candidate.
func TestBuffer_LegacyEntryWithoutKind_ReplaysAsFrame(t *testing.T) {
	dir := t.TempDir()
	buf, err := OpenBuffer(dir, 1<<20, 10, 0)
	if err != nil {
		t.Fatalf("OpenBuffer: %v", err)
	}
	// Simulate a pre-J6 entry: Kind left at its zero value, exactly what a
	// legacy writeAtomic call (before Kind existed) would have produced.
	if err := buf.Enqueue(BufferedFrame{CandidateKey: "cam-1", Seq: 1, Timestamp: time.Now(), JPEG: []byte("legacy-jpeg")}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	got, ok, err := buf.Peek()
	if err != nil || !ok {
		t.Fatalf("Peek() = %+v, %v, %v", got, ok, err)
	}
	if got.Kind != KindFrame {
		t.Fatalf("Kind = %q, want KindFrame (legacy entry must default to frame)", got.Kind)
	}
}
