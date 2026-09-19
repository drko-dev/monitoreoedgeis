package edgebacklog

// Hito W — W5 (SaaS unreachable) and W8 (process restart), durable-queue half.
//
// The existing backlog tests drive a scripted fake sender. These drive the
// real transport client against a local SaaS that is switched off and on, which
// is the only way to check the property the offline design actually promises:
// a queued local event survives the outage AND survives a restart, is delivered
// exactly once per stage once the SaaS comes back, and is not duplicated by a
// producer that submits the same event twice while it is still pending.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// outageSaaS is a local SaaS that can be taken "off the air": while it is down
// every connection is accepted and dropped mid-flight, which is what the
// transport classifies as ErrSaaSUnavailable (a transient failure the backlog
// must retain and retry, as opposed to ErrInvalidRequest, which it quarantines).
type outageSaaS struct {
	server *httptest.Server
	down   atomic.Bool

	mu     sync.Mutex
	counts map[string]int
	order  []string
}

func newOutageSaaS(t *testing.T) *outageSaaS {
	t.Helper()
	s := &outageSaaS{counts: map[string]int{}}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.server.Close)
	return s
}

func (s *outageSaaS) url() string { return s.server.URL }

func (s *outageSaaS) setDown(down bool) { s.down.Store(down) }

func (s *outageSaaS) handle(w http.ResponseWriter, r *http.Request) {
	if s.down.Load() {
		if hijacker, ok := w.(http.Hijacker); ok {
			if conn, _, err := hijacker.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	_, _ = io.Copy(io.Discard, r.Body)

	key := r.Method + " " + r.URL.Path
	s.mu.Lock()
	s.counts[key]++
	s.order = append(s.order, key)
	s.mu.Unlock()

	switch r.Method {
	case http.MethodPost:
		w.WriteHeader(http.StatusCreated)
	case http.MethodPut:
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func (s *outageSaaS) delivered(pathSuffix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for key, n := range s.counts {
		if strings.HasSuffix(key, pathSuffix) {
			total += n
		}
	}
	return total
}

func (s *outageSaaS) sequence() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.order...)
}

func TestW5_DurableBacklogSurvivesTheOutageAndDeliversEachStageOnce(t *testing.T) {
	dir := t.TempDir()
	saas := newOutageSaaS(t)
	client, err := transport.New(saas.url(), true, 2*time.Second, "w5-backlog")
	if err != nil {
		t.Fatalf("transport.New: %v", err)
	}

	backlog := openWithConfig(t, dir, time.Millisecond, 5*time.Millisecond)
	event := submission(t, dir, "evt-1")

	if err := backlog.Enqueue(event); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	ctx := context.Background()

	// --- SaaS is unreachable -------------------------------------------
	saas.setDown(true)

	for i := 0; i < 3; i++ {
		backlog.ProcessOne(ctx, client, "device-w5", "edg_live_w5")
	}

	status := backlog.Status()
	if status.BacklogCount != 1 {
		t.Fatalf("backlog_count = %d during an outage, want 1 (the event must be retained)", status.BacklogCount)
	}
	if status.LastError == "" {
		t.Error("last_error is empty after failed sends; the failure was not reported")
	}
	if status.Quarantined != 0 {
		t.Errorf("quarantined = %d after transient failures, want 0 "+
			"(an outage must not discard a durable event)", status.Quarantined)
	}
	if status.Drops != 0 {
		t.Errorf("drops = %d, want 0 (the queue was nowhere near its bound)", status.Drops)
	}
	if got := saas.delivered("/local-events"); got != 0 {
		t.Errorf("the down SaaS recorded %d deliveries, want 0", got)
	}

	// A producer that submits the same event again while it is still pending
	// must not create a second copy of the durable work.
	if err := backlog.Enqueue(event); err != nil {
		t.Fatalf("re-Enqueue of the same pending event: %v", err)
	}
	if got := backlog.Status().BacklogCount; got != 1 {
		t.Errorf("backlog_count = %d after re-submitting the same event, want 1", got)
	}

	// --- SaaS comes back -------------------------------------------------
	saas.setDown(false)

	deadline := time.Now().Add(5 * time.Second)
	for backlog.Status().BacklogCount > 0 && time.Now().Before(deadline) {
		backlog.ProcessOne(ctx, client, "device-w5", "edg_live_w5")
	}

	if got := backlog.Status().BacklogCount; got != 0 {
		t.Fatalf("backlog_count = %d after the SaaS recovered, want 0", got)
	}
	if got := backlog.Status().Quarantined; got != 0 {
		t.Errorf("quarantined = %d after a clean recovery, want 0", got)
	}

	if got := saas.delivered("/local-events"); got != 1 {
		t.Errorf("the metadata stage was delivered %d times, want exactly 1", got)
	}
	if got := saas.delivered("/evidence/capture"); got != 1 {
		t.Errorf("the capture stage was delivered %d times, want exactly 1", got)
	}

	// Metadata strictly precedes its evidence, and no stage is replayed.
	order := saas.sequence()
	if len(order) != 2 {
		t.Fatalf("the SaaS saw %d stage deliveries, want 2: %v", len(order), order)
	}
	if !strings.HasSuffix(order[0], "/local-events") {
		t.Errorf("first delivery = %q, want the event metadata first", order[0])
	}
	if !strings.HasSuffix(order[1], "/evidence/capture") {
		t.Errorf("second delivery = %q, want the capture evidence second", order[1])
	}
}

// A queued event must survive the process (the Backlog is closed and reopened
// against the same directory) and then be delivered exactly once.
func TestW8_PendingEventSurvivesRestartAndIsNotDuplicated(t *testing.T) {
	dir := t.TempDir()
	saas := newOutageSaaS(t)
	client, err := transport.New(saas.url(), true, 2*time.Second, "w8-backlog")
	if err != nil {
		t.Fatalf("transport.New: %v", err)
	}

	saas.setDown(true)

	// --- Process A: the event is produced while the SaaS is down ---------
	backlogA := openWithConfig(t, dir, time.Millisecond, 5*time.Millisecond)
	event := submission(t, dir, "evt-restart")
	if err := backlogA.Enqueue(event); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	// One failed attempt, so the durable record carries a real attempt count.
	backlogA.ProcessOne(context.Background(), client, "device-w8", "edg_live_w8")
	if got := backlogA.Status().BacklogCount; got != 1 {
		t.Fatalf("backlog_count before restart = %d, want 1", got)
	}

	// --- Process B: same directory, fresh in-memory state ----------------
	backlogB := openWithConfig(t, dir, time.Millisecond, 5*time.Millisecond)
	if got := backlogB.Status().BacklogCount; got != 1 {
		t.Fatalf("backlog_count after restart = %d, want 1 (the queue is durable)", got)
	}
	if got := backlogB.Status().Drops; got != 0 {
		t.Errorf("drops after restart = %d, want 0", got)
	}

	// Recover the SaaS and drain in the new process.
	saas.setDown(false)
	deadline := time.Now().Add(5 * time.Second)
	for backlogB.Status().BacklogCount > 0 && time.Now().Before(deadline) {
		backlogB.ProcessOne(context.Background(), client, "device-w8", "edg_live_w8")
	}
	if got := backlogB.Status().BacklogCount; got != 0 {
		t.Fatalf("backlog_count after draining = %d, want 0", got)
	}

	// Exactly one delivery of each stage, even though two processes touched
	// this queue and the first one already attempted the metadata stage.
	if got := saas.delivered("/local-events"); got != 1 {
		t.Errorf("the metadata stage was delivered %d times across the restart, want 1", got)
	}
	if got := saas.delivered("/evidence/capture"); got != 1 {
		t.Errorf("the capture stage was delivered %d times across the restart, want 1", got)
	}

	// A third process finds nothing to do: completed work is not resurrected.
	backlogC := openWithConfig(t, dir, time.Millisecond, 5*time.Millisecond)
	if got := backlogC.Status().BacklogCount; got != 0 {
		t.Errorf("backlog_count in a third process = %d, want 0", got)
	}
	backlogC.ProcessOne(context.Background(), client, "device-w8", "edg_live_w8")
	if got := saas.delivered("/local-events"); got != 1 {
		t.Errorf("a drained queue re-delivered metadata: %d deliveries", got)
	}
}
