// Command geocam-edge is the GEO CAM Edge agent.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/drko-dev/monitoreoedgeis/internal/agent"
	"github.com/drko-dev/monitoreoedgeis/internal/config"
)

func main() {
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
