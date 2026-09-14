// Command geocam-edge is the GEO CAM Edge agent.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/agent"
	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
)

func main() {
	// Subcommands are dispatched on os.Args[1] before flag.Parse() so that
	// `--version`/`-version` keep working exactly as before for the default
	// (no-subcommand) invocation.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "identity":
			runIdentityCmd(os.Args[2:])
			return
		case "check":
			runCheckCmd(os.Args[2:])
			return
		}
	}

	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("geocam-edge %s (commit %s, built %s)\n",
			agent.Version, agent.Commit, agent.BuildDate)
		return
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge: configuration error: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := agent.New(cfg).Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge: %v\n", err)
		os.Exit(1)
	}
}

// runIdentityCmd prints edge_id, version, architecture, processing_mode and
// data dir — no secrets. It resolves (and, on first run, persists) identity
// exactly like the running agent would.
func runIdentityCmd(args []string) {
	fs := flag.NewFlagSet("identity", flag.ExitOnError)
	_ = fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge identity: configuration error: %v\n", err)
		os.Exit(1)
	}

	ident, err := identity.Load(cfg.DataDir, cfg.EdgeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge identity: %v\n", err)
		os.Exit(1)
	}

	host := platform.Detect()
	fmt.Print(identityReport(ident, cfg, host))
}

// identityReport formats the output of `geocam-edge identity`. Kept pure
// (no I/O) so it is directly testable.
func identityReport(ident identity.Identity, cfg *config.Config, host platform.Info) string {
	return fmt.Sprintf(
		"edge_id:          %s\n"+
			"identity_source:  %s\n"+
			"version:          %s\n"+
			"architecture:     %s\n"+
			"processing_mode:  %s\n"+
			"data_dir:         %s\n",
		ident.EdgeID, ident.Source, agent.Version, host.GOARCH, cfg.ProcessingMode, cfg.DataDir,
	)
}

// runCheckCmd inspects a (presumably already running) agent's health status
// over its local HTTP surface, without starting a full agent itself. Exits
// non-zero when the agent is unreachable or not READY — usable directly as
// a systemd/K8s exec health check.
func runCheckCmd(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	_ = fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge check: configuration error: %v\n", err)
		os.Exit(1)
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/status", cfg.HealthAddr))
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge check: agent unreachable at %s: %v\n", cfg.HealthAddr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var snap health.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge check: invalid response from agent: %v\n", err)
		os.Exit(1)
	}

	report, ready := checkReport(snap)
	fmt.Print(report)
	if !ready {
		os.Exit(1)
	}
}

// checkReport formats the output of `geocam-edge check` and reports whether
// the agent is READY. Kept pure (no I/O) so it is directly testable.
func checkReport(snap health.Snapshot) (report string, ready bool) {
	report = fmt.Sprintf(
		"status:           %s\n"+
			"edge_id:          %s\n"+
			"version:          %s\n"+
			"processing_mode:  %s\n"+
			"uptime:           %s\n",
		snap.Status, snap.EdgeID, snap.Version, snap.ProcessingMode, snap.Uptime,
	)
	return report, snap.Status == health.StateReady
}
