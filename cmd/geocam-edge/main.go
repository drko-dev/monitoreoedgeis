// Command geocam-edge is the GEO CAM Edge agent.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/agent"
	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/discovery"
	"github.com/drko-dev/monitoreoedgeis/internal/health"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/platform"
	"github.com/drko-dev/monitoreoedgeis/internal/transport"
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
		case "enroll":
			runEnrollCmd(os.Args[2:])
			return
		case "credential":
			runCredentialCmd(os.Args[2:])
			return
		case "discovery":
			runDiscoveryCmd(os.Args[2:])
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

// runEnrollCmd claims a one-time enrollment token against the SaaS and
// persists the resulting credential. It always reuses the existing
// (persisted) edge_id — it never generates a new one.
func runEnrollCmd(args []string) {
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
		fmt.Fprintf(os.Stderr, "geocam-edge enroll: %s\n", saasErrorMessage(err))
		os.Exit(1)
	}
	if warning != "" {
		fmt.Fprintf(os.Stderr, "geocam-edge enroll: warning: %s\n", warning)
	}

	if err := credentials.Save(cfg.DataDir, creds); err != nil {
		fmt.Fprintf(os.Stderr, "geocam-edge enroll: enrollment succeeded but persisting credentials failed: %v\n", err)
		os.Exit(1)
	}

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
func runCredentialCmd(args []string) {
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
		fmt.Fprintf(os.Stderr, "geocam-edge credential rotate: rotated and persisted, but verification against %s failed: %v\n",
			transport.MePath, err)
		os.Exit(1)
	}

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
