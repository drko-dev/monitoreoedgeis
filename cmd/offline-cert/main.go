// Command offline-cert validates a runtime pack's manifest, target tuple and
// declared artifact hashes. It never provisions software or contacts a network.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/drko-dev/monitoreoedgeis/internal/installer"
	"github.com/drko-dev/monitoreoedgeis/internal/offlinecert"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("offline-cert", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	packDir := flags.String("pack-dir", "", "runtime pack directory")
	selfTest := flags.Bool("self-test", false, "validate a tiny synthetic pack and emit a scoped readiness report")
	manifestPath := flags.String("manifest", "", "manifest path (defaults to <pack-dir>/runtime-manifest.json)")
	targetOS := flags.String("target-os", runtime.GOOS, "target operating system")
	targetArch := flags.String("target-arch", runtime.GOARCH, "target architecture")
	baseSHA := flags.String("base-sha", "", "source commit SHA for report provenance")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *selfTest {
		return runSelfTest(*targetOS, *targetArch, *baseSHA)
	}
	if *packDir == "" {
		fmt.Fprintln(os.Stderr, "-pack-dir is required")
		return 2
	}
	if *manifestPath == "" {
		*manifestPath = filepath.Join(*packDir, offlinecert.ManifestFilename)
	}

	report := offlinecert.NewReport("runtime-pack-artifact-integrity-only")
	report.BaseSHA = *baseSHA
	manifest, err := installer.LoadRuntimeManifest(*manifestPath)
	if err != nil {
		_ = report.Add(offlinecert.Check{ID: "manifest-schema", Category: "manifest", Status: offlinecert.StatusFailed, Evidence: err.Error()})
		_ = report.Add(offlinecert.Check{ID: "target-compatibility", Category: "compatibility", Status: offlinecert.StatusSkipped, Blocker: "manifest did not pass schema validation"})
		_ = report.Add(offlinecert.Check{ID: "artifact-integrity", Category: "artifacts", Status: offlinecert.StatusSkipped, Blocker: "manifest did not pass schema validation"})
		addFutureChecks(report)
		_ = report.WriteJSON(os.Stdout)
		return 1
	}
	_ = report.Add(offlinecert.Check{ID: "manifest-schema", Category: "manifest", Status: offlinecert.StatusPassed, Evidence: "existing RuntimeManifest parser and schema validator"})
	if err := manifest.ValidateCompatibility(*targetOS, *targetArch); err != nil {
		_ = report.Add(offlinecert.Check{ID: "target-compatibility", Category: "compatibility", Status: offlinecert.StatusFailed, Evidence: err.Error()})
		_ = report.Add(offlinecert.Check{ID: "artifact-integrity", Category: "artifacts", Status: offlinecert.StatusSkipped, Blocker: "target compatibility failed"})
		addFutureChecks(report)
		_ = report.WriteJSON(os.Stdout)
		return 1
	}
	_ = report.Add(offlinecert.Check{ID: "target-compatibility", Category: "compatibility", Status: offlinecert.StatusPassed, Evidence: *targetOS + "/" + *targetArch})
	artifacts, err := manifest.ValidateArtifacts(*packDir)
	if err != nil {
		_ = report.Add(offlinecert.Check{ID: "artifact-integrity", Category: "artifacts", Status: offlinecert.StatusFailed, Evidence: err.Error()})
		addFutureChecks(report)
		_ = report.WriteJSON(os.Stdout)
		return 1
	}
	if artifacts.ValidArtifacts != artifacts.TotalArtifacts {
		_ = report.Add(offlinecert.Check{ID: "artifact-integrity", Category: "artifacts", Status: offlinecert.StatusFailed, Evidence: fmt.Sprintf("%d/%d declared artifacts validated", artifacts.ValidArtifacts, artifacts.TotalArtifacts)})
		addFutureChecks(report)
		_ = report.WriteJSON(os.Stdout)
		return 1
	}
	_ = report.Add(offlinecert.Check{ID: "artifact-integrity", Category: "artifacts", Status: offlinecert.StatusPassed, Evidence: fmt.Sprintf("%d/%d declared artifacts validated", artifacts.ValidArtifacts, artifacts.TotalArtifacts)})
	addFutureChecks(report)
	_ = report.WriteJSON(os.Stdout)
	return 0
}

func runSelfTest(targetOS, targetArch, baseSHA string) int {
	pack, err := offlinecert.NewSyntheticPack(os.TempDir(), targetOS, targetArch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create synthetic pack: %v\n", err)
		return 1
	}
	defer func() { _ = pack.Close() }()
	report := offlinecert.NewReport("synthetic-fixture-and-readiness-only")
	report.BaseSHA = baseSHA
	artifacts, err := pack.Validate(targetOS, targetArch)
	if err != nil {
		_ = report.Add(offlinecert.Check{ID: "synthetic-pack", Category: "harness", Status: offlinecert.StatusFailed, Evidence: err.Error()})
	} else {
		_ = report.Add(offlinecert.Check{ID: "synthetic-pack", Category: "harness", Status: offlinecert.StatusPassed, Evidence: fmt.Sprintf("%d/%d tiny synthetic artifacts validated", artifacts.ValidArtifacts, artifacts.TotalArtifacts)})
	}
	addFutureChecks(report)
	_ = report.WriteJSON(os.Stdout)
	if err != nil {
		return 1
	}
	return 0
}

func addFutureChecks(report *offlinecert.Report) {
	for _, check := range []offlinecert.Check{
		{ID: "offline-provisioning", Category: "offline", Status: offlinecert.StatusPending, Blocker: "integrated H3-H6 provisioner entry point is not present on the H7A base"},
		{ID: "interruption-and-rollback", Category: "recovery", Status: offlinecert.StatusPending, Blocker: "H6 activation/rollback entry point is not present on the H7A base"},
		{ID: "physical-runtime-certification", Category: "physical", Status: offlinecert.StatusPending, Blocker: "requires integrated Full Edge runtime and separately authorized physical certification"},
	} {
		_ = report.Add(check)
	}
}
