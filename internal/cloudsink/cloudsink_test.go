package cloudsink

import (
	"bytes"
	"context"
	"errors"
	"image/jpeg"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
)

type fakeSender struct {
	err error

	gotDeviceID   string
	gotCredential string
	gotCandidate  string
	gotSeq        uint64
	gotCapturedAt time.Time
	gotJPEG       []byte
	calls         int
}

func (f *fakeSender) PostFrame(_ context.Context, deviceID, credential, candidateKey string, seq uint64, capturedAt time.Time, jpeg []byte) error {
	f.calls++
	f.gotDeviceID = deviceID
	f.gotCredential = credential
	f.gotCandidate = candidateKey
	f.gotSeq = seq
	f.gotCapturedAt = capturedAt
	f.gotJPEG = append([]byte(nil), jpeg...)
	return f.err
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
	s := New(&fakeSender{}, "dev-1", "cred-1", slog.Default(), nil)
	if s.Name() != "cloud" {
		t.Fatalf("Name() = %q, want %q", s.Name(), "cloud")
	}
}

func TestCloudSink_Route_EncodesAndUploads(t *testing.T) {
	sender := &fakeSender{}
	s := New(sender, "dev-1", "cred-1", slog.Default(), nil)

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
	s := New(sender, "dev-1", "cred-1", slog.Default(), nil)

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
	s := New(&fakeSender{}, "dev-1", "cred-1", slog.Default(), nil)

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
	s := New(&fakeSender{}, "dev-1", "cred-1", slog.Default(), nil)

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

func frame(w, h int) processing.Frame {
	return processing.Frame{
		CandidateKey: "cam-1",
		Timestamp:    time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
		OutputWidth:  w,
		OutputHeight: h,
		Data:         solidYUV420p(w, h, 100, 128, 128),
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

func TestCloudSink_Status_CountsSuccessAndBytes(t *testing.T) {
	sender := &fakeSender{}
	s := New(sender, "dev-1", "cred-1", slog.Default(), nil)

	if err := s.Route(frame(16, 8)); err != nil {
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
	s := New(sender, "dev-1", "cred-1", slog.Default(), nil)

	if err := s.Route(frame(4, 4)); err == nil {
		t.Fatal("Route(): want error, got nil")
	}

	got := s.Status()
	if got.FramesUploadAttempted != 1 || got.FramesUploadFailed != 1 || got.FramesUploadSucceeded != 0 {
		t.Fatalf("counters = %+v, want 1 attempted, 1 failed, 0 succeeded", got)
	}
	// A failed upload never counts its body as delivered — jpeg_bytes_uploaded
	// must stay 0 even though jpeg_bytes_generated is non-zero.
	if got.JPEGBytesUploaded != 0 {
		t.Fatalf("JPEGBytesUploaded = %d, want 0 on failed upload", got.JPEGBytesUploaded)
	}
	if got.JPEGBytesGenerated == 0 {
		t.Fatal("JPEGBytesGenerated = 0, want > 0: encode succeeds even when upload fails")
	}
}

func TestCloudSink_Route_PublishesToHealthSink(t *testing.T) {
	sender := &fakeSender{}
	hs := &fakeHealth{}
	s := New(sender, "dev-1", "cred-1", slog.Default(), hs)

	if err := s.Route(frame(16, 8)); err != nil {
		t.Fatalf("Route() error = %v", err)
	}

	hs.mu.Lock()
	defer hs.mu.Unlock()
	if hs.n != 1 {
		t.Fatalf("SetCloudStatus calls = %d, want 1", hs.n)
	}
	if hs.last.FramesUploadSucceeded != 1 {
		t.Fatalf("published status FramesUploadSucceeded = %d, want 1", hs.last.FramesUploadSucceeded)
	}
}

func TestCloudSink_Route_ConcurrentWithStatusRead(t *testing.T) {
	sender := &fakeSender{}
	hs := &fakeHealth{}
	s := New(sender, "dev-1", "cred-1", slog.Default(), hs)

	const iterations = 200
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if err := s.Route(frame(4, 4)); err != nil {
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
