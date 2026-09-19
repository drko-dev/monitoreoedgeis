// Command geocam-edge is the GEO CAM Edge agent.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/agent"
	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery"
	"github.com/drko-dev/monitoreoedgeis/internal/factoryreset"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/ota"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
)

func main() {
	if len(os.Args) < 2 {
		runAgentCmd(nil)
		return
	}

	cmd, args := os.Args[1], os.Args[2:]

	switch cmd {
	case "-h", "--help", "help":
		printRootUsage(os.Stdout)
		return
	case "-version", "--version":
		printVersion()
		return
	case "run":
		runAgentCmd(args)
	case "version":
		runVersionCmd(args)
	case "identity":
		runIdentityCmd(args)
	case "config":
		runConfigCmd(args)
	case "check":
		runCheckCmd(args)
	case "enroll":
		runEnrollCmd(args)
	case "factory-reset":
		runFactoryResetCmd(args)
	case "credential":
		runCredentialCmd(args)
	case "discovery":
		runDiscoveryCmd(args)
	case "saas":
		runSaasCmd(args)
	case "ota":
		runOTACmd(args)
	default:
		fmt.Fprintf(os.Stderr, "geocam-edge: unknown command %q\n", cmd)
		fmt.Fprintln(os.Stderr, "Run 'geocam-edge --help' for usage.")
		os.Exit(1)
	}
}

// isHelpRequest reports whether args opens with a help request, so every
// subcommand can print its own detailed usage instead of relying on the
// flag package's bare, flag-only default usage.
func isHelpRequest(args []string) bool {
	return len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help")
}

// runAgentCmd starts the actual agent daemon. It is the single
// implementation shared by both `geocam-edge run` and plain `geocam-edge`
// (no subcommand) — there is exactly one code path that starts the daemon.
func runAgentCmd(args []string) {
	if isHelpRequest(args) {
		printRunUsage(os.Stdout)
		return
	}
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	_ = fs.Parse(args)

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

// runVersionCmd implements `geocam-edge version`. --version (the flag) keeps
// working too and prints the same thing.
func runVersionCmd(args []string) {
	if isHelpRequest(args) {
		printVersionUsage(os.Stdout)
		return
	}
	fs := flag.NewFlagSet("version", flag.ExitOnError)
	_ = fs.Parse(args)
	printVersion()
}

func printVersion() {
	host := platform.Detect()
	fmt.Print(versionReport(host))
}

// versionReport formats build/version metadata. No secrets. Kept pure so it
// is directly testable.
func versionReport(host platform.Info) string {
	return fmt.Sprintf(
		"geocam-edge %s (commit %s, built %s)\n"+
			"platform:     %s\n"+
			"architecture: %s\n",
		agent.Version, agent.Commit, agent.BuildDate, host.OS, host.GOARCH,
	)
}

// runIdentityCmd prints edge_id, version, architecture, processing_mode and
// data dir — no secrets. It resolves (and, on first run, persists) identity
// exactly like the running agent would.
func runIdentityCmd(args []string) {
	if isHelpRequest(args) {
		printIdentityUsage(os.Stdout)
		return
	}
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
	if isHelpRequest(args) {
		printCheckUsage(os.Stdout)
		return
	}
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

// runEnrollCmd claims a one-time enrollment token against the SaaS and
// persists the resulting credential. It always reuses the existing
// (persisted) edge_id — it never generates a new one.
func runEnrollCmd(args []string) {
	if isHelpRequest(args) {
		printEnrollUsage(os.Stdout)
		return
	}
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	tokenFlag := fs.String("token", "",
		"enrollment token (dev only — exposes the token in shell history and process listings; "+
			"prefer piping it via stdin or setting GEOCAM_ENROLLMENT_TOKEN)")
	_ = fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge enroll: configuration error: %v\n", err)
		os.Exit(1)
	}

	ident, err := identity.Load(cfg.DataDir, cfg.EdgeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge enroll: identity error: %v\n", err)
		os.Exit(1)
	}

	existing, err := credentials.Load(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge enroll: %v\n", err)
		os.Exit(1)
	}
	if existing.IsEnrolled() {
		fmt.Fprintln(os.Stderr, "geocam-edge enroll: already enrolled, use re-enrollment flow")
		os.Exit(1)
	}

	token, err := resolveEnrollmentToken(*tokenFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge enroll: %v\n", err)
		os.Exit(1)
	}

	client, err := transport.New(cfg.SaaSURL, cfg.AllowInsecureHTTP, cfg.SaaSTimeout, agent.Version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge enroll: %v\n", err)
		os.Exit(1)
	}

	host := platform.Detect()
	ctx, cancel := context.WithTimeout(context.Background(), cfg.SaaSTimeout+time.Second)
	defer cancel()

	creds, warning, err := runEnroll(ctx, client, ident, host, token)
	if err != nil {
		// Claim never happened server-side (or the credential could not be
		// generated in the first place): nothing to persist.
		// S11 security event log: edge_id is a non-secret identifier; the
		// enrollment token/credential itself is never logged here or anywhere.
		slog.Default().Error("enrollment failed", "edge_id", ident.EdgeID, "error", saasErrorMessage(err))
		fmt.Fprintf(os.Stderr, "geocam-edge enroll: %s\n", saasErrorMessage(err))
		os.Exit(1)
	}
	if warning != "" {
		fmt.Fprintf(os.Stderr, "geocam-edge enroll: warning: %s\n", warning)
	}

	if err := credentials.Save(cfg.DataDir, creds); err != nil {
		slog.Default().Error("enrollment succeeded but persisting credentials failed", "edge_id", creds.EdgeID, "device_id", creds.DeviceID, "error", err)
		fmt.Fprintf(os.Stderr, "geocam-edge enroll: enrollment succeeded but persisting credentials failed: %v\n", err)
		os.Exit(1)
	}

	slog.Default().Info("enrollment succeeded", "edge_id", creds.EdgeID, "device_id", creds.DeviceID, "tenant_id", creds.TenantID, "site_id", creds.SiteID)
	fmt.Print(enrollSummary(creds.EdgeID, creds.DeviceID, creds.TenantID, creds.SiteID))
}

// runEnroll generates the device credential locally, claims token against
// the SaaS (sending only the credential's hash), and best-effort fetches
// organization/site via /edge/me. It performs no I/O beyond those SaaS
// calls — the caller owns persisting, printing, and process exit — which is
// what makes it directly testable against an httptest mock SaaS.
//
// A non-nil error means the claim itself never happened server-side: the
// generated credential is simply discarded, nothing to persist. Once the
// claim succeeds, this always returns a Credentials worth persisting (err
// nil) — if /edge/me fails, warning is non-empty and TenantID/SiteID are
// left empty rather than losing the now-valid credential: the enrollment
// already happened server-side, so discarding cred here would leave the
// device claimed remotely but unable to authenticate at all.
func runEnroll(ctx context.Context, client *transport.Client, ident identity.Identity, host platform.Info, token string) (creds credentials.Credentials, warning string, err error) {
	// Zero-knowledge model: the credential is generated and kept entirely on
	// this device. Only its SHA-256 hash ever reaches the SaaS.
	cred, err := credentials.GenerateCredential()
	if err != nil {
		return credentials.Credentials{}, "", err
	}

	// No "hostname" field exists in the real enroll contract
	// (agent_version/platform/architecture only) — omitted rather than
	// stuffed into an unrelated field.
	resp, err := client.Enroll(ctx, transport.EnrollRequest{
		EnrollmentToken:   token,
		GatewayInstanceID: ident.EdgeID,
		DeviceKeyHash:     credentials.HashCredential(cred),
		AgentVersion:      agent.Version,
		Platform:          host.OS,
		Architecture:      host.GOARCH,
		EdgeID:            ident.EdgeID,
	})
	if err != nil {
		return credentials.Credentials{}, "", err
	}

	creds = credentials.Credentials{
		EdgeID:            ident.EdgeID,
		DeviceID:          resp.DeviceID,
		Credential:        cred,
		CredentialVersion: 1,
		EnrolledAt:        time.Now().UTC(),
	}
	meResp, meErr := client.Me(ctx, resp.DeviceID, cred)
	if meErr != nil {
		return creds, fmt.Sprintf(
			"enrolled, but could not confirm organization/site (%s); run `geocam-edge check` or retry verification later",
			saasErrorMessage(meErr)), nil
	}
	creds.TenantID = meResp.OrganizationID.String()
	creds.SiteID = meResp.SiteID.String()
	return creds, "", nil
}

// resolveEnrollmentToken reads the enrollment token from stdin (priority,
// when piped), falling back to GEOCAM_ENROLLMENT_TOKEN, falling back to the
// --token flag (dev convenience only).
func resolveEnrollmentToken(tokenFlag string) (string, error) {
	tok, err := readStdinToken()
	if err != nil {
		return "", err
	}
	if tok != "" {
		return tok, nil
	}
	if tok := strings.TrimSpace(os.Getenv("GEOCAM_ENROLLMENT_TOKEN")); tok != "" {
		return tok, nil
	}
	if tok := strings.TrimSpace(tokenFlag); tok != "" {
		fmt.Fprintln(os.Stderr,
			"geocam-edge enroll: warning: --token exposes the enrollment token in shell history "+
				"and process listings; prefer piping it via stdin or GEOCAM_ENROLLMENT_TOKEN")
		return tok, nil
	}
	return "", fmt.Errorf("enrollment token required: pipe it via stdin, set GEOCAM_ENROLLMENT_TOKEN, or pass --token (dev only, insecure)")
}

// readStdinToken reads a piped token from stdin. It returns "" (not an
// error) when stdin is an interactive terminal, i.e. nothing was piped.
func readStdinToken() (string, error) {
	stat, err := os.Stdin.Stat()
	if err != nil || stat.Mode()&os.ModeCharDevice != 0 {
		return "", nil
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
	if err != nil {
		return "", fmt.Errorf("reading enrollment token from stdin: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// saasErrorMessage renders a transport error as a clear, secret-free
// message. It never includes the enrollment token or credential — the
// transport package guarantees its errors never carry them either.
func saasErrorMessage(err error) string {
	switch {
	case errors.Is(err, transport.ErrTokenInvalid):
		// The SaaS deliberately makes invalid/expired/used/mismatched
		// tokens indistinguishable (anti-enumeration): all 401s land here.
		return "enrollment token invalid, expired, or already used"
	case errors.Is(err, transport.ErrAlreadyEnrolled):
		return "this edge_id is already enrolled with the SaaS"
	case errors.Is(err, transport.ErrInvalidRequest):
		return "request rejected as invalid by SaaS"
	case errors.Is(err, transport.ErrUnauthorized):
		return "credential rejected by SaaS"
	case errors.Is(err, transport.ErrTimeout):
		return "SaaS request timed out"
	case errors.Is(err, transport.ErrSaaSUnavailable):
		return "SaaS is unreachable"
	default:
		return err.Error()
	}
}

func enrollSummary(edgeID, deviceID, organizationID, siteID string) string {
	return fmt.Sprintf(
		"Enrollment successful.\n"+
			"edge_id:          %s\n"+
			"device_id:        %s\n"+
			"organization_id:  %s\n"+
			"site_id:          %s\n",
		edgeID, deviceID, organizationID, siteID,
	)
}

// runCredentialCmd dispatches `geocam-edge credential <subcommand>`.
// runFactoryResetCmd removes only local device state. It never touches the
// installed binary, releases, systemd, or any path outside GEOCAM_DATA_DIR.
func runFactoryResetCmd(args []string) {
	if isHelpRequest(args) {
		printFactoryResetUsage(os.Stdout)
		return
	}
	fs := flag.NewFlagSet("factory-reset", flag.ExitOnError)
	confirmed := fs.Bool("confirm", false, "explicitly confirm removal of local device state")
	_ = fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge factory-reset: configuration error: %v\n", err)
		os.Exit(1)
	}
	if err := factoryreset.Reset(cfg.DataDir, *confirmed); err != nil {
		// S11 security event log: never logs DataDir contents, only the
		// outcome and the (non-secret) data directory path.
		slog.Default().Error("factory reset failed", "data_dir", cfg.DataDir, "error", err)
		fmt.Fprintf(os.Stderr, "geocam-edge factory-reset: %v\n", err)
		os.Exit(1)
	}
	slog.Default().Info("factory reset completed", "data_dir", cfg.DataDir)
	fmt.Printf("Factory reset complete. Local device state removed from %s.\n", cfg.DataDir)
}

func printFactoryResetUsage(w io.Writer) {
	fmt.Fprint(w, `Usage: geocam-edge factory-reset --confirm

Remove only local device state below GEOCAM_DATA_DIR and return this Edge to
an unenrolled state. Releases, installed software, and systemd are preserved.
The --confirm flag is required; this command never resets automatically.
`)
}

// runCredentialCmd dispatches `geocam-edge credential <subcommand>`.
func runCredentialCmd(args []string) {
	if isHelpRequest(args) {
		printCredentialUsage(os.Stdout)
		return
	}
	if len(args) == 0 || args[0] != "rotate" {
		fmt.Fprintln(os.Stderr, "geocam-edge credential: usage: geocam-edge credential rotate")
		os.Exit(1)
	}
	runCredentialRotateCmd(args[1:])
}

// runCredentialRotateCmd rotates the stored credential: it persists the new
// credential ATOMICALLY (temp file, then rename) before ever touching the
// old one on disk, so a mid-write failure never corrupts or loses the
// previous, still-valid credential.
func runCredentialRotateCmd(args []string) {
	if isHelpRequest(args) {
		printCredentialRotateUsage(os.Stdout)
		return
	}
	fs := flag.NewFlagSet("credential rotate", flag.ExitOnError)
	_ = fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge credential rotate: configuration error: %v\n", err)
		os.Exit(1)
	}

	creds, err := credentials.Load(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge credential rotate: %v\n", err)
		os.Exit(1)
	}
	if !creds.IsEnrolled() {
		fmt.Fprintln(os.Stderr, "geocam-edge credential rotate: not enrolled, run `geocam-edge enroll` first")
		os.Exit(1)
	}

	client, err := transport.New(cfg.SaaSURL, cfg.AllowInsecureHTTP, cfg.SaaSTimeout, agent.Version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge credential rotate: %v\n", err)
		os.Exit(1)
	}

	// New credential B is generated locally, same as enroll — the SaaS only
	// ever sees its hash. Generated once, before any network call, and
	// reused verbatim across every retry: a retry must submit the exact
	// same device_key_hash the first attempt did.
	newCred, err := credentials.GenerateCredential()
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge credential rotate: %v\n", err)
		os.Exit(1)
	}
	rotationID, err := identity.NewUUIDv4()
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge credential rotate: %v\n", err)
		os.Exit(1)
	}

	// No single per-attempt deadline here: rotateWithRetry spans up to
	// rotateMaxAttempts requests with backoff sleeps between them, each
	// individual request already bounded by the http.Client's own
	// cfg.SaaSTimeout.
	ctx := context.Background()

	resp, err := rotateWithRetry(ctx, client, creds.DeviceID, creds.Credential,
		transport.RotateKeyRequest{DeviceKeyHash: credentials.HashCredential(newCred), RotationID: rotationID},
		rotateBackoffs)
	if err != nil {
		// A queda intacta en disco: nunca se llamó a Save.
		// S11 security event log: edge_id/rotation_id are non-secret
		// identifiers; neither credential (old or new) is ever logged.
		slog.Default().Error("credential rotation failed", "edge_id", creds.EdgeID, "rotation_id", rotationID, "error", saasErrorMessage(err))
		if errors.Is(err, transport.ErrUnauthorized) {
			fmt.Fprintln(os.Stderr, "geocam-edge credential rotate: credential rejected by SaaS (revoked) — re-enrollment required")
		} else {
			fmt.Fprintf(os.Stderr, "geocam-edge credential rotate: %s\n", saasErrorMessage(err))
		}
		os.Exit(1)
	}

	newVersion := creds.CredentialVersion + 1

	// ponytail: if Save fails here, the SaaS has already ACKed the rotation
	// server-side but persisting B locally failed. The OLD credential A on
	// disk is safe (Save only replaces it via atomic rename after a fully
	// successful write), so the device is not bricked, but A no longer
	// authenticates against the SaaS — rotation must be retried. Upgrade
	// path: a local write-ahead log of pending rotations, if this proves to
	// matter in practice.
	if err := credentials.Save(cfg.DataDir, credentials.Credentials{
		EdgeID:            creds.EdgeID,
		DeviceID:          creds.DeviceID,
		Credential:        newCred,
		CredentialVersion: newVersion,
		TenantID:          creds.TenantID,
		SiteID:            creds.SiteID,
		EnrolledAt:        creds.EnrolledAt,
	}); err != nil {
		fmt.Fprintf(os.Stderr,
			"geocam-edge credential rotate: SaaS acknowledged the rotation but persisting the new credential locally failed: %v\n"+
				"retry `geocam-edge credential rotate`\n", err)
		os.Exit(1)
	}

	if _, err := client.Me(ctx, resp.DeviceID, newCred); err != nil {
		slog.Default().Error("credential rotated and persisted but post-rotation verification failed", "edge_id", creds.EdgeID, "rotation_id", rotationID, "error", err)
		fmt.Fprintf(os.Stderr, "geocam-edge credential rotate: rotated and persisted, but verification against %s failed: %v\n",
			transport.MePath, err)
		os.Exit(1)
	}

	slog.Default().Info("credential rotation succeeded", "edge_id", creds.EdgeID, "rotation_id", rotationID, "new_credential_version", newVersion)
	fmt.Print(rotateSummary(creds.EdgeID, newVersion))
}

// rotateMaxAttempts bounds credential-rotation retries: the same rotation_id
// and device_key_hash are resubmitted on every attempt, which is what makes
// this safe to retry (the SaaS treats rotation_id as an idempotency key).
const rotateMaxAttempts = 3

// rotateBackoffs holds the sleep between attempt N and N+1 — len must be
// rotateMaxAttempts-1.
var rotateBackoffs = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// rotateWithRetry calls client.RotateKey up to rotateMaxAttempts times with
// the identical req on every attempt. It stops immediately (no further
// retry) on ErrUnauthorized: a revoked current credential will not start
// working on a later attempt, and A must stay untouched on disk in that
// case.
func rotateWithRetry(ctx context.Context, client *transport.Client, deviceID, currentCredential string, req transport.RotateKeyRequest, backoffs []time.Duration) (transport.RotateResponse, error) {
	var lastErr error
	for attempt := 0; attempt < rotateMaxAttempts; attempt++ {
		resp, err := client.RotateKey(ctx, deviceID, currentCredential, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if errors.Is(err, transport.ErrUnauthorized) {
			return transport.RotateResponse{}, err
		}
		if attempt < len(backoffs) {
			time.Sleep(backoffs[attempt])
		}
	}
	return transport.RotateResponse{}, lastErr
}

func rotateSummary(edgeID string, version int) string {
	return fmt.Sprintf(
		"Credential rotated successfully.\n"+
			"edge_id:            %s\n"+
			"credential_version: %d\n",
		edgeID, version,
	)
}

func runDiscoveryCmd(args []string) {
	if isHelpRequest(args) {
		printDiscoveryUsage(os.Stdout)
		return
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "geocam-edge discovery: missing subcommand (usage: geocam-edge discovery scan [--interface <iface>] [--timeout <duration>] [--json])")
		os.Exit(1)
	}

	switch args[0] {
	case "scan":
		runDiscoveryScanCmd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "geocam-edge discovery: unknown subcommand %q (usage: geocam-edge discovery scan)\n", args[0])
		os.Exit(1)
	}
}

func runDiscoveryScanCmd(args []string) {
	if isHelpRequest(args) {
		printDiscoveryScanUsage(os.Stdout)
		return
	}
	fs := flag.NewFlagSet("discovery scan", flag.ExitOnError)
	ifaceFlag := fs.String("interface", "", "comma-separated network interfaces to scan (default: auto-private)")
	timeoutFlag := fs.Duration("timeout", 4*time.Second, "scan duration per interface (default: 4s)")
	jsonFlag := fs.Bool("json", false, "output results in JSON format")
	_ = fs.Parse(args)

	var ifaces []string
	if *ifaceFlag != "" {
		for _, part := range strings.Split(*ifaceFlag, ",") {
			if s := strings.TrimSpace(part); s != "" {
				ifaces = append(ifaces, s)
			}
		}
	}

	cfg, err := config.Load()
	if err != nil {
		cfg = &config.Config{
			LogLevel: "info",
		}
	}
	if len(ifaces) == 0 && len(cfg.DiscoveryInterfaces) > 0 {
		ifaces = cfg.DiscoveryInterfaces
	}

	engine := discovery.NewEngine(nil, nil, nil, ifaces, *timeoutFlag, nil)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	result, err := engine.RunScan(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge discovery scan: error: %v\n", err)
		os.Exit(1)
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result.DevicesFound); err != nil {
			fmt.Fprintf(os.Stderr, "geocam-edge discovery scan: encoding error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	fmt.Print(discoveryScanReport(result))
}

func discoveryScanReport(result *discovery.ScanResult) string {
	if result == nil || len(result.DevicesFound) == 0 {
		dur := ""
		if result != nil {
			dur = fmt.Sprintf(" (scanned in %s)", result.Duration.Round(time.Millisecond))
		}
		return fmt.Sprintf("No network video devices discovered%s.\n", dur)
	}

	var sb strings.Builder
	sb.WriteString("================================================================================\n")
	sb.WriteString(fmt.Sprintf("DISCOVERY SCAN RESULTS: %d device(s) found in %s\n",
		len(result.DevicesFound), result.Duration.Round(time.Millisecond)))
	sb.WriteString("================================================================================\n")

	for i, dev := range result.DevicesFound {
		sb.WriteString(fmt.Sprintf("[%d] %s:%d%s\n", i+1, dev.IP, dev.Port, dev.Path))
		if dev.EPRAddress != "" {
			sb.WriteString(fmt.Sprintf("    EPR:           %s\n", dev.EPRAddress))
		}
		if dev.DeviceType != "" && dev.DeviceType != discovery.DeviceTypeUnknown {
			sb.WriteString(fmt.Sprintf("    Type:          %s\n", dev.DeviceType))
		}
		if dev.Manufacturer != "" {
			sb.WriteString(fmt.Sprintf("    Manufacturer:  %s\n", dev.Manufacturer))
		}
		if dev.Model != "" {
			sb.WriteString(fmt.Sprintf("    Model:         %s\n", dev.Model))
		}
		if dev.Serial != "" {
			sb.WriteString(fmt.Sprintf("    Serial:        %s\n", dev.Serial))
		}
		if dev.Firmware != "" {
			sb.WriteString(fmt.Sprintf("    Firmware:      %s\n", dev.Firmware))
		}
		sb.WriteString(fmt.Sprintf("    Auth Required: %t\n", dev.AuthRequired))
		if len(dev.VideoSources) > 0 {
			sb.WriteString(fmt.Sprintf("    Channels:      %d\n", len(dev.VideoSources)))
		}
		if len(dev.Scopes) > 0 {
			sb.WriteString(fmt.Sprintf("    Scopes:        %s\n", strings.Join(dev.Scopes, " ")))
		}
		sb.WriteString("--------------------------------------------------------------------------------\n")
	}

	return sb.String()
}

// runConfigCmd prints the effective, NON-secret configuration: everything in
// internal/config.Config plus a yes/no enrollment-token/enrolled summary.
// Never prints GEOCAM_ENROLLMENT_TOKEN's value or the stored credential.
func runConfigCmd(args []string) {
	if isHelpRequest(args) {
		printConfigUsage(os.Stdout)
		return
	}
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	_ = fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge config: configuration error: %v\n", err)
		os.Exit(1)
	}

	creds, err := credentials.Load(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge config: %v\n", err)
		os.Exit(1)
	}

	host := platform.Detect()
	tokenConfigured := strings.TrimSpace(os.Getenv("GEOCAM_ENROLLMENT_TOKEN")) != ""
	fmt.Print(configReport(cfg, host, creds, tokenConfigured))
}

// configReport formats `geocam-edge config` output. Kept pure (no I/O) so it
// is directly testable. It never includes GEOCAM_ENROLLMENT_TOKEN's value or
// creds.Credential — only booleans derived from them.
func configReport(cfg *config.Config, host platform.Info, creds credentials.Credentials, enrollmentTokenConfigured bool) string {
	interfaces := "auto"
	if len(cfg.DiscoveryInterfaces) > 0 {
		interfaces = strings.Join(cfg.DiscoveryInterfaces, ",")
	}
	tokenState := "not configured"
	if enrollmentTokenConfigured {
		tokenState = "configured"
	}
	enrolledState := "no"
	if creds.IsEnrolled() {
		enrolledState = "yes"
	}

	return fmt.Sprintf(
		"version:              %s\n"+
			"architecture:         %s\n"+
			"saas_url:             %s\n"+
			"processing_mode:      %s\n"+
			"data_dir:             %s\n"+
			"health_addr:          %s\n"+
			"heartbeat_interval:   %s\n"+
			"discovery_enabled:    %t\n"+
			"discovery_interval:   %s\n"+
			"discovery_timeout:    %s\n"+
			"discovery_interfaces: %s\n"+
			"connectivity_enabled: %t\n"+
			"stream_role:          %s\n"+
			"stream_timeout:       %s\n"+
			"enrollment_token:     %s\n"+
			"enrolled:             %s\n",
		agent.Version, host.GOARCH, cfg.SaaSURL, cfg.ProcessingMode, cfg.DataDir, cfg.HealthAddr,
		cfg.HeartbeatInterval, cfg.DiscoveryEnabled, cfg.DiscoveryInterval, cfg.DiscoveryTimeout,
		interfaces, cfg.ConnectivityEnabled, cfg.StreamRole, cfg.StreamTimeout, tokenState, enrolledState,
	)
}

// runSaasCmd dispatches `geocam-edge saas <subcommand>`.
func runSaasCmd(args []string) {
	if isHelpRequest(args) {
		printSaasUsage(os.Stdout)
		return
	}
	if len(args) == 0 || args[0] != "check" {
		fmt.Fprintln(os.Stderr, "geocam-edge saas: usage: geocam-edge saas check")
		os.Exit(1)
	}
	runSaasCheckCmd(args[1:])
}

// runSaasCheckCmd verifies real SaaS connectivity and authentication by
// reusing the existing /edge/me endpoint via transport.Client.Me — no new
// SaaS endpoint or contract change.
func runSaasCheckCmd(args []string) {
	if isHelpRequest(args) {
		printSaasCheckUsage(os.Stdout)
		return
	}
	fs := flag.NewFlagSet("saas check", flag.ExitOnError)
	_ = fs.Parse(args)

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge saas check: configuration error: %v\n", err)
		os.Exit(1)
	}
	if cfg.SaaSURL == "" {
		fmt.Fprintln(os.Stderr, "SaaS connectivity: FAIL")
		fmt.Fprintln(os.Stderr, "reason:            GEOCAM_SAAS_URL is not configured")
		os.Exit(1)
	}

	creds, err := credentials.Load(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge saas check: %v\n", err)
		os.Exit(1)
	}
	if !creds.IsEnrolled() {
		fmt.Printf(
			"SaaS connectivity: NOT ENROLLED\n"+
				"saas_url:          %s\n"+
				"edge_id:           %s\n",
			cfg.SaaSURL, cfg.EdgeID,
		)
		os.Exit(1)
	}

	client, err := transport.New(cfg.SaaSURL, cfg.AllowInsecureHTTP, cfg.SaaSTimeout, agent.Version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge saas check: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.SaaSTimeout+time.Second)
	defer cancel()

	meResp, meErr := client.Me(ctx, creds.DeviceID, creds.Credential)
	report, ok := saasCheckReport(cfg.SaaSURL, creds.EdgeID, meResp, meErr)
	if !ok {
		fmt.Fprint(os.Stderr, report)
		os.Exit(1)
	}
	fmt.Print(report)
}

// saasCheckReport formats `geocam-edge saas check` output against an already
// resolved /edge/me result. Kept pure (no I/O) so it is directly testable.
// Never includes creds.Credential. err is classified via the transport
// sentinel errors (errors.Is) so the failure reason is always clear.
func saasCheckReport(saasURL, edgeID string, me transport.MeResponse, err error) (report string, ok bool) {
	if err == nil {
		return fmt.Sprintf(
			"SaaS connectivity: OK\n"+
				"authentication:    OK\n"+
				"saas_url:          %s\n"+
				"edge_id:           %s\n"+
				"device_id:         %s\n"+
				"organization_id:   %s\n"+
				"site_id:           %s\n",
			saasURL, edgeID, me.DeviceID, me.OrganizationID.String(), me.SiteID.String(),
		), true
	}

	return fmt.Sprintf(
		"SaaS connectivity: FAIL\n"+
			"reason:            %s\n"+
			"saas_url:          %s\n"+
			"edge_id:           %s\n",
		saasErrorMessage(err), saasURL, edgeID,
	), false
}

// runOTACmd dispatches `geocam-edge ota <subcommand>`.
func runOTACmd(args []string) {
	if len(args) == 0 || isHelpRequest(args) {
		printOTAUsage(os.Stdout)
		if len(args) == 0 {
			os.Exit(1)
		}
		return
	}
	switch args[0] {
	case "verify":
		runOTAVerifyCmd(args[1:])
	case "sign":
		runOTASignCmd(args[1:])
	default:
		fmt.Fprintln(os.Stderr, "geocam-edge ota: usage: geocam-edge ota <verify|sign>")
		os.Exit(1)
	}
}

// runOTAVerifyCmd implements `geocam-edge ota verify` (Hito T4): the same
// signature/checksum/layout verification the appliance runs internally
// before staging apply.request, reused here so a human or IA2's privileged
// updater can independently re-run it against an already-downloaded set of
// files.
func runOTAVerifyCmd(args []string) {
	if isHelpRequest(args) {
		printOTAVerifyUsage(os.Stdout)
		return
	}
	fs := flag.NewFlagSet("ota verify", flag.ExitOnError)
	artifactDir := fs.String("artifact-dir", "", "path to a staged release dir (metadata.json, SHA256SUMS, SHA256SUMS.sig, artifact) -- preferred; the exact contract IA2's privileged updater reuses")
	artifact := fs.String("artifact", "", "flag mode: path to the artifact tar.gz (used only when -artifact-dir is not given)")
	sums := fs.String("sha256sums", "", "flag mode: path to the SHA256SUMS manifest")
	sig := fs.String("signature", "", "flag mode: path to SHA256SUMS.sig")
	pubKeyFile := fs.String("public-key", "", "path to the Ed25519 public key (defaults to GEOCAM_OTA_PUBLIC_KEY_FILE)")
	version := fs.String("version", "", "flag mode: expected release version (vX.Y.Z) -- required together with -arch to check the VERSION/ARCH binding")
	arch := fs.String("arch", "", "flag mode: expected architecture (amd64|arm64) -- required together with -version")
	currentVersion := fs.String("current-version", "", "version treated as 'current' for forward-eligibility (default: running agent version)")
	_ = fs.Parse(args)

	keyPath := *pubKeyFile
	if keyPath == "" {
		if cfg, err := config.Load(); err == nil {
			keyPath = cfg.OTAPublicKeyFile
		}
	}
	pubKey, err := ota.LoadPublicKey(keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge ota verify: %v\n", err)
		os.Exit(1)
	}
	curVer := *currentVersion
	if curVer == "" {
		curVer = agent.Version
	}

	if *artifactDir != "" {
		meta, err := ota.VerifyReleaseDir(*artifactDir, pubKey, curVer)
		if err != nil {
			// Deliberately no "artifact=" line on any failure path: a
			// caller (IA2's privileged updater) must never see that
			// token unless every check -- signature, checksum, forward
			// version, VERSION/ARCH binding -- has fully passed.
			fmt.Fprintf(os.Stderr, "geocam-edge ota verify: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("OK: signature valid, checksum matches, VERSION/ARCH bound to the release descriptor.")
		// Exactly one line with this "artifact=" prefix, printed only
		// after full success -- the parseable contract PR #60's
		// privileged updater consumes.
		fmt.Printf("artifact=%s\n", meta.ArtifactName)
		return
	}

	if *artifact == "" || *sums == "" || *sig == "" {
		fmt.Fprintln(os.Stderr, "geocam-edge ota verify: either -artifact-dir, or -artifact/-sha256sums/-signature, are required")
		os.Exit(1)
	}
	manifest, err := os.ReadFile(*sums)
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge ota verify: read sha256sums: %v\n", err)
		os.Exit(1)
	}
	sigBytes, err := os.ReadFile(*sig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge ota verify: read signature: %v\n", err)
		os.Exit(1)
	}
	artifactName := filepath.Base(*artifact)

	if *version != "" && *arch != "" {
		if err := ota.VerifyReleaseFiles(*artifact, manifest, sigBytes, pubKey, artifactName, curVer, *version, *arch); err != nil {
			fmt.Fprintf(os.Stderr, "geocam-edge ota verify: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("OK: signature valid, checksum matches, VERSION/ARCH bound to the release descriptor.")
		return
	}

	if err := ota.VerifyManifestSignature(manifest, sigBytes, pubKey); err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge ota verify: %v\n", err)
		os.Exit(1)
	}
	if err := ota.VerifyArtifactChecksum(*artifact, manifest, artifactName); err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge ota verify: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "geocam-edge ota verify: warning: -version and -arch were not both given -- VERSION/ARCH binding was NOT checked")
	fmt.Println("OK: signature valid, checksum matches.")
}

// runOTASignCmd implements `geocam-edge ota sign`, used only by the
// release pipeline (see .github/workflows/release.yml) to produce
// SHA256SUMS.sig from an externally provisioned private key. Never run on
// an appliance; never ships a private key anywhere near one.
func runOTASignCmd(args []string) {
	if isHelpRequest(args) {
		printOTASignUsage(os.Stdout)
		return
	}
	fs := flag.NewFlagSet("ota sign", flag.ExitOnError)
	sums := fs.String("sha256sums", "", "path to the SHA256SUMS manifest to sign")
	privKeyFile := fs.String("private-key", "", "path to the Ed25519 private key")
	out := fs.String("out", "", "output path for the detached signature (default: <sha256sums>.sig)")
	_ = fs.Parse(args)

	if *sums == "" || *privKeyFile == "" {
		fmt.Fprintln(os.Stderr, "geocam-edge ota sign: -sha256sums and -private-key are required")
		os.Exit(1)
	}
	priv, err := ota.LoadPrivateKey(*privKeyFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge ota sign: %v\n", err)
		os.Exit(1)
	}
	manifest, err := os.ReadFile(*sums)
	if err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge ota sign: read sha256sums: %v\n", err)
		os.Exit(1)
	}
	sig := ota.SignManifest(priv, manifest)
	dest := *out
	if dest == "" {
		dest = *sums + ".sig"
	}
	if err := os.WriteFile(dest, sig, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge ota sign: write signature: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("signed %s -> %s\n", *sums, dest)
}

// --- Usage text --------------------------------------------------------
//
// Every printXUsage function is a thin fmt.Fprint wrapper around a constant
// so tests can assert on content without spawning a process, and so
// `--help`/`-h`/`help` behave identically everywhere.

const rootUsage = `geocam-edge is the GEO CAM Edge agent CLI.

Usage:
  geocam-edge [command] [flags]

Commands:
  run                  Start the edge agent daemon (default when no command is given)
  version              Print version, commit, build date and platform
  identity             Print this Edge's identity (edge_id, source, version, data dir)
  config               Print effective, non-secret configuration
  check                Check a running agent's health over its local HTTP surface
  enroll               Enroll this Edge against the SaaS using a one-time token
  factory-reset        Remove local device state after explicit confirmation
  credential rotate    Rotate the locally stored SaaS credential
  discovery scan       Scan the LAN for ONVIF/RTSP camera devices
  saas check           Verify SaaS connectivity and authentication
  ota verify           Verify a downloaded OTA release's signature/checksum/layout
  ota sign             Sign a SHA256SUMS manifest (release pipeline only)

Flags:
  -h, --help           Show this help
  --version            Print version and exit (same as 'geocam-edge version')

Run 'geocam-edge <command> --help' for details on a specific command.

Typical flow:
  geocam-edge config
  geocam-edge identity
  geocam-edge enroll
  geocam-edge saas check
  geocam-edge run              # in one terminal
  geocam-edge check            # in another terminal
  geocam-edge discovery scan

Environment variables (all optional unless noted):
  GEOCAM_SAAS_URL               SaaS base URL (required for enroll/credential rotate/saas check)
  GEOCAM_ENROLLMENT_TOKEN       one-time enrollment token (alternative to stdin/--token)
  GEOCAM_PROCESSING_MODE        processing mode (default: cloud)
  GEOCAM_LOG_LEVEL              debug|info|warn|error (default: info)
  GEOCAM_SAAS_TIMEOUT           SaaS HTTP request timeout (default: 10s)
  GEOCAM_DATA_DIR               local state directory (default: /var/lib/geocam-edge)
  GEOCAM_HEALTH_ADDR            local health HTTP bind address (default: 127.0.0.1:8091)
  GEOCAM_HEARTBEAT_INTERVAL     heartbeat interval, 5s-5m (default: 30s)
  GEOCAM_ALLOW_INSECURE_HTTP    allow http:// (not https://) for GEOCAM_SAAS_URL (default: false)
  GEOCAM_OTA_PUBLIC_KEY_FILE    path to the Ed25519 public key for OTA signature verification
  GEOCAM_DISCOVERY_ENABLED      enable background discovery (default: true)
  GEOCAM_DISCOVERY_INTERVAL     background discovery interval, 1m-24h (default: 5m)
  GEOCAM_DISCOVERY_TIMEOUT      background discovery scan timeout, 1s-30s (default: 4s)
  GEOCAM_DISCOVERY_INTERFACES   comma-separated interfaces to scan (default: auto)
  GEOCAM_CONNECTIVITY_ENABLED   enable camera connectivity supervisor (default: true)
  GEOCAM_STREAM_ROLE            sub|main (default: sub)
  GEOCAM_STREAM_TIMEOUT         camera stream dial/packet timeout, 1s-60s (default: 5s)
`

func printRootUsage(w io.Writer) { fmt.Fprint(w, rootUsage) }

const runUsage = `Usage: geocam-edge run

Start the edge agent daemon: loads configuration, starts the local health
HTTP surface, heartbeat, discovery (if enabled) and camera connectivity
supervisor (if enabled), and blocks until SIGINT/SIGTERM.

This is the same code path used when geocam-edge is invoked with no command
at all.
`

func printRunUsage(w io.Writer) { fmt.Fprint(w, runUsage) }

const versionUsage = `Usage: geocam-edge version

Print version, commit, build date, platform and architecture. No secrets.

Equivalent to 'geocam-edge --version', which is also still supported.
`

func printVersionUsage(w io.Writer) { fmt.Fprint(w, versionUsage) }

const identityUsage = `Usage: geocam-edge identity

Print this Edge's identity: edge_id, identity_source, version, architecture,
processing_mode and data_dir. No secrets.
`

func printIdentityUsage(w io.Writer) { fmt.Fprint(w, identityUsage) }

const configUsage = `Usage: geocam-edge config

Print the effective, non-secret configuration loaded from the environment
(see 'geocam-edge --help' for the full list of GEOCAM_* variables), plus:

  enrollment_token: configured|not configured   (never the value)
  enrolled:         yes|no                      (from local credential state)

Never prints GEOCAM_ENROLLMENT_TOKEN's value or the stored credential.
`

func printConfigUsage(w io.Writer) { fmt.Fprint(w, configUsage) }

const checkUsage = `Usage: geocam-edge check

Query an already-running agent's local health HTTP surface (GEOCAM_HEALTH_ADDR,
default 127.0.0.1:8091) and print its status/edge_id/version/processing_mode/
uptime. Exits non-zero if the agent is unreachable or not READY. Suitable as
a systemd/Docker/Kubernetes health check.
`

func printCheckUsage(w io.Writer) { fmt.Fprint(w, checkUsage) }

const enrollUsage = `Usage: geocam-edge enroll [--token <token>]

Claim a one-time enrollment token against the SaaS (requires GEOCAM_SAAS_URL)
and persist the resulting credential locally. Fails if this Edge is already
enrolled — re-enrollment is never silent.

The enrollment token is resolved in this order:
  1. stdin, if piped                (preferred)
  2. GEOCAM_ENROLLMENT_TOKEN        (recommended for automation)
  3. --token <token>                (dev only — exposes the token in shell
                                      history and process listings)

A credential is generated locally and never leaves this device in plaintext
(only its SHA-256 hash is sent to the SaaS). The resulting credential and
identity are persisted under GEOCAM_DATA_DIR. Nothing is ever printed to
stdout/stderr that would leak the credential or the token.

Examples:
  echo "$TOKEN" | geocam-edge enroll
  GEOCAM_ENROLLMENT_TOKEN=... geocam-edge enroll
  geocam-edge enroll --token dev-only-token   # development only
`

func printEnrollUsage(w io.Writer) { fmt.Fprint(w, enrollUsage) }

const credentialUsage = `Usage: geocam-edge credential <subcommand>

Subcommands:
  rotate    Rotate the locally stored SaaS credential

Run 'geocam-edge credential rotate --help' for details.
`

func printCredentialUsage(w io.Writer) { fmt.Fprint(w, credentialUsage) }

const credentialRotateUsage = `Usage: geocam-edge credential rotate

Rotate this Edge's SaaS credential: a new credential is generated locally,
its hash is submitted to the SaaS (up to 3 attempts with backoff, stopping
immediately if the SaaS rejects the current credential as unauthorized), and
only once the SaaS acknowledges the rotation is the new credential persisted
to disk ATOMICALLY (temp file + rename) — the previous credential on disk is
never touched until the write fully succeeds.

If the SaaS acknowledges the rotation but the local write fails, the old
credential is left intact on disk but no longer valid against the SaaS: rerun
'geocam-edge credential rotate' to retry. Requires prior enrollment.
`

func printCredentialRotateUsage(w io.Writer) { fmt.Fprint(w, credentialRotateUsage) }

const discoveryUsage = `Usage: geocam-edge discovery <subcommand>

Subcommands:
  scan    Scan the LAN for ONVIF/RTSP camera devices

Run 'geocam-edge discovery scan --help' for details.
`

func printDiscoveryUsage(w io.Writer) { fmt.Fprint(w, discoveryUsage) }

const discoveryScanUsage = `Usage: geocam-edge discovery scan [--interface <iface[,iface...]>] [--timeout <duration>] [--json]

Run a one-shot ONVIF/WS-Discovery scan of the LAN and print discovered
camera devices.

Flags:
  --interface   comma-separated network interfaces to scan (default: auto-private,
                or GEOCAM_DISCOVERY_INTERFACES if set)
  --timeout     scan duration per interface (default: 4s)
  --json        output results as JSON instead of a formatted table

Examples:
  geocam-edge discovery scan
  geocam-edge discovery scan --interface eth0,wlan0 --timeout 8s
  geocam-edge discovery scan --json
`

func printDiscoveryScanUsage(w io.Writer) { fmt.Fprint(w, discoveryScanUsage) }

const saasUsage = `Usage: geocam-edge saas <subcommand>

Subcommands:
  check    Verify SaaS connectivity and authentication

Run 'geocam-edge saas check --help' for details.
`

func printSaasUsage(w io.Writer) { fmt.Fprint(w, saasUsage) }

const saasCheckUsage = `Usage: geocam-edge saas check

Verify real SaaS connectivity and authentication by calling the existing
GET /edge/me endpoint with the locally stored credential (no new SaaS
endpoint, no contract change).

  - GEOCAM_SAAS_URL not configured       -> FAIL, exit 1
  - not enrolled locally                 -> NOT ENROLLED, exit 1
  - enrolled, SaaS call succeeds         -> OK, exit 0
  - enrolled, SaaS call fails            -> FAIL with a specific reason
                                             (unreachable, timed out, credential
                                             rejected/revoked, insecure URL), exit 1

Never prints the stored credential.
`

func printSaasCheckUsage(w io.Writer) { fmt.Fprint(w, saasCheckUsage) }

const otaUsage = `Usage: geocam-edge ota <subcommand>

Subcommands:
  verify    Verify a downloaded OTA release's signature/checksum/layout
  sign      Sign a SHA256SUMS manifest (release pipeline only)

Run 'geocam-edge ota verify --help' or 'geocam-edge ota sign --help' for details.
`

func printOTAUsage(w io.Writer) { fmt.Fprint(w, otaUsage) }

const otaVerifyUsage = `Usage: geocam-edge ota verify -artifact-dir <staged-release-dir> [-public-key <path>] [-current-version <vX.Y.Z>]
   or: geocam-edge ota verify -artifact <path> -sha256sums <path> -signature <path> [-public-key <path>] [-version <vX.Y.Z> -arch <amd64|arm64>] [-current-version <vX.Y.Z>]

Verify a downloaded OTA release (Hito T4), fail-closed, in order:

  1. SHA256SUMS.sig is a valid Ed25519 signature of SHA256SUMS under the
     configured public key. No signature or no key -> reject; no
     checksum-only fallback exists.
  2. The artifact's real SHA-256 matches the (now-trusted) SHA256SUMS entry
     for its recorded artifact name.
  3. The candidate version is a valid, strictly newer version than
     -current-version (default: the running agent's, see 'geocam-edge
     version').
  4. The artifact's own embedded VERSION/ARCH exactly match the release
     descriptor's version/architecture -- a descriptor and its artifact
     are never allowed to diverge silently.

-artifact-dir is preferred: it points at a directory produced by this
appliance's own OTA download (metadata.json + SHA256SUMS + SHA256SUMS.sig +
the named artifact), and is the exact contract IA2's privileged updater
reuses unmodified against a root-owned snapshot of that directory. It
independently revalidates metadata.json's artifact_name as a safe local
filename (no path separators, no "..", not a symlink) rather than trusting
that the original download already did.

On full success in -artifact-dir mode, stdout contains EXACTLY ONE line
with the prefix "artifact=", naming the verified artifact:

  artifact=geocam-edge-v2.0.0-linux-amd64.tar.gz

That line is the parseable, machine-readable success signal -- it is never
printed on any failure path, regardless of which check failed.

Flag mode (-artifact/-sha256sums/-signature) is for ad-hoc verification;
step 4 only runs there when both -version and -arch are given, and it
never prints an "artifact=" line.
`

func printOTAVerifyUsage(w io.Writer) { fmt.Fprint(w, otaVerifyUsage) }

const otaSignUsage = `Usage: geocam-edge ota sign -sha256sums <path> -private-key <path> [-out <path>]

Sign a SHA256SUMS manifest with an Ed25519 private key, producing a
detached signature (default: <sha256sums>.sig). Used only by the release
pipeline (see .github/workflows/release.yml), driven from an externally
provisioned key (e.g. a GitHub Actions secret) -- never run on an
appliance, never bundles or prints the key.
`

func printOTASignUsage(w io.Writer) { fmt.Fprint(w, otaSignUsage) }
