package agent

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/systemd"
)

const (
	processWatchdogPeriod       = 10 * time.Second
	processWatchdogProbeTimeout = 3 * time.Second
	processWatchdogMissLimit    = 3
)

// livenessProbe reports whether the process can still do the one thing that
// proves it is not wedged: answer its own local health surface.
//
// That is the right signal for a *process* watchdog. A deadlock, a runtime
// wedge, or a stuck accept loop all make this fail, while a single slow camera,
// a broken decoder or an unreachable SaaS intentionally do not — those have
// their own supervised reconnect loops with their own backoff, and restarting
// the whole Edge for them is exactly what Y10 says not to do.
type livenessProbe interface {
	Probe(ctx context.Context) error
}

// startWatchdog arms the systemd watchdog when — and only when — systemd asked
// for one by setting WATCHDOG_USEC. Outside a unit that declares WatchdogSec=
// this returns immediately and the agent behaves exactly as before.
//
// The liveness probe is supplied here so the agent's own health server is the
// thing being probed; see runWatchdogLoop for the ping policy.
func (a *Agent) startWatchdog(ctx context.Context, probe livenessProbe) {
	interval, ok := systemd.WatchdogInterval()
	if !ok {
		return
	}
	if probe == nil {
		a.log.Warn("systemd watchdog is configured but no liveness probe is available; not arming it")
		return
	}
	a.log.Info("systemd watchdog armed",
		slog.Duration("watchdog_interval", interval),
		slog.Duration("ping_period", watchdogPeriod(interval)))
	go runWatchdogLoop(ctx, interval, probe, a.log)
}

// startServiceProcessWatchdog is the fallback for launchd/Windows SCM, which
// restart a process after exit but cannot probe a Go process that is still
// alive and hung. A missed local /healthz probe makes Agent.Run return a
// failure; the native manager then applies its bounded restart policy. Camera,
// RTSP and SaaS health are deliberately not part of this probe.
func (a *Agent) startServiceProcessWatchdog(ctx context.Context, probe livenessProbe) <-chan error {
	if a.cfg == nil || !a.cfg.ServiceManaged || systemd.Enabled() || probe == nil {
		return nil
	}
	fatal := make(chan error, 1)
	go runServiceProcessWatchdog(ctx, processWatchdogPeriod, processWatchdogProbeTimeout, processWatchdogMissLimit, probe, fatal, a.log)
	return fatal
}

func runServiceProcessWatchdog(
	ctx context.Context,
	period, probeTimeout time.Duration,
	missLimit int,
	probe livenessProbe,
	fatal chan<- error,
	log *slog.Logger,
) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	missed := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		err := probe.Probe(probeCtx)
		cancel()
		if err == nil {
			missed = 0
			continue
		}
		if ctx.Err() != nil {
			return
		}
		missed++
		if log != nil {
			log.Warn("service liveness probe failed", slog.Int("consecutive_failures", missed), slog.String("error_type", fmt.Sprintf("%T", err)))
		}
		if missed >= missLimit {
			fatal <- fmt.Errorf("service liveness probe failed %d consecutive times", missed)
			return
		}
	}
}

// watchdogPeriod is how often the loop re-checks liveness and pings. systemd's
// own documentation suggests pinging at half the interval; a third is used here
// so two consecutive missed checks are still tolerated before the watchdog
// deadline expires. On a loaded appliance that matters: a momentary stall under
// heavy decode/inference load must not be mistaken for a hang and kill a working
// Edge.
func watchdogPeriod(watchdogInterval time.Duration) time.Duration {
	period := watchdogInterval / 3
	if period <= 0 {
		return watchdogInterval
	}
	return period
}

// runWatchdogLoop pings WATCHDOG=1 for as long as probe keeps succeeding.
//
// The gate is the whole point: a process that is merely running gets no ping.
// If the probe stops succeeding, the pings stop, systemd's timer expires, and
// systemd restarts the unit — which is the one thing `Restart=on-failure` cannot
// do for a process that never exits.
func runWatchdogLoop(ctx context.Context, watchdogInterval time.Duration, probe livenessProbe, log *slog.Logger) {
	period := watchdogPeriod(watchdogInterval)
	ticker := time.NewTicker(period)
	defer ticker.Stop()

	missed := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		probeCtx, cancel := context.WithTimeout(ctx, period)
		err := probe.Probe(probeCtx)
		cancel()
		if err != nil {
			// A cancelled context means shutdown, not a wedged process: stop
			// quietly rather than logging a failure on the way out.
			if ctx.Err() != nil {
				return
			}
			missed++
			if log != nil {
				log.Warn("watchdog liveness probe failed; withholding the systemd ping",
					slog.Any("error", err),
					slog.Int("consecutive_failures", missed),
					slog.Duration("watchdog_interval", watchdogInterval))
			}
			continue
		}
		if missed > 0 && log != nil {
			log.Info("watchdog liveness probe recovered", slog.Int("missed_checks", missed))
		}
		missed = 0

		if err := systemd.Watchdog(); err != nil && log != nil {
			// The agent keeps running: a notify failure is systemd's problem to
			// surface (the unit will be killed and restarted by its own limit),
			// and taking the Edge down over it would be strictly worse.
			log.Warn("systemd watchdog ping failed", slog.Any("error", err))
		}
	}
}

// notifySystemdReady completes Type=notify startup.
//
// It is sent once the modules have started, whether or not the agent reached
// READY. That is deliberate: a DEGRADED agent is still serving /status and its
// local function may be perfectly intact, so refusing to notify would make
// systemd kill and restart it — a restart loop for a condition (a corrupt
// identity file, a revoked credential) that restarting cannot fix. The
// readiness verdict belongs to /readyz, which reports DEGRADED as 503 without
// asking systemd to intervene.
func (a *Agent) notifySystemdReady() {
	if !systemd.Enabled() {
		return
	}
	if err := systemd.Ready(); err != nil {
		a.log.Warn("systemd READY notification failed", slog.Any("error", err))
		return
	}
	a.log.Info("systemd notified READY", slog.String("status", a.health.State().String()))
}

// notifySystemdStopping tells systemd a clean shutdown has begun, which also
// cancels its watchdog timer so a slow-but-deliberate stop is not treated as a
// hang.
func (a *Agent) notifySystemdStopping() {
	if !systemd.Enabled() {
		return
	}
	if err := systemd.Stopping(); err != nil {
		a.log.Warn("systemd STOPPING notification failed", slog.Any("error", err))
	}
}
