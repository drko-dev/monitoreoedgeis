package appliance_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapUnenrolledWithoutTokenRemainsUnenrolled(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	bin := fakeBinary(t, t.TempDir(), "geocam-edge")

	out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.0.0"}, bin)
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}

	bootDir := filepath.Join(root, "boot")
	if err := os.MkdirAll(bootDir, 0o755); err != nil {
		t.Fatal(err)
	}

	env := []string{
		"GEOCAM_BOOT_DIR=" + bootDir,
		"GEOCAM_BOOT_FIRMWARE_DIR=" + filepath.Join(bootDir, "firmware"),
	}
	out, err = runScript(t, root, "bootstrap.sh", env)
	if err != nil {
		t.Fatalf("bootstrap.sh should succeed even without token (awaits enrollment): %v\n%s", err, out)
	}

	if !strings.Contains(out, "awaiting") {
		t.Errorf("expected bootstrap output to indicate awaiting enrollment, got:\n%s", out)
	}

	credFile := filepath.Join(root, "var/lib/geocam-edge/credentials.json")
	if _, err := os.Stat(credFile); !os.IsNotExist(err) {
		t.Errorf("credentials file should not exist when no token is provided: %s", credFile)
	}
}

func TestBootstrapWithTokenFileEnrollsAndWipesToken(t *testing.T) {
	requireBash(t)
	root := t.TempDir()

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "geocam-edge")
	// Fake geocam-edge that reads token from stdin, writes identity and credentials on enroll
	fakeScript := `#!/bin/sh
cmd="$1"
shift
case "$cmd" in
  version)
    echo "geocam-edge 1.0.0"
    ;;
  enroll)
    # Read token from stdin
    read -r token
    if [ -z "$token" ]; then
      echo "enroll error: no token received on stdin" >&2
      exit 1
    fi
    data_dir="${GEOCAM_DATA_DIR:-/var/lib/geocam-edge}"
    mkdir -p "$data_dir"
    printf '{"edge_id":"edge-test-123","token_received_len":%d}\n' "${#token}" > "$data_dir/identity.json"
    printf '{"device_id":"dev-123","credential":"secret-cred-xyz","tenant_id":"ten-1"}\n' > "$data_dir/credentials.json"
    echo "enrolled device edge-test-123"
    ;;
  config)
    echo "enrolled: yes"
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(binPath, []byte(fakeScript), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.0.0"}, binPath)
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}

	bootDir := filepath.Join(root, "boot")
	if err := os.MkdirAll(bootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const secretToken = "super-secret-zero-touch-token-777"
	tokenPath := filepath.Join(bootDir, "geocam-enroll.token")
	if err := os.WriteFile(tokenPath, []byte(secretToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	env := []string{
		"GEOCAM_BOOT_DIR=" + bootDir,
		"GEOCAM_BOOT_FIRMWARE_DIR=" + filepath.Join(bootDir, "firmware"),
		"GEOCAM_SAAS_URL=https://saas.example.com",
	}

	out, err = runScript(t, root, "bootstrap.sh", env)
	if err != nil {
		t.Fatalf("bootstrap.sh failed: %v\n%s", err, out)
	}

	if strings.Contains(out, secretToken) {
		t.Errorf("bootstrap.sh output leaked the secret token:\n%s", out)
	}

	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatalf("token file was not deleted after zero-touch bootstrap: %s still exists", tokenPath)
	}

	credFile := filepath.Join(root, "var/lib/geocam-edge/credentials.json")
	credBytes, err := os.ReadFile(credFile)
	if err != nil {
		t.Fatalf("failed to read persisted credentials: %v", err)
	}
	if !strings.Contains(string(credBytes), "secret-cred-xyz") {
		t.Fatalf("credentials do not contain expected content: %s", string(credBytes))
	}

	identFile := filepath.Join(root, "var/lib/geocam-edge/identity.json")
	if _, err := os.Stat(identFile); err != nil {
		t.Fatalf("identity file missing after bootstrap enroll: %v", err)
	}
}

func TestBootstrapAlreadyEnrolledSkipsEnrollment(t *testing.T) {
	requireBash(t)
	root := t.TempDir()

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "geocam-edge")
	fakeScript := `#!/bin/sh
cmd="$1"
shift
case "$cmd" in
  version)
    echo "geocam-edge 1.0.0"
    ;;
  enroll)
    echo "FAIL: enroll should never be invoked when already enrolled!" >&2
    exit 1
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(binPath, []byte(fakeScript), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.0.0"}, binPath)
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}

	dataDir := filepath.Join(root, "var/lib/geocam-edge")
	if err := os.WriteFile(filepath.Join(dataDir, "identity.json"), []byte(`{"edge_id":"stable-edge"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "credentials.json"), []byte(`{"credential":"stable-cred"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	bootDir := filepath.Join(root, "boot")
	if err := os.MkdirAll(bootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(bootDir, "geocam-enroll.token")
	if err := os.WriteFile(tokenPath, []byte("ignored-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	env := []string{
		"GEOCAM_BOOT_DIR=" + bootDir,
		"GEOCAM_BOOT_FIRMWARE_DIR=" + filepath.Join(bootDir, "firmware"),
		"GEOCAM_SAAS_URL=https://saas.example.com",
	}

	out, err = runScript(t, root, "bootstrap.sh", env)
	if err != nil {
		t.Fatalf("bootstrap.sh should succeed and skip enrollment: %v\n%s", err, out)
	}

	if !strings.Contains(out, "already enrolled") {
		t.Errorf("expected output to mention already enrolled, got:\n%s", out)
	}

	credBytes, err := os.ReadFile(filepath.Join(dataDir, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(credBytes), "stable-cred") {
		t.Errorf("existing credential overwritten: %s", string(credBytes))
	}
}

func TestBootstrapDiscoveryScanTriggered(t *testing.T) {
	requireBash(t)
	root := t.TempDir()

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "geocam-edge")
	scanMarker := filepath.Join(root, "discovery_scanned")
	fakeScript := `#!/bin/sh
cmd="$1"
shift
case "$cmd" in
  version)
    echo "geocam-edge 1.0.0"
    ;;
  discovery)
    if [ "$1" = "scan" ]; then
      touch "` + scanMarker + `"
      echo "DISCOVERY SCAN RESULTS: 2 device(s) found"
      exit 0
    fi
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(binPath, []byte(fakeScript), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := runScript(t, root, "install.sh", []string{"GEOCAM_VERSION=1.0.0"}, binPath)
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}

	dataDir := filepath.Join(root, "var/lib/geocam-edge")
	if err := os.WriteFile(filepath.Join(dataDir, "identity.json"), []byte(`{"edge_id":"edge-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "credentials.json"), []byte(`{"credential":"cred-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err = runScript(t, root, "bootstrap.sh", nil, "--scan")
	if err != nil {
		t.Fatalf("bootstrap.sh --scan failed: %v\n%s", err, out)
	}

	if _, err := os.Stat(scanMarker); os.IsNotExist(err) {
		t.Errorf("discovery scan marker was not created; scan was not executed")
	}
	if !strings.Contains(out, "running local ONVIF discovery scan") {
		t.Errorf("expected discovery scan log in output, got:\n%s", out)
	}
}

func TestBootstrapSystemdUnitTemplateStructure(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(scriptsDir(t), "..", "systemd", "geocam-edge-bootstrap.service.in"))
	if err != nil {
		t.Fatalf("reading bootstrap unit template: %v", err)
	}
	content := string(data)

	required := []string{
		"[Unit]", "[Service]", "[Install]",
		"After=network-online.target",
		"Before=geocam-edge.service",
		"Type=oneshot",
		"RemainAfterExit=yes",
		"ExecStart=@GEOCAM_BOOTSTRAP_EXEC@",
		"WantedBy=multi-user.target",
	}
	for _, want := range required {
		if !strings.Contains(content, want) {
			t.Errorf("bootstrap unit template missing required directive: %q", want)
		}
	}
}
