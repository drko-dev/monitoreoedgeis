package vision

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// Worker states, published under /status. NOT_READY variants are never
// treated as an error the agent retries aggressively — they reflect real,
// expected conditions (no models installed, worker not configured).
const (
	StateNotConfigured = "not_configured" // no GEOCAM_EDGE_YOLO_WORKER_CMD
	StateModelMissing  = "model_missing"  // ModelManager.Ready() == false
	StateStarting      = "starting"
	StateReady         = "ready"
	StateRestarting    = "restarting"
	StateStopped       = "stopped"
	StateError         = "error"
)

// WorkerStatus is Worker's point-in-time /status snapshot (K4). Never
// includes an RTSP URI, credential, or frame bytes — only counters and
// state, matching every other Sink/pipeline status block in this repo.
type WorkerStatus struct {
	State           string   `json:"state"`
	Device          string   `json:"device"`
	ModelsLoaded    []string `json:"models_loaded,omitempty"`
	Restarts        int64    `json:"restarts"`
	InferenceCount  int64    `json:"inference_count"`
	InferenceErrors int64    `json:"inference_errors"`
	LastInferenceMs float64  `json:"last_inference_ms"`
	LastError       string   `json:"last_error,omitempty"`
}

// Worker owns the Python vision-worker subprocess's full lifecycle: start,
// health, one-at-a-time inference requests, timeout, shutdown, and
// restart-with-backoff (K2). It never embeds PyTorch in this binary — it
// only spawns/supervises an external process and speaks JSON over a Unix
// domain socket, which Config.SocketPath keeps loopback-local (never a TCP
// listener reachable off this host).
type Worker struct {
	cfg    Config
	models *ModelManager
	logger *slog.Logger

	mu    sync.Mutex
	state string
	conn  net.Conn
	rd    *bufio.Reader
	cmd   *exec.Cmd

	device          string
	modelsLoaded    []string
	lastError       string
	restarts        atomic.Int64
	inferenceCount  atomic.Int64
	inferenceErrors atomic.Int64
	lastInferenceMs atomic.Value // float64

	stopCh chan struct{}
	doneCh chan struct{}

	// onStateChange, if set, fires after every setState — this is what lets
	// Sink push a fresh Status to the health reporter on every lifecycle
	// transition (ready/model_missing/error/...), not only after a frame
	// happens to be routed. Without it /readyz's edge-mode gate (K1) would
	// stay 503 forever on a camera that never streams a frame, even once
	// the worker itself is genuinely ready.
	onStateChange func()
}

// SetOnStateChange registers a callback invoked (without arguments) after
// every worker state transition. Only one callback is supported; must be
// called before Start.
func (w *Worker) SetOnStateChange(fn func()) { w.onStateChange = fn }

// Config is the subset of config.Config the vision package needs, decoupled
// from the config package itself so vision has no import-cycle risk and its
// tests need not construct a full agent config.
type Config struct {
	WorkerCmd         string
	WorkerArgs        []string
	ModelsDir         string
	PersonModel       string
	VehicleModel      string
	PersonConfidence  float64
	VehicleConfidence float64
	NMSIoU            float64
	Device            string
	ImgSize           int
	SocketPath        string
	StartTimeout      time.Duration
	InferTimeout      time.Duration
}

// NewWorker builds a Worker. It does not start the subprocess — call Start.
func NewWorker(cfg Config, models *ModelManager, logger *slog.Logger) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	w := &Worker{
		cfg:    cfg,
		models: models,
		logger: logger,
		state:  StateNotConfigured,
	}
	w.lastInferenceMs.Store(float64(0))
	return w
}

// ModelManager returns the ModelManager used by this worker.
func (w *Worker) ModelManager() *ModelManager { return w.models }

// Ready reports whether the worker currently has a live, health-checked
// connection ready to accept inference requests.
func (w *Worker) Ready() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state == StateReady
}

func (w *Worker) setState(s string) {
	w.mu.Lock()
	w.state = s
	w.mu.Unlock()
	if w.onStateChange != nil {
		w.onStateChange()
	}
}

func (w *Worker) setError(err error) {
	w.mu.Lock()
	w.lastError = err.Error()
	w.mu.Unlock()
}

// Start launches the supervisor loop in the background and returns
// immediately — it never blocks agent startup on a slow or absent worker.
// A worker that never becomes ready simply stays in a NOT_READY-family
// state forever, visible under /status, rather than crashing the agent.
func (w *Worker) Start(ctx context.Context) error {
	if w.cfg.WorkerCmd == "" {
		w.setState(StateNotConfigured)
		w.logger.Warn("edge vision worker not started: GEOCAM_EDGE_YOLO_WORKER_CMD is not configured")
		w.stopCh = make(chan struct{})
		w.doneCh = make(chan struct{})
		close(w.doneCh)
		return nil
	}
	w.stopCh = make(chan struct{})
	w.doneCh = make(chan struct{})
	w.setState(StateStarting)
	go w.supervise(ctx)
	return nil
}

// WaitForReady blocks until the worker reaches StateReady, ctx is cancelled,
// or a terminal unready state (StateNotConfigured, StateModelMissing, StateError)
// is detected without reaching ready.
func (w *Worker) WaitForReady(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		w.mu.Lock()
		state := w.state
		lastErr := w.lastError
		w.mu.Unlock()

		switch state {
		case StateReady:
			return nil
		case StateNotConfigured:
			return errors.New("vision worker: GEOCAM_EDGE_YOLO_WORKER_CMD is not configured (state=not_configured)")
		case StateModelMissing:
			return errors.New("vision worker: model weights missing or unready (state=model_missing)")
		case StateError:
			if lastErr != "" {
				return fmt.Errorf("vision worker: startup error (state=error): %s", lastErr)
			}
			return errors.New("vision worker: startup error (state=error)")
		case StateStopped:
			return errors.New("vision worker: worker is stopped")
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("vision worker: timed out waiting for ready state (current state=%s): %w", state, ctx.Err())
		case <-ticker.C:
		}
	}
}

// Stop signals the supervisor loop to exit, closes any live connection, and
// kills the subprocess if it does not exit on its own within StartTimeout.
// Bounded and synchronous: Router.Stop (via sinkCloser) relies on this never
// hanging forever.
func (w *Worker) Stop(ctx context.Context) error {
	if w.stopCh == nil {
		return nil
	}
	select {
	case <-w.stopCh:
		// already closed
	default:
		close(w.stopCh)
	}
	select {
	case <-w.doneCh:
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(w.shutdownTimeout()):
		return errors.New("vision worker: shutdown timed out")
	}
	w.setState(StateStopped)
	return nil
}

func (w *Worker) shutdownTimeout() time.Duration {
	if w.cfg.StartTimeout > 0 {
		return w.cfg.StartTimeout
	}
	return 30 * time.Second
}

// supervise is the single goroutine that owns w.cmd/w.conn end to end: model
// readiness checks, subprocess spawn, health handshake, and
// restart-with-backoff on death — mirroring the same bounded-backoff
// reconnect pattern internal/processing.cameraPipeline.run already uses for
// its decoder subprocess, rather than inventing a second one.
func (w *Worker) supervise(ctx context.Context) {
	defer close(w.doneCh)
	backoff := 1 * time.Second
	const maxBackoff = 30 * time.Second

	for {
		select {
		case <-w.stopCh:
			w.shutdownProcess()
			return
		case <-ctx.Done():
			w.shutdownProcess()
			return
		default:
		}

		if !w.models.Ready() {
			w.setState(StateModelMissing)
			if !w.sleep(ctx, 5*time.Second) {
				w.shutdownProcess()
				return
			}
			continue
		}

		w.setState(StateStarting)
		if err := w.spawnAndHandshake(ctx); err != nil {
			w.setError(err)
			w.setState(StateError)
			w.logger.Warn("edge vision worker failed to start, backing off",
				slog.Any("error", err), slog.Duration("backoff", backoff))
			w.restarts.Add(1)
			w.shutdownProcess()
			if !w.sleep(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = 1 * time.Second
		w.setState(StateReady)

		// Snapshot cmd under the lock rather than reading w.cmd from the
		// goroutine below: shutdownProcess clears w.cmd concurrently (from
		// the stopCh/ctx.Done branches racing with this same death-wait),
		// so an unsynchronized w.cmd.Wait() would be a data race.
		w.mu.Lock()
		cmd := w.cmd
		w.mu.Unlock()

		// Block until the subprocess dies or we're asked to stop.
		died := make(chan struct{})
		go func() {
			if cmd != nil {
				_ = cmd.Wait()
			}
			close(died)
		}()
		select {
		case <-w.stopCh:
			w.shutdownProcess()
			return
		case <-ctx.Done():
			w.shutdownProcess()
			return
		case <-died:
			w.restarts.Add(1)
			w.setState(StateRestarting)
			w.logger.Warn("edge vision worker process exited, restarting")
			w.shutdownProcess()
			if !w.sleep(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, maxBackoff)
		}
	}
}

func (w *Worker) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-w.stopCh:
		return false
	case <-t.C:
		return true
	}
}

func (w *Worker) spawnAndHandshake(ctx context.Context) error {
	_ = os.Remove(w.cfg.SocketPath) // stale socket from a prior crashed run

	args := append([]string{}, w.cfg.WorkerArgs...)
	args = append(args,
		"--socket", w.cfg.SocketPath,
		"--person-model", w.models.PersonModelPath(),
		"--vehicle-model", w.models.VehicleModelPath(),
		"--person-confidence", ftoa(w.cfg.PersonConfidence),
		"--vehicle-confidence", ftoa(w.cfg.VehicleConfidence),
		"--nms-iou", ftoa(w.cfg.NMSIoU),
		"--device", w.cfg.Device,
		"--imgsz", itoa(w.cfg.ImgSize),
	)
	cmd := exec.Command(w.cfg.WorkerCmd, args...)
	cmd.Stdout = w.logWriter("stdout")
	cmd.Stderr = w.logWriter("stderr")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn worker: %w", err)
	}
	w.mu.Lock()
	w.cmd = cmd
	w.mu.Unlock()

	startCtx, cancel := context.WithTimeout(ctx, w.startTimeout())
	defer cancel()

	conn, err := dialUnixWithRetry(startCtx, w.cfg.SocketPath)
	if err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("connect to worker socket: %w", err)
	}

	w.mu.Lock()
	w.conn = conn
	w.rd = bufio.NewReader(conn)
	w.mu.Unlock()

	resp, err := w.roundTrip(startCtx, wireRequest{Type: "health"})
	if err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("health handshake: %w", err)
	}
	if resp.Type != "health_ok" || !resp.Ready {
		_ = cmd.Process.Kill()
		return fmt.Errorf("worker health check reported not ready: %s", resp.Error)
	}
	w.mu.Lock()
	w.device = resp.Device
	w.modelsLoaded = resp.ModelsLoaded
	w.mu.Unlock()
	return nil
}

func (w *Worker) startTimeout() time.Duration {
	if w.cfg.StartTimeout > 0 {
		return w.cfg.StartTimeout
	}
	return 30 * time.Second
}

func (w *Worker) logWriter(stream string) *slogWriter {
	return &slogWriter{logger: w.logger, stream: stream}
}

// Infer sends one frame to the worker and blocks for its result, bounded by
// ctx (the Sink applies Config.InferTimeout). Only one Infer call is ever
// in flight in practice: Router runs exactly one worker goroutine per sink
// (see internal/processing/router.go), so this needs no internal queue —
// the mutex below is purely a correctness guard, not a throughput limiter.
func (w *Worker) Infer(ctx context.Context, req InferRequest) (InferenceResult, error) {
	if !w.Ready() {
		return InferenceResult{}, errors.New("vision worker not ready")
	}
	resp, err := w.roundTrip(ctx, wireRequest{
		Type:         "infer",
		CandidateKey: req.CandidateKey,
		FrameSeq:     req.FrameSeq,
		TimestampMS:  req.Timestamp.UnixMilli(),
		Width:        req.Width,
		Height:       req.Height,
		JPEG:         req.JPEG,
	})
	if err != nil {
		w.inferenceErrors.Add(1)
		return InferenceResult{}, err
	}
	if resp.Type == "error" {
		w.inferenceErrors.Add(1)
		return InferenceResult{}, fmt.Errorf("worker inference error: %s", resp.Error)
	}
	w.inferenceCount.Add(1)
	w.lastInferenceMs.Store(resp.InferenceMS)
	return InferenceResult{
		CandidateKey:  req.CandidateKey,
		FrameSeq:      resp.FrameSeq,
		Timestamp:     req.Timestamp,
		InferenceMS:   resp.InferenceMS,
		Device:        w.deviceName(),
		ModelsLoaded:  w.modelsLoadedNames(),
		Detections:    resp.Detections,
		CorrelationID: req.CorrelationID,
	}, nil
}

func (w *Worker) deviceName() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.device
}

func (w *Worker) modelsLoadedNames() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.modelsLoaded
}

// roundTrip writes one request line and reads exactly one response line.
// The connection is single-writer/single-reader by construction (see
// Infer's doc comment on why no queue is needed), so no locking is needed
// around the write+read pair itself — only around swapping w.conn/w.rd,
// which shutdownProcess and spawnAndHandshake do under w.mu.
func (w *Worker) roundTrip(ctx context.Context, req wireRequest) (wireResponse, error) {
	w.mu.Lock()
	conn, rd := w.conn, w.rd
	w.mu.Unlock()
	if conn == nil || rd == nil {
		return wireResponse{}, errors.New("vision worker: no connection")
	}

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Time{})
	}
	defer conn.SetDeadline(time.Time{})

	line, err := json.Marshal(req)
	if err != nil {
		return wireResponse{}, err
	}
	line = append(line, '\n')
	if _, err := conn.Write(line); err != nil {
		return wireResponse{}, fmt.Errorf("write request: %w", err)
	}

	respLine, err := rd.ReadBytes('\n')
	if err != nil {
		return wireResponse{}, fmt.Errorf("read response: %w", err)
	}
	var resp wireResponse
	if err := json.Unmarshal(respLine, &resp); err != nil {
		return wireResponse{}, fmt.Errorf("decode response: %w", err)
	}
	return resp, nil
}

// shutdownProcess closes the connection and terminates the subprocess,
// waiting briefly for a graceful exit before killing it. Safe to call
// repeatedly and on a nil process.
func (w *Worker) shutdownProcess() {
	w.mu.Lock()
	conn := w.conn
	cmd := w.cmd
	w.conn = nil
	w.rd = nil
	w.cmd = nil
	w.mu.Unlock()

	if conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = w.roundTripOn(ctx, conn, wireRequest{Type: "shutdown"})
		cancel()
		_ = conn.Close()
	}
	if cmd != nil && cmd.Process != nil {
		done := make(chan struct{})
		go func() { _, _ = cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
	_ = os.Remove(w.cfg.SocketPath)
}

// roundTripOn is roundTrip against an explicit connection, used only by
// shutdownProcess after w.conn has already been cleared under the lock.
func (w *Worker) roundTripOn(ctx context.Context, conn net.Conn, req wireRequest) (wireResponse, error) {
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	line, err := json.Marshal(req)
	if err != nil {
		return wireResponse{}, err
	}
	line = append(line, '\n')
	if _, err := conn.Write(line); err != nil {
		return wireResponse{}, err
	}
	rd := bufio.NewReader(conn)
	respLine, err := rd.ReadBytes('\n')
	if err != nil {
		return wireResponse{}, err
	}
	var resp wireResponse
	_ = json.Unmarshal(respLine, &resp)
	return resp, nil
}

// Status returns a point-in-time WorkerStatus snapshot (K4).
func (w *Worker) Status() WorkerStatus {
	w.mu.Lock()
	state := w.state
	device := w.device
	models := w.modelsLoaded
	lastErr := w.lastError
	w.mu.Unlock()
	lastMs, _ := w.lastInferenceMs.Load().(float64)
	return WorkerStatus{
		State:           state,
		Device:          device,
		ModelsLoaded:    models,
		Restarts:        w.restarts.Load(),
		InferenceCount:  w.inferenceCount.Load(),
		InferenceErrors: w.inferenceErrors.Load(),
		LastInferenceMs: lastMs,
		LastError:       lastErr,
	}
}

func dialUnixWithRetry(ctx context.Context, path string) (net.Conn, error) {
	backoff := 50 * time.Millisecond
	for {
		conn, err := net.Dial("unix", path)
		if err == nil {
			return conn, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w (last dial error: %v)", ctx.Err(), err)
		case <-time.After(backoff):
		}
		if backoff < 500*time.Millisecond {
			backoff *= 2
		}
	}
}
