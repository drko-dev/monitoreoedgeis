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
	s := New(&fakeSender{}, "dev-1", "cred-1", Config{}, slog.Default(), nil)
	if s.Name() != "cloud" {
		t.Fatalf("Name() = %q, want %q", s.Name(), "cloud")
	}
}

func TestCloudSink_Route_EncodesAndUploads(t *testing.T) {
	sender := &fakeSender{}
	s := New(sender, "dev-1", "cred-1", Config{}, slog.Default(), nil)

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
	s := New(sender, "dev-1", "cred-1", Config{}, slog.Default(), nil)

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
	s := New(&fakeSender{}, "dev-1", "cred-1", Config{}, slog.Default(), nil)

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
	s := New(&fakeSender{}, "dev-1", "cred-1", Config{}, slog.Default(), nil)

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

// --- Milestone I10: real resource-usage metrics -------------------------

func TestCloudSink_Status_CountsSuccessAndBytes(t *testing.T) {
	sender := &fakeSender{}
	s := New(sender, "dev-1", "cred-1", Config{}, slog.Default(), nil)

	if err := s.Route(testFrame("cam-1", 1)); err != nil {
		t.Fatalf("Route() error = %v", err)
	}

	got := s.Status()
	if got.FramesEncoded != 1 || got.FramesUploadAttempted != 1 || got.FramesUploadSucceeded != 1 || got.FramesUploadFailed != 0 {
		t.Fatalf("counters = %+v, want 1 encoded/attempted/succeeded, 0 failed", got)
	}
	if got.JPEGBytesGenerated == 0 || got.JPEGBytesGenerated != uint64(len(sender.gotJPEG)) {
		t.Fatalf("JPEGBytesGenerated = %d, want exactly uploaded len %d", got.JPEGBytesGenerated, len(sender.gotJPEG))
	}
	if got.JPEGBytesUploaded != got.JPEGBytesGenerated {
		t.Fatalf("JPEGBytesUploaded = %d, want == JPEGBytesGenerated %d (single successful upload)", got.JPEGBytesUploaded, got.JPEGBytesGenerated)
	}
	if got.EncodeLatencyAvgMs < 0 || got.UploadLatencyAvgMs < 0 {
		t.Fatalf("negative latency: encode=%v upload=%v", got.EncodeLatencyAvgMs, got.UploadLatencyAvgMs)
	}
}

func TestCloudSink_Status_SeparatesFailedFromSucceeded(t *testing.T) {
	wantErr := errors.New("saas unreachable")
	sender := &fakeSender{err: wantErr}
	s := New(sender, "dev-1", "cred-1", Config{}, slog.Default(), nil)

	if err := s.Route(testFrame("cam-1", 1)); err == nil {
		t.Fatal("Route(): want error, got nil")
	}

	got := s.Status()
	if got.FramesUploadAttempted != 1 || got.FramesUploadFailed != 1 || got.FramesUploadSucceeded != 0 {
		t.Fatalf("counters = %+v, want 1 attempted, 1 failed, 0 succeeded", got)
	}
	if got.JPEGBytesUploaded != 0 {
		t.Fatalf("JPEGBytesUploaded = %d, want 0 on failed upload", got.JPEGBytesUploaded)
	}
	if got.JPEGBytesGenerated == 0 {
		t.Fatal("JPEGBytesGenerated = 0, want > 0: encode succeeds even when upload fails")
	}
}

type fakeHealth struct {
	mu   sync.Mutex
	last Status
	n    int
}

func (h *fakeHealth) SetCloudStatus(s Status) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.last = s
	h.n++
}

func TestCloudSink_Route_PublishesToHealthSink(t *testing.T) {
	sender := &fakeSender{}
	hs := &fakeHealth{}
	s := New(sender, "dev-1", "cred-1", Config{}, slog.Default(), hs)

	if err := s.Route(testFrame("cam-1", 1)); err != nil {
		t.Fatalf("Route() error = %v", err)
	}

	hs.mu.Lock()
	defer hs.mu.Unlock()
	if hs.n == 0 {
		t.Fatal("SetCloudStatus was never called")
	}
	if hs.last.FramesUploadSucceeded != 1 {
		t.Fatalf("published status FramesUploadSucceeded = %d, want 1", hs.last.FramesUploadSucceeded)
	}
}

func TestCloudSink_Route_ConcurrentWithStatusRead(t *testing.T) {
	sender := &fakeSender{}
	hs := &fakeHealth{}
	s := New(sender, "dev-1", "cred-1", Config{}, slog.Default(), hs)

	const iterations = 200
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if err := s.Route(testFrame("cam-1", uint64(i))); err != nil {
				t.Errorf("Route() error = %v", err)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = s.Status()
		}
	}()
	wg.Wait()

	got := s.Status()
	if got.FramesUploadSucceeded != iterations {
		t.Fatalf("FramesUploadSucceeded = %d, want %d", got.FramesUploadSucceeded, iterations)
	}
}

// --- Milestone I7: configurable JPEG quality and bandwidth control ------

func TestCloudSink_ConfigurableJPEGQuality(t *testing.T) {
	senderLow := &fakeSender{}
	sLow := New(senderLow, "dev-1", "cred-1", Config{JPEGQuality: 10}, slog.Default(), nil)

	senderHigh := &fakeSender{}
	sHigh := New(senderHigh, "dev-1", "cred-1", Config{JPEGQuality: 95}, slog.Default(), nil)

	frame := processing.Frame{
		CandidateKey: "cam-1",
		Timestamp:    time.Now(),
		Seq:          1,
		OutputWidth:  64,
		OutputHeight: 64,
		Data:         solidYUV420p(64, 64, 120, 100, 150),
	}

	if err := sLow.Route(frame); err != nil {
		t.Fatalf("sLow.Route() error = %v", err)
	}
	if err := sHigh.Route(frame); err != nil {
		t.Fatalf("sHigh.Route() error = %v", err)
	}

	if len(senderLow.gotJPEG) >= len(senderHigh.gotJPEG) {
		t.Fatalf("Quality 10 JPEG (%d bytes) should be smaller than Quality 95 JPEG (%d bytes)",
			len(senderLow.gotJPEG), len(senderHigh.gotJPEG))
	}

	// Verify fallback for out-of-range quality.
	sFallback := New(&fakeSender{}, "dev-1", "cred-1", Config{JPEGQuality: 150}, slog.Default(), nil)
	if sFallback.cfg.JPEGQuality != DefaultJPEGQuality {
		t.Fatalf("expected fallback to %d for quality 150, got %d", DefaultJPEGQuality, sFallback.cfg.JPEGQuality)
	}
}

func TestCloudSink_PrePostThrottling(t *testing.T) {
	sender := &fakeSender{}
	// Strict limit: burst only 10 bytes -> any realistic frame exceeds this and gets throttled.
	cfg := Config{JPEGQuality: 85, MaxBytesPerSec: 100, BurstBytes: 10}
	s := New(sender, "dev-1", "cred-1", cfg, slog.Default(), nil)

	frame := processing.Frame{
		CandidateKey: "cam-test",
		Timestamp:    time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC),
		Seq:          99,
		OutputWidth:  16,
		OutputHeight: 8,
		Data:         solidYUV420p(16, 8, 128, 128, 128),
	}

	err := s.Route(frame)
	if err == nil {
		t.Fatal("expected Route() to fail due to rate limit, got nil")
	}
	if !errors.Is(err, ErrThrottled) {
		t.Fatalf("expected errors.Is(err, ErrThrottled) = true, got %v", err)
	}

	// CRITICAL: Ensure POST was never called (pre-POST throttling) and it
	// never counts as an upload attempt.
	if sender.calls != 0 {
		t.Fatalf("sender.calls = %d, want 0 (frame must be throttled BEFORE POST)", sender.calls)
	}

	status := s.Status()
	if status.ThrottledFrames != 1 {
		t.Fatalf("status.ThrottledFrames = %d, want 1", status.ThrottledFrames)
	}
	if status.ThrottledBytes == 0 {
		t.Fatal("status.ThrottledBytes = 0, want > 0")
	}
	if status.FramesUploadAttempted != 0 {
		t.Fatalf("status.FramesUploadAttempted = %d, want 0 (throttle must not count as an attempt)", status.FramesUploadAttempted)
	}
	if status.FramesUploadSucceeded != 0 {
		t.Fatalf("status.FramesUploadSucceeded = %d, want 0", status.FramesUploadSucceeded)
	}
	if status.ConfiguredBytesPerSec != 100 {
		t.Fatalf("status.ConfiguredBytesPerSec = %d, want 100", status.ConfiguredBytesPerSec)
	}
}

func TestCloudSink_ErrorDistinction(t *testing.T) {
	// 1. Throttling error.
	sThrottled := New(&fakeSender{}, "dev-1", "cred-1", Config{MaxBytesPerSec: 10, BurstBytes: 10}, slog.Default(), nil)
	frame := processing.Frame{
		CandidateKey: "cam-1",
		Timestamp:    time.Now(),
		Seq:          1,
		OutputWidth:  16,
		OutputHeight: 8,
		Data:         solidYUV420p(16, 8, 100, 100, 100),
	}
	errThrottled := sThrottled.Route(frame)
	if !errors.Is(errThrottled, ErrThrottled) {
		t.Fatalf("expected ErrThrottled, got %v", errThrottled)
	}

	// 2. Transport error.
	netErr := errors.New("connection reset by peer")
	sTransport := New(&fakeSender{err: netErr}, "dev-1", "cred-1", Config{}, slog.Default(), nil)
	errTransport := sTransport.Route(frame)
	if !errors.Is(errTransport, netErr) {
		t.Fatalf("expected wrapped netErr, got %v", errTransport)
	}
	if errors.Is(errTransport, ErrThrottled) {
		t.Fatal("transport error must not match ErrThrottled")
	}

	// 3. Dimension error.
	badFrame := processing.Frame{OutputWidth: 0, OutputHeight: 0}
	errDim := sTransport.Route(badFrame)
	if errors.Is(errDim, ErrThrottled) || errors.Is(errDim, netErr) {
		t.Fatalf("dimension error should be distinct, got %v", errDim)
	}
}

func TestCloudSink_DefaultBackwardCompatibility(t *testing.T) {
	sender := &fakeSender{}
	s := New(sender, "dev-1", "cred-1", Config{}, slog.Default(), nil)

	frame := processing.Frame{
		CandidateKey: "cam-1",
		Timestamp:    time.Now(),
		Seq:          10,
		OutputWidth:  16,
		OutputHeight: 8,
		Data:         solidYUV420p(16, 8, 100, 100, 100),
	}

	if err := s.Route(frame); err != nil {
		t.Fatalf("Route() failed: %v", err)
	}
	if sender.calls != 1 {
		t.Fatalf("sender.calls = %d, want 1", sender.calls)
	}
	status := s.Status()
	if status.ThrottledFrames != 0 {
		t.Fatalf("ThrottledFrames = %d, want 0", status.ThrottledFrames)
	}
	if status.FramesUploadSucceeded != 1 {
		t.Fatalf("FramesUploadSucceeded = %d, want 1", status.FramesUploadSucceeded)
	}
	if status.ConfiguredBytesPerSec != 0 {
		t.Fatalf("ConfiguredBytesPerSec = %d, want 0", status.ConfiguredBytesPerSec)
	}
	if status.JPEGQuality != DefaultJPEGQuality {
		t.Fatalf("JPEGQuality = %d, want default %d", status.JPEGQuality, DefaultJPEGQuality)
	}
}

// --- Milestone I6: offline buffer integration ----------------------------

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
	s := New(sender, "dev-1", "cred-1", Config{}, slog.Default(), nil,
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
	if status := s.Status(); status.FramesUploadFailed != 1 {
		t.Fatalf("FramesUploadFailed = %d, want 1 (the direct attempt still failed before buffering)", status.FramesUploadFailed)
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

func TestCloudSink_Route_PermanentError_CountsFailedNoBuffer(t *testing.T) {
	sender := &fakeSender{err: fmt.Errorf("nope: %w", transport.ErrInvalidRequest)}
	s, _ := newBufferedSink(t, sender, 10)

	if err := s.Route(testFrame("cam-1", 1)); err == nil {
		t.Fatal("Route(): want error for a permanent 4xx, got nil")
	}
	status := s.Status()
	if status.FramesUploadFailed != 1 {
		t.Fatalf("FramesUploadFailed = %d, want 1", status.FramesUploadFailed)
	}
	if got := s.CloudBufferStats().BufferedFrames; got != 0 {
		t.Fatalf("BufferedFrames = %d, want 0 (permanent 4xx must never spool)", got)
	}
}

func TestCloudSink_Route_ThrottledFrame_NeverEntersBuffer(t *testing.T) {
	sender := &fakeSender{}
	dir := t.TempDir()
	cfg := Config{MaxBytesPerSec: 1, BurstBytes: 1}
	s := New(sender, "dev-1", "cred-1", cfg, slog.Default(), nil, WithBuffer(dir, 1<<20, 10, 0))
	t.Cleanup(s.Close)

	err := s.Route(testFrame("cam-1", 1))
	if !errors.Is(err, ErrThrottled) {
		t.Fatalf("Route() error = %v, want ErrThrottled", err)
	}
	if sender.calls != 0 {
		t.Fatalf("sender.calls = %d, want 0 (throttled before POST)", sender.calls)
	}
	if status := s.Status(); status.FramesUploadAttempted != 0 {
		t.Fatalf("FramesUploadAttempted = %d, want 0", status.FramesUploadAttempted)
	}
	if got := s.CloudBufferStats().BufferedFrames; got != 0 {
		t.Fatalf("BufferedFrames = %d, want 0 (ErrThrottled must never enter the I6 buffer)", got)
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

// TestCloudSink_Replay_CountsUploadNotEncode pins the I6+I10 integration
// contract: a replay is a real PostFrame attempt (counted in
// frames_upload_attempted/succeeded and jpeg_bytes_uploaded) but never
// re-encodes the frame (frames_encoded/jpeg_bytes_generated stay put — the
// JPEG was already generated once, before it ever entered the buffer).
func TestCloudSink_Replay_CountsUploadNotEncode(t *testing.T) {
	drainPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { drainPollInterval = 2 * time.Second })

	sender := &fakeSender{err: transport.ErrSaaSUnavailable}
	s, _ := newBufferedSink(t, sender, 10)

	if err := s.Route(testFrame("cam-1", 1)); err != nil {
		t.Fatalf("Route() error = %v", err)
	}
	afterRoute := s.Status()
	if afterRoute.FramesEncoded != 1 {
		t.Fatalf("FramesEncoded after Route = %d, want 1", afterRoute.FramesEncoded)
	}
	if afterRoute.FramesUploadAttempted != 1 {
		t.Fatalf("FramesUploadAttempted after Route = %d, want 1 (the failed direct attempt)", afterRoute.FramesUploadAttempted)
	}

	sender.mu.Lock()
	sender.err = nil
	sender.mu.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && s.CloudBufferStats().BufferedFrames != 0 {
		time.Sleep(20 * time.Millisecond)
	}

	final := s.Status()
	if final.FramesEncoded != 1 {
		t.Fatalf("FramesEncoded after replay = %d, want still 1 (replay must not re-encode)", final.FramesEncoded)
	}
	if final.FramesUploadAttempted != 2 {
		t.Fatalf("FramesUploadAttempted after replay = %d, want 2 (direct attempt + replay attempt)", final.FramesUploadAttempted)
	}
	if final.FramesUploadSucceeded != 1 {
		t.Fatalf("FramesUploadSucceeded = %d, want 1 (the replay succeeded)", final.FramesUploadSucceeded)
	}
	if final.JPEGBytesUploaded != afterRoute.JPEGBytesGenerated {
		t.Fatalf("JPEGBytesUploaded = %d, want == JPEGBytesGenerated %d", final.JPEGBytesUploaded, afterRoute.JPEGBytesGenerated)
	}
	if got := s.CloudBufferStats().ReplayedFrames; got != 1 {
		t.Fatalf("ReplayedFrames = %d, want 1", got)
	}
}

// TestCloudSink_Replay_WaitsForLimiterInsteadOfDropping is the CRITICAL I6+I7
// integration case: a frame already durable in the offline buffer must
// never be dropped just because the bandwidth limiter is temporarily out of
// tokens — replay paces itself against the limiter (limiter.Wait) instead.
func TestCloudSink_Replay_WaitsForLimiterInsteadOfDropping(t *testing.T) {
	drainPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { drainPollInterval = 2 * time.Second })

	f := testFrame("cam-1", 1)

	// Measure the real JPEG size this frame encodes to, so the limiter's
	// burst can be set just large enough to admit exactly one frame
	// directly — draining it, so replay must wait for a refill.
	probe := New(&fakeSender{}, "dev-1", "cred-1", Config{}, slog.Default(), nil)
	if err := probe.Route(f); err != nil {
		t.Fatalf("probe Route() error = %v", err)
	}
	frameBytes := int64(probe.Status().JPEGBytesGenerated)

	sender := &fakeSender{err: transport.ErrSaaSUnavailable}
	dir := t.TempDir()
	cfg := Config{MaxBytesPerSec: 200, BurstBytes: frameBytes}
	s := New(sender, "dev-1", "cred-1", cfg, slog.Default(), nil, WithBuffer(dir, 1<<20, 10, 0))
	t.Cleanup(s.Close)

	if err := s.Route(f); err != nil {
		t.Fatalf("Route() error = %v", err)
	}
	if got := s.CloudBufferStats().BufferedFrames; got != 1 {
		t.Fatalf("BufferedFrames = %d, want 1", got)
	}

	// The burst is now drained (Route's own limiter check consumed it), so
	// replay must wait for it to refill rather than dropping the frame.
	sender.mu.Lock()
	sender.err = nil
	sender.mu.Unlock()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && s.CloudBufferStats().BufferedFrames != 0 {
		time.Sleep(20 * time.Millisecond)
	}
	stats := s.CloudBufferStats()
	if stats.BufferedFrames != 0 {
		t.Fatal("frame was never replayed: the limiter must have waited, not dropped it")
	}
	if stats.ReplayedFrames != 1 {
		t.Fatalf("ReplayedFrames = %d, want 1", stats.ReplayedFrames)
	}
}

// TestCloudSink_Close_DuringLimiterWait_TerminatesCleanly ensures shutdown
// cancels a replay that is blocked inside limiter.Wait immediately, instead
// of waiting out the full refill.
func TestCloudSink_Close_DuringLimiterWait_TerminatesCleanly(t *testing.T) {
	drainPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { drainPollInterval = 2 * time.Second })

	f := testFrame("cam-1", 1)
	probe := New(&fakeSender{}, "dev-1", "cred-1", Config{}, slog.Default(), nil)
	if err := probe.Route(f); err != nil {
		t.Fatalf("probe Route() error = %v", err)
	}
	frameBytes := int64(probe.Status().JPEGBytesGenerated)

	sender := &fakeSender{err: transport.ErrSaaSUnavailable}
	dir := t.TempDir()
	// bytesPerSec=1 with burst exactly one frame: after Route drains the
	// burst, a real refill would take ~frameBytes seconds — far longer
	// than this test should ever wait, so Close() must cancel the wait.
	cfg := Config{MaxBytesPerSec: 1, BurstBytes: frameBytes}
	s := New(sender, "dev-1", "cred-1", cfg, slog.Default(), nil, WithBuffer(dir, 1<<20, 10, 0))

	if err := s.Route(f); err != nil {
		t.Fatalf("Route() error = %v", err)
	}

	sender.mu.Lock()
	sender.err = nil
	sender.mu.Unlock()

	// Give the drain loop a moment to pick the frame up and start waiting.
	time.Sleep(50 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		s.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close() did not return: limiter.Wait did not observe cancellation")
	}
}

func TestCloudSink_RestartRecoversSpool_AndReplays(t *testing.T) {
	dir := t.TempDir()
	sender := &fakeSender{err: transport.ErrSaaSUnavailable}
	s1 := New(sender, "dev-1", "cred-1", Config{}, slog.Default(), nil, WithBuffer(dir, 1<<20, 10, 0))
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

	s2 := New(sender, "dev-1", "cred-1", Config{}, slog.Default(), nil, WithBuffer(dir, 1<<20, 10, 0))
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
	s := New(sender, "dev-1", "cred-1", Config{}, slog.Default(), nil, WithBuffer(dir, 1<<20, 10, 0))

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
	s := New(&fakeSender{}, "dev-1", "cred-1", Config{}, slog.Default(), nil)
	s.Close() // must not panic or block
}

func TestCloudSink_DisabledBuffer_BehavesLikePreI6(t *testing.T) {
	sender := &fakeSender{err: fmt.Errorf("down: %w", transport.ErrSaaSUnavailable)}
	// No WithBuffer option: buffering stays off, exactly the original
	// (pre-I6) drop-on-failure behavior.
	s := New(sender, "dev-1", "cred-1", Config{}, slog.Default(), nil)

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
