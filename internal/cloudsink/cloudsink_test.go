package cloudsink

import (
	"bytes"
	"context"
	"errors"
	"image/jpeg"
	"log/slog"
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

func TestCloudSink_ConfigurableJPEGQuality(t *testing.T) {
	senderLow := &fakeSender{}
	sLow := NewWithConfig(senderLow, "dev-1", "cred-1", Config{JPEGQuality: 10}, slog.Default())

	senderHigh := &fakeSender{}
	sHigh := NewWithConfig(senderHigh, "dev-1", "cred-1", Config{JPEGQuality: 95}, slog.Default())

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

	// Verify fallback for out-of-range quality
	sFallback := NewWithConfig(&fakeSender{}, "dev-1", "cred-1", Config{JPEGQuality: 150}, slog.Default())
	if sFallback.cfg.JPEGQuality != DefaultJPEGQuality {
		t.Fatalf("expected fallback to %d for quality 150, got %d", DefaultJPEGQuality, sFallback.cfg.JPEGQuality)
	}
}

func TestCloudSink_PrePostThrottling(t *testing.T) {
	sender := &fakeSender{}
	// Strict limit: burst only 10 bytes -> any realistic frame will exceed this and get throttled.
	cfg := Config{
		JPEGQuality:    85,
		MaxBytesPerSec: 100,
		BurstBytes:     10,
	}
	s := NewWithConfig(sender, "dev-1", "cred-1", cfg, slog.Default())

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
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected errors.Is(err, ErrRateLimited) = true, got %v", err)
	}

	// CRITICAL: Ensure POST was never called (pre-POST throttling)
	if sender.calls != 0 {
		t.Fatalf("sender.calls = %d, want 0 (frame must be throttled BEFORE POST)", sender.calls)
	}

	// Check metrics
	status := s.Status()
	if status.ThrottledFrames != 1 {
		t.Fatalf("status.ThrottledFrames = %d, want 1", status.ThrottledFrames)
	}
	if status.ThrottledBytes <= 0 {
		t.Fatalf("status.ThrottledBytes = %d, want > 0", status.ThrottledBytes)
	}
	if status.UploadedFrames != 0 {
		t.Fatalf("status.UploadedFrames = %d, want 0", status.UploadedFrames)
	}
	if status.UploadedBytes != 0 {
		t.Fatalf("status.UploadedBytes = %d, want 0", status.UploadedBytes)
	}
	if status.ConfiguredLimit != 100 {
		t.Fatalf("status.ConfiguredLimit = %d, want 100", status.ConfiguredLimit)
	}
}

func TestCloudSink_ErrorDistinction(t *testing.T) {
	// 1. Throttling error
	sThrottled := NewWithConfig(&fakeSender{}, "dev-1", "cred-1", Config{
		MaxBytesPerSec: 10,
		BurstBytes:     10,
	}, slog.Default())
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

	// 2. Transport error
	netErr := errors.New("connection reset by peer")
	sTransport := New(&fakeSender{err: netErr}, "dev-1", "cred-1", slog.Default())
	errTransport := sTransport.Route(frame)
	if !errors.Is(errTransport, netErr) {
		t.Fatalf("expected wrapped netErr, got %v", errTransport)
	}
	if errors.Is(errTransport, ErrThrottled) {
		t.Fatal("transport error must not match ErrThrottled")
	}

	// 3. Dimension error
	badFrame := processing.Frame{
		OutputWidth:  0,
		OutputHeight: 0,
	}
	errDim := sTransport.Route(badFrame)
	if errors.Is(errDim, ErrThrottled) || errors.Is(errDim, netErr) {
		t.Fatalf("dimension error should be distinct, got %v", errDim)
	}
}

func TestCloudSink_DefaultBackwardCompatibility(t *testing.T) {
	sender := &fakeSender{}
	s := New(sender, "dev-1", "cred-1", slog.Default())

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
	if status.UploadedFrames != 1 {
		t.Fatalf("UploadedFrames = %d, want 1", status.UploadedFrames)
	}
	if status.ConfiguredLimit != 0 {
		t.Fatalf("ConfiguredLimit = %d, want 0", status.ConfiguredLimit)
	}
}
