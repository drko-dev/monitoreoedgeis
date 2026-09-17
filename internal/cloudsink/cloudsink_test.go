package cloudsink

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

type fakeSender struct {
	mu  sync.Mutex
	err error
	// failFirstN, if > 0, makes the first N calls fail with err before
	// switching to success — used to exercise buffer replay.
	failFirstN int

	gotDeviceID   string
	gotCredential string
	gotCandidate  string
	gotSeq        uint64
	gotCapturedAt time.Time
	gotJPEG       []byte
	calls         int
}

func (f *fakeSender) PostFrame(_ context.Context, deviceID, credential, candidateKey string, seq uint64, capturedAt time.Time, jpeg []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.gotDeviceID = deviceID
	f.gotCredential = credential
	f.gotCandidate = candidateKey
	f.gotSeq = seq
	f.gotCapturedAt = capturedAt
	f.gotJPEG = append([]byte(nil), jpeg...)
	if f.failFirstN > 0 {
		if f.calls <= f.failFirstN {
			return f.err
		}
		return nil
	}
	return f.err
}

func (f *fakeSender) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func solidYUV420p(width, height int, y, cb, cr byte) []byte {
	ySize := width * height
	cSize := (width / 2) * (height / 2)
	buf := make([]byte, ySize+2*cSize)
	for i := 0; i < ySize; i++ {
		buf[i] = y
	}
	for i := ySize; i < ySize+cSize; i++ {
		buf[i] = cb
	}
	for i := ySize + cSize; i < ySize+2*cSize; i++ {
		buf[i] = cr
	}
	return buf
}

func TestCloudSink_Name(t *testing.T) {
	s := New(&fakeSender{}, "dev-1", "cred-1", slog.Default())
	if s.Name() != "cloud" {
		t.Fatalf("Name() = %q, want %q", s.Name(), "cloud")
	}
}

func TestCloudSink_Route_EncodesAndUploads(t *testing.T) {
	sender := &fakeSender{}
	s := New(sender, "dev-1", "cred-1", slog.Default())

	frame := processing.Frame{
		CandidateKey: "cam-1",
		Timestamp:    time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
		Seq:          42,
		OutputWidth:  16,
		OutputHeight: 8,
		Data:         solidYUV420p(16, 8, 100, 128, 128),
	}

	if err := s.Route(frame); err != nil {
		t.Fatalf("Route() error = %v", err)
	}
	if sender.calls != 1 {
		t.Fatalf("PostFrame calls = %d, want 1", sender.calls)
	}
	if sender.gotDeviceID != "dev-1" || sender.gotCredential != "cred-1" {
		t.Fatalf("wrong identity forwarded: device=%q credential=%q", sender.gotDeviceID, sender.gotCredential)
	}
	if sender.gotCandidate != "cam-1" {
		t.Fatalf("gotCandidate = %q, want cam-1", sender.gotCandidate)
	}
	if sender.gotSeq != 42 {
		t.Fatalf("gotSeq = %d, want 42", sender.gotSeq)
	}
	if !sender.gotCapturedAt.Equal(frame.Timestamp) {
		t.Fatalf("gotCapturedAt = %v, want %v", sender.gotCapturedAt, frame.Timestamp)
	}

	img, err := jpeg.Decode(bytes.NewReader(sender.gotJPEG))
	if err != nil {
		t.Fatalf("uploaded bytes are not a valid JPEG: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != 16 || b.Dy() != 8 {
		t.Fatalf("decoded JPEG dimensions = %dx%d, want 16x8", b.Dx(), b.Dy())
	}
}

func TestCloudSink_Route_PropagatesSenderError(t *testing.T) {
	wantErr := errors.New("saas unreachable")
	sender := &fakeSender{err: wantErr}
	s := New(sender, "dev-1", "cred-1", slog.Default())

	frame := processing.Frame{
		CandidateKey: "cam-1",
		OutputWidth:  4,
		OutputHeight: 4,
		Data:         solidYUV420p(4, 4, 0, 0, 0),
	}

	err := s.Route(frame)
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("Route() error = %v, want wrapped %v", err, wantErr)
	}
}

func TestCloudSink_Route_RejectsMismatchedFrameData(t *testing.T) {
	s := New(&fakeSender{}, "dev-1", "cred-1", slog.Default())

	frame := processing.Frame{
		OutputWidth:  16,
		OutputHeight: 8,
		Data:         []byte{1, 2, 3}, // far too short for 16x8 yuv420p
	}

	if err := s.Route(frame); err == nil {
		t.Fatal("Route() with mismatched frame data: want error, got nil")
	}
}

func TestCloudSink_Route_RejectsOddDimensions(t *testing.T) {
	s := New(&fakeSender{}, "dev-1", "cred-1", slog.Default())

	frame := processing.Frame{
		OutputWidth:  15,
		OutputHeight: 8,
		Data:         make([]byte, 15*8+2*((15/2)*(8/2))),
	}

	if err := s.Route(frame); err == nil {
		t.Fatal("Route() with odd width: want error, got nil")
	}
}

func TestYUV420pToImage_ValidDimensions(t *testing.T) {
	data := solidYUV420p(4, 2, 10, 20, 30)
	img, err := yuv420pToImage(data, 4, 2)
	if err != nil {
		t.Fatalf("yuv420pToImage() error = %v", err)
	}
	if img.Rect.Dx() != 4 || img.Rect.Dy() != 2 {
		t.Fatalf("image dimensions = %dx%d, want 4x2", img.Rect.Dx(), img.Rect.Dy())
	}
	if img.Y[0] != 10 || img.Cb[0] != 20 || img.Cr[0] != 30 {
		t.Fatalf("plane data not passed through unchanged: Y=%d Cb=%d Cr=%d", img.Y[0], img.Cb[0], img.Cr[0])
	}
}

func TestYUV420pToImage_RejectsZeroDimensions(t *testing.T) {
	if _, err := yuv420pToImage(nil, 0, 0); err == nil {
		t.Fatal("yuv420pToImage(0,0): want error, got nil")
	}
}

// --- Milestone I6: offline buffer integration --------------------------

func testFrame(candidateKey string, seq uint64) processing.Frame {
	return processing.Frame{
		CandidateKey: candidateKey,
		Timestamp:    time.Now(),
		Seq:          seq,
		OutputWidth:  4,
		OutputHeight: 4,
		Data:         solidYUV420p(4, 4, 10, 20, 30),
	}
}

func newBufferedSink(t *testing.T, sender FrameSender, maxFrames int) (*CloudSink, string) {
	t.Helper()
	dir := t.TempDir()
	s := New(sender, "dev-1", "cred-1", slog.Default(),
		WithBuffer(dir, 1<<20, maxFrames, 0))
	t.Cleanup(s.Close)
	return s, dir
}

func TestCloudSink_Route_UploadOK_NeverBuffers(t *testing.T) {
	sender := &fakeSender{}
	s, _ := newBufferedSink(t, sender, 10)

	if err := s.Route(testFrame("cam-1", 1)); err != nil {
		t.Fatalf("Route() error = %v", err)
	}
	if got := s.CloudBufferStats().BufferedFrames; got != 0 {
		t.Fatalf("BufferedFrames = %d, want 0 (upload succeeded)", got)
	}
}

func TestCloudSink_Route_RecoverableError_Buffers(t *testing.T) {
	sender := &fakeSender{err: fmt.Errorf("boom: %w", transport.ErrSaaSUnavailable)}
	s, _ := newBufferedSink(t, sender, 10)

	if err := s.Route(testFrame("cam-1", 1)); err != nil {
		t.Fatalf("Route() error = %v, want nil (buffered, not dropped)", err)
	}
	stats := s.CloudBufferStats()
	if stats.BufferedFrames != 1 {
		t.Fatalf("BufferedFrames = %d, want 1", stats.BufferedFrames)
	}
}

func TestCloudSink_Route_NonRecoverableError_NeverBuffers(t *testing.T) {
	sender := &fakeSender{err: fmt.Errorf("nope: %w", transport.ErrUnauthorized)}
	s, _ := newBufferedSink(t, sender, 10)

	err := s.Route(testFrame("cam-1", 1))
	if err == nil || !errors.Is(err, transport.ErrUnauthorized) {
		t.Fatalf("Route() error = %v, want wrapped ErrUnauthorized", err)
	}
	if got := s.CloudBufferStats().BufferedFrames; got != 0 {
		t.Fatalf("BufferedFrames = %d, want 0 (unauthorized must never be buffered)", got)
	}
}

// TestCloudSink_Route_HTTPStatusClassification pins buffer-or-not behavior
// per HTTP status, using transport's real status classifier (not a
// hand-picked sentinel) so a future change to that classification is
// caught here too.
func TestCloudSink_Route_HTTPStatusClassification(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantBuffer bool
	}{
		{"500 -> buffer", statusErr(t, http.StatusInternalServerError), true},
		{"503 -> buffer", statusErr(t, http.StatusServiceUnavailable), true},
		{"429 -> buffer", statusErr(t, http.StatusTooManyRequests), true},
		{"408 -> buffer", statusErr(t, http.StatusRequestTimeout), true},
		{"timeout -> buffer", transport.ErrTimeout, true},
		{"unreachable -> buffer", transport.ErrSaaSUnavailable, true},

		{"400 -> NO buffer", statusErr(t, http.StatusBadRequest), false},
		{"401 -> NO buffer", statusErr(t, http.StatusUnauthorized), false},
		{"403 -> NO buffer", statusErr(t, http.StatusForbidden), false},
		{"404 -> NO buffer", statusErr(t, http.StatusNotFound), false},
		{"409 -> NO buffer", statusErr(t, http.StatusConflict), false},
		{"413 -> NO buffer", statusErr(t, http.StatusRequestEntityTooLarge), false},
		{"422 -> NO buffer", statusErr(t, http.StatusUnprocessableEntity), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender := &fakeSender{err: tt.err}
			s, _ := newBufferedSink(t, sender, 10)

			_ = s.Route(testFrame("cam-1", 1))

			got := s.CloudBufferStats().BufferedFrames
			if tt.wantBuffer && got != 1 {
				t.Fatalf("BufferedFrames = %d, want 1 (buffered)", got)
			}
			if !tt.wantBuffer && got != 0 {
				t.Fatalf("BufferedFrames = %d, want 0 (never buffered)", got)
			}
		})
	}
}

// statusErr runs the real transport classifier for status via a tiny local
// HTTP server, so this test exercises the same code path PostFrame does
// rather than hand-picking a sentinel that might drift from it.
func statusErr(t *testing.T, status int) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	c, err := transport.New(srv.URL, true, 2*time.Second, "test")
	if err != nil {
		t.Fatalf("transport.New() error = %v", err)
	}
	return c.PostFrame(context.Background(), "dev-1", "cred-1", "cam-1", 1, time.Now(), []byte{1})
}

// TestCloudSink_Replay_PermanentErrorDiscardsOnceWithoutBlockingQueue covers
// the case a frame already sitting in the spool later fails replay for a
// permanent (non-recoverable) reason: it must be dropped exactly once and
// never block the frame queued behind it.
func TestCloudSink_Replay_PermanentErrorDiscardsOnceWithoutBlockingQueue(t *testing.T) {
	drainPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { drainPollInterval = 2 * time.Second })

	// Both frames buffer directly: the sender always fails while they're
	// enqueued via Route (SaaS unavailable), then switches to a permanent
	// 404 once the drain loop starts replaying.
	sender := &fakeSender{err: transport.ErrSaaSUnavailable}
	s, _ := newBufferedSink(t, sender, 10)

	if err := s.Route(testFrame("cam-1", 1)); err != nil {
		t.Fatalf("Route(1) error = %v", err)
	}
	if err := s.Route(testFrame("cam-1", 2)); err != nil {
		t.Fatalf("Route(2) error = %v", err)
	}
	if got := s.CloudBufferStats().BufferedFrames; got != 2 {
		t.Fatalf("BufferedFrames before replay = %d, want 2", got)
	}

	sender.mu.Lock()
	sender.err = statusErr(t, http.StatusNotFound)
	sender.mu.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.CloudBufferStats().BufferedFrames == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	stats := s.CloudBufferStats()
	if stats.BufferedFrames != 0 {
		t.Fatalf("BufferedFrames after replay = %d, want 0 (both discarded, queue never blocked)", stats.BufferedFrames)
	}
	// Neither frame was ever replayable once the sender started returning
	// 404, so both must have been dropped — not counted as replayed.
	if stats.ReplayedFrames != 0 {
		t.Fatalf("ReplayedFrames = %d, want 0 (404 is permanent, never a successful replay)", stats.ReplayedFrames)
	}
}

func TestCloudSink_Route_UnclassifiedError_NeverBuffers(t *testing.T) {
	// A plain, unwrapped error (never seen from the real transport client)
	// must not be buffered either: only errors this package can actually
	// name as transient are.
	sender := &fakeSender{err: errors.New("weird local failure")}
	s, _ := newBufferedSink(t, sender, 10)

	if err := s.Route(testFrame("cam-1", 1)); err == nil {
		t.Fatal("Route() with unclassified error: want error, got nil")
	}
	if got := s.CloudBufferStats().BufferedFrames; got != 0 {
		t.Fatalf("BufferedFrames = %d, want 0", got)
	}
}

func TestCloudSink_Route_KeepsQueuingBehindPendingFrames_FIFO(t *testing.T) {
	sender := &fakeSender{err: fmt.Errorf("down: %w", transport.ErrTimeout)}
	s, dir := newBufferedSink(t, sender, 10)

	if err := s.Route(testFrame("cam-1", 1)); err != nil {
		t.Fatalf("Route(1) error = %v", err)
	}

	// Now let a direct upload for the SAME camera succeed — Route must
	// still queue it behind frame 1 rather than uploading it directly,
	// or replay would deliver seq 2 before seq 1.
	sender.mu.Lock()
	sender.err = nil
	sender.mu.Unlock()
	if err := s.Route(testFrame("cam-1", 2)); err != nil {
		t.Fatalf("Route(2) error = %v", err)
	}

	if got := s.CloudBufferStats().BufferedFrames; got != 2 {
		t.Fatalf("BufferedFrames = %d, want 2 (frame 2 must queue behind frame 1)", got)
	}
	_ = dir
}

func TestCloudSink_Replay_UploadsBufferedFramesInOrder(t *testing.T) {
	drainPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { drainPollInterval = 2 * time.Second })

	// failFirstN=1: only the first call (frame 1's direct upload attempt
	// from Route) fails. Frame 2 never reaches the sender via Route at all
	// — HasPending(cam-1) is already true once frame 1 is queued, so Route
	// enqueues it directly (see the FIFO-ordering test above). Both
	// replay attempts must then succeed with no backoff involved.
	sender := &fakeSender{err: transport.ErrSaaSUnavailable, failFirstN: 1}
	s, _ := newBufferedSink(t, sender, 10)

	if err := s.Route(testFrame("cam-1", 1)); err != nil {
		t.Fatalf("Route(1) error = %v", err)
	}
	if err := s.Route(testFrame("cam-1", 2)); err != nil {
		t.Fatalf("Route(2) error = %v", err)
	}
	if got := s.CloudBufferStats().BufferedFrames; got != 2 {
		t.Fatalf("BufferedFrames before replay = %d, want 2", got)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.CloudBufferStats().BufferedFrames == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	stats := s.CloudBufferStats()
	if stats.BufferedFrames != 0 {
		t.Fatalf("BufferedFrames after replay = %d, want 0", stats.BufferedFrames)
	}
	if stats.ReplayedFrames != 2 {
		t.Fatalf("ReplayedFrames = %d, want 2", stats.ReplayedFrames)
	}
}

func TestCloudSink_RestartRecoversSpool_AndReplays(t *testing.T) {
	dir := t.TempDir()
	sender := &fakeSender{err: transport.ErrSaaSUnavailable}
	s1 := New(sender, "dev-1", "cred-1", slog.Default(), WithBuffer(dir, 1<<20, 10, 0))
	if err := s1.Route(testFrame("cam-1", 1)); err != nil {
		t.Fatalf("Route() error = %v", err)
	}
	if got := s1.CloudBufferStats().BufferedFrames; got != 1 {
		t.Fatalf("BufferedFrames = %d, want 1", got)
	}
	s1.Close()

	drainPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { drainPollInterval = 2 * time.Second })

	sender.mu.Lock()
	sender.err = nil
	sender.mu.Unlock()

	s2 := New(sender, "dev-1", "cred-1", slog.Default(), WithBuffer(dir, 1<<20, 10, 0))
	t.Cleanup(s2.Close)

	if got := s2.CloudBufferStats().BufferedFrames; got != 1 {
		t.Fatalf("BufferedFrames after restart = %d, want 1 (recovered from disk)", got)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && s2.CloudBufferStats().BufferedFrames != 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if got := s2.CloudBufferStats().BufferedFrames; got != 0 {
		t.Fatalf("BufferedFrames after replay post-restart = %d, want 0", got)
	}
}

func TestCloudSink_Close_ShutsDownDrainLoopCleanly(t *testing.T) {
	drainPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { drainPollInterval = 2 * time.Second })

	sender := &fakeSender{err: transport.ErrSaaSUnavailable}
	dir := t.TempDir()
	s := New(sender, "dev-1", "cred-1", slog.Default(), WithBuffer(dir, 1<<20, 10, 0))

	if err := s.Route(testFrame("cam-1", 1)); err != nil {
		t.Fatalf("Route() error = %v", err)
	}

	done := make(chan struct{})
	go func() {
		s.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close() did not return: drain goroutine did not shut down cleanly")
	}
}

func TestCloudSink_Close_WithoutBuffer_IsNoop(t *testing.T) {
	s := New(&fakeSender{}, "dev-1", "cred-1", slog.Default())
	s.Close() // must not panic or block
}

func TestCloudSink_DisabledBuffer_BehavesLikePreI6(t *testing.T) {
	sender := &fakeSender{err: fmt.Errorf("down: %w", transport.ErrSaaSUnavailable)}
	// No WithBuffer option: buffering stays off, exactly the original
	// (pre-I6) drop-on-failure behavior.
	s := New(sender, "dev-1", "cred-1", slog.Default())

	err := s.Route(testFrame("cam-1", 1))
	if err == nil {
		t.Fatal("Route() with buffering disabled: want the upload error propagated, got nil")
	}
	if got := s.CloudBufferStats(); got != (processing.CloudBufferStats{}) {
		t.Fatalf("CloudBufferStats() = %+v, want zero value when disabled", got)
	}
}

func TestCloudSink_Race_ConcurrentRouteAndDrain(t *testing.T) {
	drainPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { drainPollInterval = 2 * time.Second })

	sender := &fakeSender{err: transport.ErrSaaSUnavailable}
	s, _ := newBufferedSink(t, sender, 500)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := uint64(0); i < 50; i++ {
			_ = s.Route(testFrame("cam-1", i))
		}
	}()

	// Let uploads start succeeding partway through, so the drain loop is
	// racing real Route() calls, not just spinning on failures.
	go func() {
		time.Sleep(20 * time.Millisecond)
		sender.mu.Lock()
		sender.err = nil
		sender.mu.Unlock()
	}()

	wg.Wait()
	s.Close()
}
