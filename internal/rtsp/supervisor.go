package rtsp

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// CameraTarget specifies target parameters for a camera RTSP stream.
type CameraTarget struct {
	CandidateKey string
	Addr         string // host:port
	RTSPPath     string // /stream2 or /stream1
	Username     string
	Password     string
	StreamRole   string // "sub" or "main"
	Codec        string
	Width        int
	Height       int
	FPS          float64
}

// Supervisor manages the connection lifecycle, read loop, and reconnect backoff
// for a single camera stream (G1-G4, G9, G11).
type Supervisor struct {
	target CameraTarget
	cfg    Config
	logger *slog.Logger

	mu         sync.RWMutex
	status     CameraStreamStatus
	sink       PacketSink
	descriptor StreamDescriptor
	descReady  bool

	// started guards the run goroutine. Start is idempotent and Stop on a
	// supervisor that was never started returns instead of blocking on a
	// done channel nothing will ever close.
	started  bool
	stopFunc context.CancelFunc
	doneChan chan struct{}
}

// NewSupervisor creates a supervisor for the given camera target and config.
func NewSupervisor(target CameraTarget, cfg Config, logger *slog.Logger) *Supervisor {
	if logger == nil {
		logger = slog.Default()
	}
	role := target.StreamRole
	if role == "" {
		role = cfg.StreamRole
	}
	if role == "" {
		role = StreamRoleSub
	}

	s := &Supervisor{
		target: target,
		cfg:    cfg,
		logger: logger.With("candidate_key", target.CandidateKey, "stream_role", role),
		status: CameraStreamStatus{
			CandidateKey: target.CandidateKey,
			Status:       StateConnecting,
			StreamRole:   role,
			Codec:        target.Codec,
			Width:        target.Width,
			Height:       target.Height,
			FPS:          target.FPS,
		},
		doneChan: make(chan struct{}),
	}
	return s
}

// Start launches the supervision goroutine in the background.
//
// Start is idempotent. A second call is a no-op rather than a second
// goroutine: two goroutines would both close doneChan on exit (panicking
// with "close of closed channel") and two supervisors for one camera would
// double the connections and the packet stream.
func (s *Supervisor) Start(ctx context.Context) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	subCtx, cancel := context.WithCancel(ctx)
	s.stopFunc = cancel
	s.started = true
	s.mu.Unlock()

	go s.run(subCtx)
}

// Stop signals the supervisor to shut down and waits for it to complete.
//
// Stop on a supervisor that was never started returns immediately: no
// goroutine exists to close doneChan, so waiting on it would block forever.
// Calling Stop twice is safe for the same reason — the channel is already
// closed.
func (s *Supervisor) Stop() {
	s.mu.RLock()
	started := s.started
	stop := s.stopFunc
	s.mu.RUnlock()

	if !started {
		return
	}
	if stop != nil {
		stop()
	}
	<-s.doneChan
}

// SetPacketSink registers sink to receive this supervisor's video RTP
// payloads. Passing nil deregisters it. Safe for concurrent use with the
// read loop delivering packets.
func (s *Supervisor) SetPacketSink(sink PacketSink) {
	s.mu.Lock()
	s.sink = sink
	s.mu.Unlock()
}

// Descriptor returns the stream's non-sensitive metadata (codec, dimensions,
// SPS/PPS, ...) once resolved from a successful connection, and whether it
// is ready yet.
func (s *Supervisor) Descriptor() (StreamDescriptor, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.descriptor, s.descReady
}

// SetDescriptor sets the stream descriptor (used in tests and simulation).
func (s *Supervisor) SetDescriptor(desc StreamDescriptor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.descriptor = desc
	s.descReady = true
}

// Snapshot returns a copy of the current camera stream status.
func (s *Supervisor) Snapshot() CameraStreamStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cp := s.status
	if s.status.LastPacketAt != nil {
		t := *s.status.LastPacketAt
		cp.LastPacketAt = &t
	}
	return cp
}

func (s *Supervisor) run(ctx context.Context) {
	defer close(s.doneChan)
	defer func() {
		s.mu.Lock()
		s.status.Status = StateOffline
		s.mu.Unlock()
	}()

	initialBackoff := s.cfg.InitialBackoff
	if initialBackoff <= 0 {
		initialBackoff = 1 * time.Second
	}
	backoff := initialBackoff
	maxBackoff := s.cfg.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = 60 * time.Second
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		s.logger.Info("connecting to camera stream", "addr", s.target.Addr, "path", s.target.RTSPPath)

		session, err := Dial(ctx, s.target.Addr, s.target.RTSPPath, s.target.Username, s.target.Password, s.cfg.DialTimeout)
		if err != nil {
			if errors.Is(err, ErrTimeout) || isTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
				s.incrementTimeout()
			}
			state := StateDegraded
			if errors.Is(err, ErrAuthFailed) {
				state = StateAuthFailed
			}
			s.recordError(err, state)
			s.logger.Warn("stream dial failed, backing off", "error", s.safeError(err), "backoff", backoff)

			if !s.sleepBackoff(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, maxBackoff)
			s.incrementReconnect()
			continue
		}

		// Connected successfully!
		s.mu.Lock()
		s.status.Status = StateOnline
		s.status.LastErrorSafe = ""
		// Prefer the codec SDP actually declares for the video payload
		// type (RFC 4566 a=rtpmap) over the ONVIF-reported one: SDP is the
		// on-the-wire source of truth, and ONVIF GetProfiles responses can
		// mismap video/audio encoder metadata on some devices. This only
		// affects what the video pipeline (Hito H) selects a decoder for
		// — CameraStreamStatus.Codec (surfaced elsewhere) keeps the
		// ONVIF-reported value unchanged.
		codec := s.status.Codec
		if sdpCodec := session.Codec(); sdpCodec != "" {
			codec = sdpCodec
		}
		s.descriptor = StreamDescriptor{
			CandidateKey:       s.target.CandidateKey,
			Codec:              codec,
			Width:              s.status.Width,
			Height:             s.status.Height,
			FPS:                s.status.FPS,
			StreamRole:         s.status.StreamRole,
			SpropParameterSets: session.SpropParameterSets(),
		}
		s.descReady = true
		s.mu.Unlock()

		s.logger.Info("camera stream connected and playing", "addr", s.target.Addr)
		// Reset to the *normalised* initial delay, never to the raw config
		// field: with a zero InitialBackoff the raw field would put the
		// retry loop back to a zero delay after every successful session,
		// turning a flapping camera into an unthrottled reconnect storm.
		backoff = initialBackoff

		// Stream reading loop
		err = s.streamLoop(ctx, session)
		_ = session.Teardown(2 * time.Second)

		if ctx.Err() != nil {
			return
		}

		// Loop exited due to error or silent stream. This is a stall of an
		// already-connected/playing stream, distinct from a dial timeout
		// (line ~153): both count as a timeout, but only this one is also
		// a stall.
		if errors.Is(err, ErrTimeout) || isTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
			s.incrementTimeout()
			s.incrementStreamStall()
		}
		s.recordError(err, StateDegraded)
		s.incrementReconnect()
		s.logger.Warn("stream interrupted, reconnecting", "error", s.safeError(err), "backoff", backoff)

		if !s.sleepBackoff(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (s *Supervisor) streamLoop(ctx context.Context, session *Session) error {
	// The video channel is whatever SETUP actually negotiated for this
	// session (session.VideoChannel()), never a hardcoded number — RTCP on
	// the sibling channel must never reach PacketSink.
	videoChannel := session.VideoChannel()

	// Config documents PacketTimeout as the silence threshold with a 5s
	// default, and the supervisor substitutes defaults for InitialBackoff,
	// MaxBackoff and DialTimeout. A non-positive PacketTimeout must
	// therefore mean "use the default", not "never time out": ReadPacket
	// treats a non-positive timeout as "no deadline", which would let a
	// camera that went silent at the socket level look healthy forever.
	packetTimeout := s.cfg.PacketTimeout
	if packetTimeout <= 0 {
		packetTimeout = DefaultConfig().PacketTimeout
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		channel, payload, err := session.ReadPacket(packetTimeout)
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		s.mu.Lock()
		s.status.PacketsReceived++
		s.status.BytesReceived += int64(len(payload))
		s.status.LastPacketAt = &now
		s.status.Status = StateOnline
		sink := s.sink
		s.mu.Unlock()

		if channel == videoChannel && sink != nil {
			sink.OnPacket(s.target.CandidateKey, payload, now)
		}
	}
}

func (s *Supervisor) sleepBackoff(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Supervisor) recordError(err error, state State) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.status.Status = state
	s.status.LastErrorSafe = s.safeError(err)
}

func (s *Supervisor) incrementReconnect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.ReconnectCount++
}

// incrementTimeout counts any failure classified as a timeout: dial
// timeout, handshake timeout (both inside Dial), or a stream read timeout.
// It never touches StallCount on its own — a timeout before the stream was
// ever connected/playing is not a stall of an established stream.
func (s *Supervisor) incrementTimeout() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.TimeoutCount++
}

// incrementStreamStall counts a timeout/silence on a stream that was
// already connected and playing. Callers must also call incrementTimeout
// for the same event — every stall is a timeout, not every timeout is a
// stall.
func (s *Supervisor) incrementStreamStall() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.StallCount++
}

func (s *Supervisor) safeError(err error) string {
	return SanitizeError(err)
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
