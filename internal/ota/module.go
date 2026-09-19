package ota

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

// Client is the outbound transport CheckOnce talks through -- the same
// Edge-initiated, authenticated channel heartbeat/control/remote-config
// use. No inbound port, no second poller.
type Client interface {
	GetNextOTARelease(ctx context.Context, deviceID, credential string) (*transport.OTARelease, error)
}

// Module is the small OTA sync component. CheckOnce is driven externally
// -- after a successful heartbeat (see internal/agent/heartbeat_module.go's
// OnSuccess wiring) -- rather than owning its own poll loop, mirroring
// internal/remoteconfig.Module's externally-driven SyncOnce. Reuses the
// heartbeat cadence instead of adding a second scheduler.
type Module struct {
	client               Client
	downloader           Downloader
	deviceID, credential string
	currentVersion       string
	logger               *slog.Logger

	// running guards against overlapping CheckOnce calls: a heartbeat can
	// succeed again before a slow download finishes.
	running atomic.Bool
	// lastReleaseID is an in-memory fast path so a release already handled
	// this process run does not trigger a redundant SaaS round-trip's
	// worth of work; FileDownloader.FetchAndStage's on-disk "verified.ok"
	// marker is the durable, restart-safe version of the same check.
	lastReleaseID atomic.Value
}

// New builds a Module.
func New(client Client, downloader Downloader, deviceID, credential, currentVersion string, logger *slog.Logger) *Module {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Module{
		client:         client,
		downloader:     downloader,
		deviceID:       deviceID,
		credential:     credential,
		currentVersion: currentVersion,
		logger:         logger,
	}
	m.lastReleaseID.Store("")
	return m
}

// CheckOnce performs a single fetch -> compare -> download -> stage cycle.
// Its error is informational only -- the caller (heartbeat's OnSuccess
// hook) logs and discards it rather than letting an OTA failure turn a
// successful heartbeat into a failed one.
//
// Safe to call concurrently: a call arriving while a previous one is still
// downloading/staging is skipped, not queued -- the next successful
// heartbeat will simply try again.
func (m *Module) CheckOnce(ctx context.Context) error {
	if !m.running.CompareAndSwap(false, true) {
		m.logger.Debug("ota: check already in progress, skipping")
		return nil
	}
	defer m.running.Store(false)

	release, err := m.client.GetNextOTARelease(ctx, m.deviceID, m.credential)
	if err != nil {
		return fmt.Errorf("ota: check for update: %w", err)
	}
	if release == nil {
		return nil
	}

	if id, _ := m.lastReleaseID.Load().(string); id == release.ReleaseID {
		m.logger.Debug("ota: release already processed this run, skipping", slog.String("release_id", release.ReleaseID))
		return nil
	}

	eligible, err := IsUpdateEligible(m.currentVersion, release.Version)
	if err != nil {
		return fmt.Errorf("ota: version comparison: %w", err)
	}
	if !eligible {
		m.logger.Debug("ota: candidate release is not a forward update, skipping",
			slog.String("current", m.currentVersion), slog.String("candidate", release.Version))
		return nil
	}

	m.logger.Info("ota: eligible update found, downloading",
		slog.String("release_id", release.ReleaseID), slog.String("version", release.Version))

	if err := m.downloader.FetchAndStage(ctx, release); err != nil {
		return fmt.Errorf("ota: download/stage release %s: %w", release.ReleaseID, err)
	}
	m.lastReleaseID.Store(release.ReleaseID)
	m.logger.Info("ota: release staged for apply", slog.String("release_id", release.ReleaseID))
	return nil
}
