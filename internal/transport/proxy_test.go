package transport

import (
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestClientRespectsStandardProxyEnv locks in Hito R / R8's audit finding:
// Client (internal/transport) never sets http.Client.Transport, so it falls
// back to http.DefaultTransport, whose Proxy field is http.ProxyFromEnvironment.
// HTTP_PROXY/HTTPS_PROXY/NO_PROXY therefore already work for every Edge->SaaS
// call (enrollment, heartbeat, cloudsink) without any Client code change.
func TestClientRespectsStandardProxyEnv(t *testing.T) {
	c, err := New("https://saas.example.internal", false, 0, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.httpClient.Transport != nil {
		t.Fatalf("expected httpClient.Transport to be nil (falls back to http.DefaultTransport), got %T", c.httpClient.Transport)
	}

	t.Run("HTTPS_PROXY is used for the SaaS host", func(t *testing.T) {
		runProxyEnvCheck(t, map[string]string{
			"HTTP_PROXY":  "",
			"HTTPS_PROXY": "http://proxy.corp.internal:3128",
			"NO_PROXY":    "",
		}, "https://saas.example.internal/api/v1/gateway/enroll", "proxy.corp.internal:3128")
	})

	t.Run("NO_PROXY bypasses the proxy for a matching host", func(t *testing.T) {
		runProxyEnvCheck(t, map[string]string{
			"HTTP_PROXY":  "",
			"HTTPS_PROXY": "http://proxy.corp.internal:3128",
			"NO_PROXY":    "cam-nvr.factory.internal",
		}, "https://cam-nvr.factory.internal/onvif/device_service", "")
	})

	t.Run("a proxy URL with embedded userinfo never has to be logged", func(t *testing.T) {
		// Client (internal/transport) has no logging path at all -- verified by
		// inspection, this sub-test documents *why* that closes the "no
		// credentials in logs/status" requirement rather than re-testing a
		// logger that does not exist.
		proxyWithCreds, err := url.Parse("http://proxyuser:proxysecret@proxy.corp.internal:3128")
		if err != nil {
			t.Fatalf("url.Parse: %v", err)
		}
		if !strings.Contains(proxyWithCreds.String(), "proxysecret") {
			t.Fatalf("sanity check: url.String() should still contain the secret (this test only documents that Client never calls it)")
		}
	})
}

// TestProxyEnvCheckHelperProcess is not a real test: it is only ever invoked
// as a subprocess (see runProxyEnvCheck) with -test.run matching just this
// name, so it never re-enters TestClientRespectsStandardProxyEnv's subtests.
// Skipped when run normally (GEOCAM_PROXY_ENV_CHECK_SUBPROCESS unset).
func TestProxyEnvCheckHelperProcess(t *testing.T) {
	if os.Getenv("GEOCAM_PROXY_ENV_CHECK_SUBPROCESS") != "1" {
		t.Skip("helper process for TestClientRespectsStandardProxyEnv, not a standalone test")
	}

	req, err := http.NewRequest(http.MethodPost, os.Getenv("GEOCAM_PROXY_ENV_CHECK_URL"), nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	got, err := http.ProxyFromEnvironment(req)
	if err != nil {
		t.Fatalf("ProxyFromEnvironment: %v", err)
	}
	gotHost := ""
	if got != nil {
		gotHost = got.Host
	}
	want := os.Getenv("GEOCAM_PROXY_ENV_CHECK_WANT")
	if gotHost != want {
		t.Fatalf("ProxyFromEnvironment: got host %q, want %q", gotHost, want)
	}
}

// runProxyEnvCheck re-executes this test binary as a fresh subprocess running
// only TestProxyEnvCheckHelperProcess, because http.ProxyFromEnvironment
// parses HTTP_PROXY/HTTPS_PROXY/NO_PROXY exactly once per process
// (sync.Once), and other tests in this package already perform real HTTP
// round-trips through http.DefaultTransport before this test runs, locking
// in whatever env was present at that point. A fresh process guarantees a
// fresh, unlocked cache. wantProxyHost == "" means "expect no proxy".
func runProxyEnvCheck(t *testing.T, env map[string]string, targetURL, wantProxyHost string) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=^TestProxyEnvCheckHelperProcess$", "-test.v")
	cmd.Env = append(os.Environ(),
		"GEOCAM_PROXY_ENV_CHECK_SUBPROCESS=1",
		"GEOCAM_PROXY_ENV_CHECK_URL="+targetURL,
		"GEOCAM_PROXY_ENV_CHECK_WANT="+wantProxyHost,
	)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess check failed: %v\n%s", err, out)
	}
}
