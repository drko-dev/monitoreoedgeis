package rtsp

import (
	"context"
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

	mu     sync.RWMutex
	status CameraStreamStatus

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
func (s *Supervisor) Start(ctx context.Context) {
	subCtx, cancel := context.WithCancel(ctx)
	s.stopFunc = cancel

	go s.run(subCtx)
}

// Stop signals the supervisor to shut down and waits for it to complete.
func (s *Supervisor) Stop() {
	if s.stopFunc != nil {
		s.stopFunc()
	}
	<-s.doneChan
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

	backoff := s.cfg.InitialBackoff
	if backoff <= 0 {
		backoff = 1 * time.Second
	}
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

		s.mu.Lock()
		s.status.Status = StateConnecting
		s.mu.Unlock()

		s.logger.Info("connecting to camera stream", "addr", s.target.Addr, "path", s.target.RTSPPath)

		session, err := Dial(ctx, s.target.Addr, s.target.RTSPPath, s.target.Username, s.target.Password, s.cfg.DialTimeout)
		if err != nil {
			s.recordError(err, StateDegraded)
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
		s.mu.Unlock()

		s.logger.Info("camera stream connected and playing", "addr", s.target.Addr)
		backoff = s.cfg.InitialBackoff // Reset backoff on successful session

		// Stream reading loop
		err = s.streamLoop(ctx, session)
		_ = session.Teardown(2 * time.Second)

		if ctx.Err() != nil {
			return
		}

		// Loop exited due to error or silent stream
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
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_, payload, err := session.ReadPacket(s.cfg.PacketTimeout)
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		s.mu.Lock()
		s.status.PacketsReceived++
		s.status.BytesReceived += int64(len(payload))
		s.status.LastPacketAt = &now
		s.status.Status = StateOnline
		s.mu.Unlock()
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

func (s *Supervisor) safeError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > 255 {
		msg = msg[:255]
	}
	return msg
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
