// Package cloudsink implements processing.Sink to push sampled video frames
// to the SaaS's Cloud Vision Worker (Milestone I). It reuses
// internal/transport for auth/HTTP against the SaaS's existing endpoints —
// never a second RTSP client, never duplicated credentials/reconnect
// handling, and never talking to the Cloud Vision Worker directly (that
// stays behind the SaaS, reachable only over its own internal IPC).
package cloudsink

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// FrameSender is the subset of *transport.Client this sink needs, kept as an
// interface so tests never spin up a real HTTP client.
type FrameSender interface {
	PostFrame(ctx context.Context, deviceID, credential, candidateKey string, seq uint64, capturedAt time.Time, jpeg []byte) error
}

// MetadataFrameSender optionally extends FrameSender to deliver hybrid candidate
// metadata (Milestone J7).
type MetadataFrameSender interface {
	PostFrameWithMetadata(ctx context.Context, deviceID, credential, candidateKey string, seq uint64, capturedAt time.Time, jpeg []byte, meta transport.FrameMetadata) error
}

// ANPRSender optionally extends FrameSender to deliver Hito J6 ANPR/LPR
// candidates. metadataJSON is the already-encoded ANPRCandidateEnvelope v1
// (internal/anpr's wire contract) -- CloudSink stays decoupled from the
// anpr package's Go types, exactly as it already stays decoupled from
// vision.Detection, and only ever relays opaque bytes plus rate-limits/
// buffers them.
type ANPRSender interface {
	PostANPRCandidate(ctx context.Context, deviceID, credential string, metadataJSON []byte, cropJPEG []byte) error
}

// DefaultJPEGQuality is the standard compression quality for Cloud upload
// (Milestone I; configurable per Milestone I7 via Config.JPEGQuality).
const DefaultJPEGQuality = 85

// JPEGQuality is kept for callers that referenced the pre-I7 fixed value.
const JPEGQuality = DefaultJPEGQuality

// RequestTimeout bounds a single frame upload. Router runs exactly one
// worker goroutine per sink (see internal/processing/router.go), so a stuck
// upload only ever delays this sink's own queue — it must still be bounded,
// or a dead SaaS would grow this sink's drop count without ever recovering.
const RequestTimeout = 5 * time.Second

// Drain backoff bounds for replaying buffered frames (Milestone I6). Capped
// exponential backoff, same shape as internal/heartbeat/backoff.go — kept as
// a private duplicate here rather than exported from that package, since
// its type is deliberately unexported (its retry policy is heartbeat-only).
//
// drainPollInterval is a var, not a const, solely so cloudsink_test.go can
// shrink it — production code never changes it.
var drainPollInterval = 2 * time.Second

const (
	minDrainBackoff = 2 * time.Second
	maxDrainBackoff = 60 * time.Second
)

// ErrThrottled is returned by Route when a frame is dropped before POST
// because upload bandwidth or frame rate limits are exceeded (Milestone
// I7). It is a policy drop, never a transport failure: it never counts as
// an upload attempt and never enters the Milestone I6 offline buffer.
var ErrThrottled = errors.New("cloudsink: frame throttled by rate limit")

// Config holds tunables for CloudSink bandwidth control (Milestone I7). The
// zero value is the pre-I7, PR #11-compatible behavior: quality 85, no
// rate limiting.
type Config struct {
	JPEGQuality    int     // 1-100; <= 0 defaults to DefaultJPEGQuality
	MaxBytesPerSec int64   // > 0 enables byte rate limiting, 0 = unlimited
	BurstBytes     int64   // > 0 sets explicit burst capacity in bytes, 0 = auto
	MaxFPS         float64 // > 0 enables frame rate limiting towards Cloud, 0 = unlimited
}

// DefaultConfig returns the backward-compatible configuration matching PR #11.
func DefaultConfig() Config {
	return Config{JPEGQuality: DefaultJPEGQuality}
}

// HealthSink accepts periodic cloud-transport status updates (e.g.
// *health.Reporter). cloudsink never imports internal/health — same
// one-way dependency direction as processing.HealthSink (see
// docs/ARCHITECTURE.md).
type HealthSink interface {
	SetCloudStatus(s Status)
}

// Status is a point-in-time snapshot of real resources consumed by the
// Cloud transport (Hito I10), plus Milestone I7's configured limits and
// throttle counters. It measures actual encode/upload activity — never an
// estimated or priced cost. Counters are cumulative since process start
// and reset only on process restart.
//
// frames_upload_attempted can be greater than frames_encoded: a Milestone
// I6 replay re-attempts the upload of a frame whose JPEG was already
// encoded (and counted) before it entered the offline buffer, so replay
// attempts add to frames_upload_attempted/frames_upload_succeeded/
// jpeg_bytes_uploaded without ever incrementing frames_encoded or
// jpeg_bytes_generated again.
type Status struct {
	FramesEncoded         uint64  `json:"frames_encoded"`
	FramesUploadAttempted uint64  `json:"frames_upload_attempted"`
	FramesUploadSucceeded uint64  `json:"frames_upload_succeeded"`
	FramesUploadFailed    uint64  `json:"frames_upload_failed"`
	JPEGBytesGenerated    uint64  `json:"jpeg_bytes_generated"`
	JPEGBytesUploaded     uint64  `json:"jpeg_bytes_uploaded"`
	EncodeLatencyAvgMs    float64 `json:"encode_latency_avg_ms"`
	UploadLatencyAvgMs    float64 `json:"upload_latency_avg_ms"`
	// EffectiveBytesPerSec/EffectiveFramesPerSec are a ROLLING rate over
	// the trailing effectiveRateWindow (10s) — never a since-process-start
	// average. A cumulative average would stay badly diluted for the rest
	// of the process's life after any outage: a long idle period followed
	// by an I6 replay burst would otherwise report a misleadingly low
	// rate right when real load is highest. Both direct and replayed
	// uploads count identically. Decays to 0 once nothing has uploaded for
	// a full window.
	EffectiveBytesPerSec  float64 `json:"effective_bytes_per_sec"`
	EffectiveFramesPerSec float64 `json:"effective_frames_per_sec"`
	// SinceSeconds is process uptime for context only — it is NOT the
	// denominator of EffectiveBytesPerSec/EffectiveFramesPerSec.
	SinceSeconds float64 `json:"since_seconds"`

	// Milestone I7: throttling and the limits currently configured.
	ThrottledFrames       uint64  `json:"throttled_frames"`
	ThrottledBytes        uint64  `json:"throttled_bytes"`
	ConfiguredBytesPerSec int64   `json:"configured_bytes_per_sec"`
	ConfiguredMaxFPS      float64 `json:"configured_max_fps"`
	ConfiguredBurstBytes  int64   `json:"configured_burst_bytes"`
	JPEGQuality           int     `json:"jpeg_quality"`
}

// stats holds the atomic counters backing Status. Route may run
// concurrently with an unrelated /status read (and, once I6 buffering is
// enabled, with the drain goroutine's own replay uploads), so every field
// is accessed through sync/atomic — never a mutex, to keep the hot path
// lock-free.
type stats struct {
	framesEncoded        atomic.Uint64
	uploadAttempted      atomic.Uint64
	uploadSucceeded      atomic.Uint64
	uploadFailed         atomic.Uint64
	jpegBytesGenerated   atomic.Uint64
	jpegBytesUploaded    atomic.Uint64
	encodeLatencyNsSum   atomic.Uint64
	encodeLatencySamples atomic.Uint64
	uploadLatencyNsSum   atomic.Uint64
	uploadLatencySamples atomic.Uint64
	throttledFrames      atomic.Uint64
	throttledBytes       atomic.Uint64
}

func (s *stats) snapshot(startedAt time.Time, cfg Config, limiter *TokenBucket, rate *effectiveRate) Status {
	encodeSamples := s.encodeLatencySamples.Load()
	uploadSamples := s.uploadLatencySamples.Load()
	var encodeAvgMs, uploadAvgMs float64
	if encodeSamples > 0 {
		encodeAvgMs = float64(s.encodeLatencyNsSum.Load()) / float64(encodeSamples) / float64(time.Millisecond)
	}
	if uploadSamples > 0 {
		uploadAvgMs = float64(s.uploadLatencyNsSum.Load()) / float64(uploadSamples) / float64(time.Millisecond)
	}

	elapsed := time.Since(startedAt).Seconds()
	bytesUploaded := s.jpegBytesUploaded.Load()
	framesSucceeded := s.uploadSucceeded.Load()
	bytesPerSec, framesPerSec := rate.rate(time.Now())

	var burstBytes int64
	if limiter != nil {
		burstBytes = limiter.BurstBytes()
	}

	return Status{
		FramesEncoded:         s.framesEncoded.Load(),
		FramesUploadAttempted: s.uploadAttempted.Load(),
		FramesUploadSucceeded: framesSucceeded,
		FramesUploadFailed:    s.uploadFailed.Load(),
		JPEGBytesGenerated:    s.jpegBytesGenerated.Load(),
		JPEGBytesUploaded:     bytesUploaded,
		EncodeLatencyAvgMs:    encodeAvgMs,
		UploadLatencyAvgMs:    uploadAvgMs,
		EffectiveBytesPerSec:  bytesPerSec,
		EffectiveFramesPerSec: framesPerSec,
		SinceSeconds:          elapsed,

		ThrottledFrames:       s.throttledFrames.Load(),
		ThrottledBytes:        s.throttledBytes.Load(),
		ConfiguredBytesPerSec: cfg.MaxBytesPerSec,
		ConfiguredMaxFPS:      cfg.MaxFPS,
		ConfiguredBurstBytes:  burstBytes,
		JPEGQuality:           cfg.JPEGQuality,
	}
}

// CloudSink implements processing.Sink. Route itself holds no per-frame
// mutable state beyond stats/limiter/buffer, all of which are safe for
// concurrent use: Router guarantees Route is called by a single goroutine
// per sink, so encode/throttle/upload ordering needs no mutex; stats stay
// atomic only because /status and the I6 drain goroutine read/write them
// from other goroutines.
type CloudSink struct {
	sender     FrameSender
	deviceID   string
	credential string
	cfg        Config
	limiter    *TokenBucket
	logger     *slog.Logger

	health    HealthSink
	stats     stats
	rate      *effectiveRate
	startedAt time.Time

	buffer          *Buffer
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	droppedOversize atomic.Uint64

	// afterReplay, when non-nil, is invoked synchronously from drainLoop
	// right after a buffered frame is successfully replayed and advanced.
	// It exists only so tests can synchronize on "a replay just completed"
	// via a channel instead of polling CloudBufferStats against a
	// wall-clock deadline, which flakes under scheduler/GC pressure. Left
	// nil in production: zero behavior change.
	afterReplay func()
}

// Option configures optional CloudSink behavior at construction time.
type Option func(*CloudSink)

// WithBuffer enables Milestone I6 offline buffering: a Cloud upload that
// fails for a recoverable reason (timeout, SaaS unavailable, or a
// retryable HTTP status) is spooled under dir instead of being dropped,
// and a background goroutine drains it with backoff once uploads start
// succeeding again. maxBytes and maxFrames bound the spool and must both be
// positive, or buffering stays disabled (logged, not a hard error — an
// Edge with no buffer limits configured must still be able to start and
// upload frames directly, exactly as before I6). maxAge <= 0 disables
// age-based eviction.
func WithBuffer(dir string, maxBytes int64, maxFrames int, maxAge time.Duration) Option {
	return func(s *CloudSink) {
		if maxBytes <= 0 || maxFrames <= 0 {
			s.logger.Warn("cloud buffer disabled: max bytes and max frames must both be positive")
			return
		}
		buf, err := OpenBuffer(dir, maxBytes, maxFrames, maxAge)
		if err != nil {
			s.logger.Error("cloud buffer disabled: recovery failed", slog.Any("error", err))
			return
		}
		s.buffer = buf
	}
}

// New creates a CloudSink. deviceID/credential are the Edge's own enrolled
// identity (internal/credentials) — the same ones the heartbeat module
// uses, never a separate credential. cfg's zero value is PR #11-compatible
// (quality 85, unlimited). health is optional (nil skips /status
// publishing, e.g. in tests). opts configures Milestone I6 buffering.
func New(sender FrameSender, deviceID, credential string, cfg Config, logger *slog.Logger, health HealthSink, opts ...Option) *CloudSink {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.JPEGQuality <= 0 || cfg.JPEGQuality > 100 {
		cfg.JPEGQuality = DefaultJPEGQuality
	}

	var limiter *TokenBucket
	if cfg.MaxBytesPerSec > 0 || cfg.MaxFPS > 0 {
		limiter = NewLimiter(cfg.MaxBytesPerSec, cfg.BurstBytes, cfg.MaxFPS)
	}

	s := &CloudSink{
		sender:     sender,
		deviceID:   deviceID,
		credential: credential,
		cfg:        cfg,
		limiter:    limiter,
		logger:     logger,
		health:     health,
		rate:       newEffectiveRate(effectiveRateWindow),
		startedAt:  time.Now(),
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.buffer != nil {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		s.wg.Add(1)
		go s.drainLoop(ctx)
	}
	return s
}

// Name implements processing.Sink.
func (s *CloudSink) Name() string { return "cloud" }

// Close implements the Router's optional sinkCloser hook: it stops the
// drain goroutine and waits for it to exit, so shutdown never leaves it
// running against a torn-down agent. A no-op when buffering is disabled.
func (s *CloudSink) Close() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	s.wg.Wait()
}

// Status returns a thread-safe snapshot of real transport resource usage,
// the currently configured Milestone I7 limits, and Milestone I6 throttle
// counters.
func (s *CloudSink) Status() Status {
	return s.stats.snapshot(s.startedAt, s.cfg, s.limiter, s.rate)
}

// CloudBufferStats implements processing.CloudBufferReporter. Returns the
// zero value when buffering is disabled.
func (s *CloudSink) CloudBufferStats() processing.CloudBufferStats {
	if s.buffer == nil {
		return processing.CloudBufferStats{}
	}
	st := s.buffer.Stats()
	return processing.CloudBufferStats{
		BufferedFrames:      st.BufferedFrames,
		BufferedBytes:       st.BufferedBytes,
		ReplayedFrames:      st.ReplayedFrames,
		DroppedFull:         st.DroppedFull,
		CorruptEntries:      st.CorruptEntries,
		DroppedOversize:     int64(s.droppedOversize.Load()),
		DroppedAge:          st.DroppedAge,
		DroppedOverCapacity: st.DroppedOverCapacity,
		Capacity:            st.Capacity,
		OldestPending:       st.OldestPending,
	}
}

// Route implements processing.Sink. Semantic order for a new frame:
//
//  1. Encode to JPEG at the configured quality (Milestone I7); record I10
//     encode metrics (frames_encoded, jpeg_bytes_generated, encode latency).
//  2. If this camera already has frames waiting in the Milestone I6 buffer,
//     queue this one behind them — sending it directly first would replay
//     it out of order.
//  3. Otherwise, apply the Milestone I7 rate limiter before POST. A
//     throttled frame is a policy drop: it never reaches PostFrame, never
//     counts as an upload attempt, and never enters the I6 buffer.
//  4. POST. Record I10 upload metrics (upload_attempted, latency,
//     succeeded/failed; jpeg_bytes_uploaded only on success).
//  5. A failed POST is buffered only when isRecoverable classifies it as
//     transient (Milestone I6); otherwise it is simply dropped, exactly as
//     before I6.
func (s *CloudSink) Route(f processing.Frame) error {
	img, err := yuv420pToImage(f.Data, f.OutputWidth, f.OutputHeight)
	if err != nil {
		return fmt.Errorf("cloudsink: %w", err)
	}

	encodeStart := time.Now()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: s.cfg.JPEGQuality}); err != nil {
		return fmt.Errorf("cloudsink: encode jpeg: %w", err)
	}
	encodeLatency := time.Since(encodeStart)
	jpegBytes := buf.Bytes()
	s.stats.framesEncoded.Add(1)
	s.stats.jpegBytesGenerated.Add(uint64(len(jpegBytes)))
	s.stats.encodeLatencyNsSum.Add(uint64(encodeLatency.Nanoseconds()))
	s.stats.encodeLatencySamples.Add(1)
	s.publishStatus()

	if s.buffer != nil && s.buffer.HasPending(f.CandidateKey) {
		return s.enqueue(f, jpegBytes)
	}

	if s.limiter != nil && !s.limiter.Allow(int64(len(jpegBytes))) {
		s.stats.throttledFrames.Add(1)
		s.stats.throttledBytes.Add(uint64(len(jpegBytes)))
		s.publishStatus()
		s.logger.Debug("frame throttled by rate limit",
			"candidate_key", f.CandidateKey, "seq", f.Seq, "bytes", len(jpegBytes))
		return ErrThrottled
	}

	uploadErr := s.upload(context.Background(), f, jpegBytes)
	if uploadErr == nil {
		return nil
	}
	if s.buffer != nil && isRecoverable(uploadErr) {
		if err := s.enqueue(f, jpegBytes); err != nil {
			return fmt.Errorf("%w (buffer: %v)", uploadErr, err)
		}
		s.logger.Debug("frame buffered for retry",
			"candidate_key", f.CandidateKey, "seq", f.Seq, "error", uploadErr)
		return nil
	}
	return uploadErr
}

// upload performs the actual PostFrame call — shared by the direct Route
// path and the Milestone I6 replay path — and records the I10 upload
// metrics common to both (upload_attempted, latency, succeeded/failed,
// jpeg_bytes_uploaded on success). It never touches frames_encoded or
// jpeg_bytes_generated: those are recorded once, at encode time, whether
// the frame ships directly or is replayed later from the buffer.
func (s *CloudSink) upload(ctx context.Context, f processing.Frame, jpegBytes []byte) error {
	uploadCtx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()

	s.stats.uploadAttempted.Add(1)
	uploadStart := time.Now()
	var err error
	if ms, ok := s.sender.(MetadataFrameSender); ok {
		meta := transport.FrameMetadata{
			ProcessingMode:  f.ProcessingMode,
			CandidateReason: f.CandidateReason,
			CandidateScore:  f.CandidateScore,
			CorrelationID:   f.CorrelationID,
		}
		err = ms.PostFrameWithMetadata(uploadCtx, s.deviceID, s.credential, f.CandidateKey, f.Seq, f.Timestamp, jpegBytes, meta)
	} else {
		err = s.sender.PostFrame(uploadCtx, s.deviceID, s.credential, f.CandidateKey, f.Seq, f.Timestamp, jpegBytes)
	}
	uploadLatency := time.Since(uploadStart)
	s.stats.uploadLatencyNsSum.Add(uint64(uploadLatency.Nanoseconds()))
	s.stats.uploadLatencySamples.Add(1)

	if err != nil {
		s.stats.uploadFailed.Add(1)
		s.publishStatus()
		return fmt.Errorf("cloudsink: upload frame: %w", err)
	}
	s.stats.uploadSucceeded.Add(1)
	s.stats.jpegBytesUploaded.Add(uint64(len(jpegBytes)))
	s.rate.record(int64(len(jpegBytes)), time.Now())
	s.publishStatus()
	s.logger.Debug("frame uploaded", "candidate_key", f.CandidateKey, "seq", f.Seq, "bytes", len(jpegBytes))
	return nil
}

// publishStatus pushes the current snapshot to health, when configured. It
// runs on every encode and every upload attempt: Route/replay are already
// dominated by JPEG encode + HTTP upload, so one atomic-backed snapshot and
// a map write on the health side is not a relevant hot-path cost.
func (s *CloudSink) publishStatus() {
	if s.health == nil {
		return
	}
	s.health.SetCloudStatus(s.stats.snapshot(s.startedAt, s.cfg, s.limiter, s.rate))
}

func (s *CloudSink) enqueue(f processing.Frame, jpegBytes []byte) error {
	err := s.buffer.Enqueue(BufferedFrame{
		Kind:            KindFrame,
		CandidateKey:    f.CandidateKey,
		Seq:             f.Seq,
		Timestamp:       f.Timestamp,
		JPEG:            jpegBytes,
		ProcessingMode:  f.ProcessingMode,
		CandidateReason: f.CandidateReason,
		CandidateScore:  f.CandidateScore,
		CorrelationID:   f.CorrelationID,
	})
	if err != nil {
		return fmt.Errorf("cloudsink: %w", err)
	}
	return nil
}

// EnqueueANPRCandidate submits one Hito J6 ANPR candidate through the SAME
// TokenBucket rate limiter and the SAME offline buffer a video frame would
// use (items #27/#28/#29) -- never a second spool, never a second limiter.
// metadataJSON is the already-built ANPRCandidateEnvelope v1; candidateKey/
// timestamp are used only for buffer bookkeeping (FIFO ordering, stats),
// never re-derived into a second identity.
//
// Falls back to buffering on a recoverable upload error exactly like Route
// does for frames; on ErrThrottled or a non-recoverable error, the
// candidate is dropped (never silently retried against a permanently
// rejecting SaaS).
func (s *CloudSink) EnqueueANPRCandidate(candidateKey string, timestamp time.Time, metadataJSON, cropJPEG []byte) error {
	sender, ok := s.sender.(ANPRSender)
	if !ok {
		return errors.New("cloudsink: configured sender does not support ANPR candidates")
	}

	if s.buffer != nil && s.buffer.HasPending(candidateKey) {
		return s.enqueueANPR(candidateKey, timestamp, metadataJSON, cropJPEG)
	}

	if s.limiter != nil && !s.limiter.Allow(int64(len(cropJPEG))) {
		s.logger.Debug("anpr candidate throttled by rate limit", "candidate_key", candidateKey, "bytes", len(cropJPEG))
		return ErrThrottled
	}

	uploadErr := s.uploadANPR(context.Background(), sender, metadataJSON, cropJPEG)
	if uploadErr == nil {
		return nil
	}
	if s.buffer != nil && isRecoverable(uploadErr) {
		if err := s.enqueueANPR(candidateKey, timestamp, metadataJSON, cropJPEG); err != nil {
			return fmt.Errorf("%w (buffer: %v)", uploadErr, err)
		}
		s.logger.Debug("anpr candidate buffered for retry", "candidate_key", candidateKey, "error", uploadErr)
		return nil
	}
	return uploadErr
}

func (s *CloudSink) uploadANPR(ctx context.Context, sender ANPRSender, metadataJSON, cropJPEG []byte) error {
	uploadCtx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	if err := sender.PostANPRCandidate(uploadCtx, s.deviceID, s.credential, metadataJSON, cropJPEG); err != nil {
		return fmt.Errorf("cloudsink: upload anpr candidate: %w", err)
	}
	s.rate.record(int64(len(cropJPEG)), time.Now())
	return nil
}

func (s *CloudSink) enqueueANPR(candidateKey string, timestamp time.Time, metadataJSON, cropJPEG []byte) error {
	err := s.buffer.Enqueue(BufferedFrame{
		Kind:          KindAnprCandidate,
		CandidateKey:  candidateKey,
		Timestamp:     timestamp,
		JPEG:          cropJPEG,
		AnprCandidate: metadataJSON,
	})
	if err != nil {
		return fmt.Errorf("cloudsink: %w", err)
	}
	return nil
}

// isRecoverable decides whether a PostFrame error is worth buffering for
// retry, classified by transport's HTTP status sentinels alone (see
// transport.classifyFrameStatus) — never inferred from a response body.
//
// Recoverable: a network-level timeout or unreachable SaaS, or
// ErrRetryableStatus (HTTP 408/429/5xx — the SaaS itself is transiently
// failing or asking to slow down).
//
// Never recoverable, however many times it's retried:
//   - ErrUnauthorized (401/403): the credential is rejected; only an
//     operator rotating it can fix this, and retrying would just be a
//     request storm against a SaaS already rejecting this Edge.
//   - ErrInvalidRequest (400/404/409/413/422/...): the request itself is
//     permanently wrong. Buffering it would spool a frame that can never
//     upload, taking up space a genuinely transient frame could use.
//   - ErrUnexpectedStatus, or any plain/unclassified error (e.g. from a
//     fake sender in a test): only errors this package can actually name
//     as transient are buffered.
//
// ErrThrottled is never passed here: Route returns it before ever calling
// upload, so it can't reach this classification.
func isRecoverable(err error) bool {
	return errors.Is(err, transport.ErrTimeout) ||
		errors.Is(err, transport.ErrSaaSUnavailable) ||
		errors.Is(err, transport.ErrRetryableStatus)
}

// drainLoop replays buffered frames in FIFO order, one at a time, pacing
// each replay through the same Milestone I7 rate limiter as the direct
// path (limiter.Wait, not limiter.Allow): a frame that is already durable
// in the buffer must never be dropped just because tokens are temporarily
// unavailable — it waits for capacity instead. It also backs off
// exponentially between failed upload attempts so a still-down SaaS never
// turns into a request storm. It never blocks Route: they only ever touch
// the buffer's own mutex briefly, not each other.
func (s *CloudSink) drainLoop(ctx context.Context) {
	defer s.wg.Done()
	var backoff time.Duration
	for {
		wait := drainPollInterval
		if backoff > 0 {
			wait = backoff
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		f, ok, err := s.buffer.Peek()
		if err != nil {
			s.logger.Warn("cloud buffer: peek failed", slog.Any("error", err))
			continue
		}
		if !ok {
			backoff = 0
			continue
		}

		if s.limiter != nil {
			if err := s.limiter.Wait(ctx, int64(len(f.JPEG))); err != nil {
				if ctx.Err() != nil {
					// Real shutdown: stop cleanly without discarding or
					// advancing the still-buffered frame.
					return
				}
				// Not a cancellation: this frame's JPEG permanently
				// exceeds the currently configured burst capacity (e.g.
				// GEOCAM_CLOUD_BURST_BYTES was lowered, or the frame
				// predates a resolution/quality change), so no amount of
				// waiting will ever admit it under this config. Killing
				// the whole drain goroutine over one such frame would
				// silently stall every frame behind it too — instead,
				// discard only this frame (never bypass the limiter) and
				// keep draining the rest of the spool.
				s.droppedOversize.Add(1)
				s.logger.Warn("cloud buffer: discarding frame that exceeds configured burst capacity",
					"candidate_key", f.CandidateKey, "seq", f.Seq, slog.Any("error", err))
				s.buffer.Discard()
				backoff = 0
				continue
			}
		}

		if f.Kind == KindAnprCandidate {
			// Hito J6: replay an ANPR candidate through the SAME buffer/
			// limiter/backoff machinery a frame would use (item #23/#28).
			if sender, ok := s.sender.(ANPRSender); ok {
				err = s.uploadANPR(ctx, sender, f.AnprCandidate, f.JPEG)
			} else {
				err = errors.New("cloudsink: configured sender does not support ANPR candidates")
			}
		} else {
			err = s.upload(ctx, processing.Frame{
				CandidateKey:    f.CandidateKey,
				Seq:             f.Seq,
				Timestamp:       f.Timestamp,
				ProcessingMode:  f.ProcessingMode,
				CandidateReason: f.CandidateReason,
				CandidateScore:  f.CandidateScore,
				CorrelationID:   f.CorrelationID,
			}, f.JPEG)
		}

		switch {
		case err == nil:
			s.buffer.Advance()
			backoff = 0
			s.logger.Debug("buffered frame replayed", "candidate_key", f.CandidateKey, "seq", f.Seq)
			if s.afterReplay != nil {
				s.afterReplay()
			}
		case errors.Is(err, context.Canceled):
			return
		case errors.Is(err, transport.ErrUnauthorized):
			// Auth failure: credential rejected or revoked. Degrade and preserve
			// durable data without discarding the frame or performing an aggressive
			// retry storm. Back off to maxDrainBackoff until credentials are valid.
			backoff = maxDrainBackoff
			s.logger.Warn("cloud buffer: auth rejected during replay, backing off and retaining frame",
				"candidate_key", f.CandidateKey, "seq", f.Seq, slog.Any("error", err), slog.Duration("backoff", backoff))
		case !isRecoverable(err):
			s.logger.Warn("cloud buffer: dropping frame after non-recoverable replay error",
				"candidate_key", f.CandidateKey, "seq", f.Seq, slog.Any("error", err))
			s.buffer.Discard()
			backoff = 0
		default:
			var rle *transport.RateLimitError
			if errors.As(err, &rle) && rle.RetryAfter > 0 {
				backoff = rle.RetryAfter
				if backoff > maxDrainBackoff {
					backoff = maxDrainBackoff
				}
			} else if backoff == 0 {
				backoff = minDrainBackoff
			} else if backoff *= 2; backoff > maxDrainBackoff {
				backoff = maxDrainBackoff
			}
			s.logger.Debug("cloud buffer: replay failed, backing off",
				slog.Any("error", err), slog.Duration("backoff", backoff))
		}
	}
}

// yuv420pToImage is now internal/processing.YUV420PToImage, shared with
// internal/vision (Milestone K) — see that function's doc comment.
var yuv420pToImage = processing.YUV420PToImage
