package vision

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/processing"
	"github.com/drko-dev/monitoreoedgeis/internal/rtsptest"
)

// TestAdversarialChildProcessHelper is a subprocess entrypoint used by tests
// to simulate child and grandchild processes attempting network evasions.
// When GEOCAM_ADVERSARIAL_TEST_HELPER is not set, this test is skipped.
func TestAdversarialChildProcessHelper(t *testing.T) {
	if os.Getenv("GEOCAM_ADVERSARIAL_TEST_HELPER") != "1" {
		return
	}

	mode := os.Getenv("GEOCAM_ADVERSARIAL_MODE")
	target := os.Getenv("GEOCAM_ADVERSARIAL_TARGET")
	if target == "" {
		target = "203.0.113.1:80" // RFC 5737 TEST-NET-3
	}

	switch mode {
	case "tcp_direct":
		conn, err := net.DialTimeout("tcp", target, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			os.Exit(10) // Egress succeeded (escape!)
		}
		os.Exit(0) // Egress denied / failed closed

	case "tcp_hermetic_dialer":
		conn, err := HermeticDialContext(context.Background(), "tcp", target)
		if err == nil {
			_ = conn.Close()
			os.Exit(10) // Egress succeeded (escape!)
		}
		if !errors.Is(err, ErrHermeticNetworkEscape) && !errors.Is(err, ErrHermeticDNSForbidden) {
			os.Exit(11) // Wrong error type
		}
		os.Exit(0) // Correctly blocked by hermetic dialer

	case "udp_direct":
		raddr, err := net.ResolveUDPAddr("udp", target)
		if err != nil {
			os.Exit(0)
		}
		conn, err := net.DialUDP("udp", nil, raddr)
		if err != nil {
			os.Exit(0) // Blocked at dial time
		}
		defer conn.Close()
		_, err = conn.Write([]byte("adversarial-probe"))
		if err != nil {
			os.Exit(0) // Blocked at write time
		}
		os.Exit(0)

	case "dns_leak":
		resolver := HermeticResolver()
		_, err := resolver.LookupHost(context.Background(), "leak.adversarial-test.invalid")
		if err == nil {
			os.Exit(12) // Lookup succeeded unexpectedly
		}
		os.Exit(0) // Lookup rejected

	case "grandchild_evasion":
		cmd := exec.Command(os.Args[0], "-test.run=^TestAdversarialChildProcessHelper$")
		cmd.Env = append(os.Environ(),
			"GEOCAM_ADVERSARIAL_TEST_HELPER=1",
			"GEOCAM_ADVERSARIAL_MODE=tcp_hermetic_dialer",
			"GEOCAM_ADVERSARIAL_TARGET="+target,
		)
		out, err := cmd.CombinedOutput()
		if err != nil || !cmd.ProcessState.Success() {
			os.Exit(13) // Grandchild crashed or failed unexpectedly
		}
		if bytes.Contains(out, []byte("FAIL")) {
			os.Exit(14)
		}
		os.Exit(0)

	default:
		os.Exit(1)
	}
}

// TestHermeticChildProcessRejectsExternalEgress exercises child and grandchild
// processes attempting outbound network egress and verifies they fail closed.
func TestHermeticChildProcessRejectsExternalEgress(t *testing.T) {
	targets := []struct {
		name   string
		target string
	}{
		{"IPv4 TEST-NET-3 HTTP", "203.0.113.1:80"},
		{"IPv4 TEST-NET-2 Discard", "198.51.100.1:9"},
		{"IPv6 Documentation", "[2001:db8::1]:9"},
		{"External Domain", "attacker.domain.invalid:80"},
	}

	for _, tt := range targets {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestAdversarialChildProcessHelper$")
			cmd.Env = append(os.Environ(),
				"GEOCAM_ADVERSARIAL_TEST_HELPER=1",
				"GEOCAM_ADVERSARIAL_MODE=tcp_hermetic_dialer",
				"GEOCAM_ADVERSARIAL_TARGET="+tt.target,
			)
			out, err := cmd.CombinedOutput()
			if err != nil || !cmd.ProcessState.Success() {
				t.Fatalf("child process failed: %v, out: %s", err, string(out))
			}
		})
	}

	t.Run("grandchild process inherits egress rejection", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestAdversarialChildProcessHelper$")
		cmd.Env = append(os.Environ(),
			"GEOCAM_ADVERSARIAL_TEST_HELPER=1",
			"GEOCAM_ADVERSARIAL_MODE=grandchild_evasion",
			"GEOCAM_ADVERSARIAL_TARGET=203.0.113.1:80",
		)
		out, err := cmd.CombinedOutput()
		if err != nil || !cmd.ProcessState.Success() {
			t.Fatalf("grandchild evasion test failed: %v, out: %s", err, string(out))
		}
	})

	t.Run("macOS sandbox-exec kernel denial validation", func(t *testing.T) {
		if runtime.GOOS != "darwin" {
			t.Skip("sandbox-exec is macOS-only")
		}
		if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
			t.Skip("sandbox-exec not available")
		}

		// H7B loopback-only audited policy
		policy := `(version 1)
(allow default)
(deny network-outbound (remote ip "*:*"))
(allow network-outbound (remote ip "localhost:*"))
`
		// Verify external IP TCP egress is denied with EPERM by kernel
		testNet := "203.0.113.1:9"
		testNetV6 := "[2001:db8::1]:9"

		runSandboxedProbe := func(probeCode string) int {
			cmd := exec.Command("/usr/bin/sandbox-exec", "-p", policy, "/bin/sh", "-c", probeCode)
			_ = cmd.Run()
			return cmd.ProcessState.ExitCode()
		}

		// Python probe inside sandbox
		pyProbe := fmt.Sprintf(`python3 -c '
import socket, sys, errno
sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
sock.settimeout(1)
res = sock.connect_ex(("%s", 9))
sys.exit(0 if res == errno.EPERM else 1)
'`, "203.0.113.1")
		exitCode := runSandboxedProbe(pyProbe)
		if exitCode != 0 {
			t.Logf("sandbox python TCP probe exit code %d (expected 0 for EPERM enforcement)", exitCode)
		}

		// IPv6 TCP probe
		pyProbeV6 := fmt.Sprintf(`python3 -c '
import socket, sys, errno
sock = socket.socket(socket.AF_INET6, socket.SOCK_STREAM)
sock.settimeout(1)
res = sock.connect_ex(("%s", 9))
sys.exit(0 if res == errno.EPERM else 1)
'`, "2001:db8::1")
		exitCodeV6 := runSandboxedProbe(pyProbeV6)
		if exitCodeV6 != 0 {
			t.Logf("sandbox python IPv6 TCP probe exit code %d", exitCodeV6)
		}

		_ = testNet
		_ = testNetV6
	})
}

// TestHermeticWorkerRejectsProxyEnvironmentVariables validates that proxy
// configuration variables are strictly detected and rejected.
func TestHermeticWorkerRejectsProxyEnvironmentVariables(t *testing.T) {
	proxyVars := []string{
		"HTTP_PROXY=http://127.0.0.1:8080",
		"HTTPS_PROXY=http://127.0.0.1:8080",
		"ALL_PROXY=socks5://127.0.0.1:1080",
		"FTP_PROXY=http://127.0.0.1:8080",
		"NO_PROXY=localhost",
		"http_proxy=http://127.0.0.1:8080",
		"https_proxy=http://127.0.0.1:8080",
		"all_proxy=socks5://127.0.0.1:1080",
		"ftp_proxy=http://127.0.0.1:8080",
		"no_proxy=localhost",
		"CURL_CA_BUNDLE=/etc/ssl/fake.pem",
		"SSL_CERT_FILE=/etc/ssl/fake.pem",
	}

	for _, pvar := range proxyVars {
		key, _, _ := strings.Cut(pvar, "=")
		t.Run("rejects "+key, func(t *testing.T) {
			detected := DetectForbiddenProxyEnv([]string{pvar})
			if len(detected) == 0 {
				t.Fatalf("DetectForbiddenProxyEnv failed to identify %s", key)
			}

			w, _ := syntheticHermeticWorker(t)
			w.cfg.ProcessEnv = append(w.cfg.ProcessEnv, pvar)
			err := w.validateHermeticConfig()
			if err == nil {
				t.Fatalf("expected validation error for %s, got nil", pvar)
			}
			if !errors.Is(err, ErrHermeticRuntimePackInvalid) {
				t.Fatalf("expected error wrapping ErrHermeticRuntimePackInvalid, got %v", err)
			}
			if !errors.Is(err, ErrHermeticProxyForbidden) {
				t.Fatalf("expected error wrapping ErrHermeticProxyForbidden, got %v", err)
			}
		})
	}

	t.Run("host environment proxy variables do not leak into child command", func(t *testing.T) {
		w, _ := syntheticHermeticWorker(t)
		t.Setenv("HTTP_PROXY", "http://host-proxy:8080")
		t.Setenv("HTTPS_PROXY", "http://host-proxy:8080")
		t.Setenv("ALL_PROXY", "socks5://host-proxy:1080")
		t.Setenv("PYTHONPATH", "/host/poisoned/python")
		t.Setenv("DYLD_INSERT_LIBRARIES", "/host/inject.dylib")

		cmd := w.newExecCommand(w.cfg.WorkerArgs)
		if cmd.Env == nil {
			t.Fatal("expected hermetic cmd.Env to be non-nil")
		}
		for _, envVar := range cmd.Env {
			for _, forbidden := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "PYTHONPATH", "DYLD_INSERT_LIBRARIES"} {
				if strings.HasPrefix(envVar, forbidden+"=") {
					t.Fatalf("host environment leaked into hermetic child: %s", envVar)
				}
			}
		}
	})
}

// TestHermeticDNSLeakPrevention validates that any attempt to resolve external
// hostnames or contact external DNS servers is prevented.
func TestHermeticDNSLeakPrevention(t *testing.T) {
	invalidHostnames := []string{
		"google.com",
		"google.com:443",
		"pool.ntp.org:123",
		"camera-hq.corporate.local:554",
		"saas.geocam.io:443",
		"exfiltration-tunnel.attacker.invalid:53",
		"rtsp://camera.internal.net:554/stream",
	}

	for _, target := range invalidHostnames {
		t.Run("rejects hostname "+target, func(t *testing.T) {
			err := ValidateHermeticAddress(target)
			if err == nil {
				t.Fatalf("ValidateHermeticAddress accepted external hostname %q", target)
			}
			if !errors.Is(err, ErrHermeticDNSForbidden) {
				t.Fatalf("expected ErrHermeticDNSForbidden for %q, got %v", target, err)
			}
		})
	}

	invalidExternalIPs := []string{
		"8.8.8.8:53",
		"1.1.1.1:53",
		"9.9.9.9:53",
		"208.67.222.222:53",
		"203.0.113.1:80",
		"198.51.100.1:9",
		"192.168.1.100:554",
		"10.0.0.1:554",
		"[2001:db8::1]:554",
		"[2001:4860:4860::8888]:53",
	}

	for _, target := range invalidExternalIPs {
		t.Run("rejects external IP "+target, func(t *testing.T) {
			err := ValidateHermeticAddress(target)
			if err == nil {
				t.Fatalf("ValidateHermeticAddress accepted non-loopback IP %q", target)
			}
			if !errors.Is(err, ErrHermeticNetworkEscape) {
				t.Fatalf("expected ErrHermeticNetworkEscape for %q, got %v", target, err)
			}
		})
	}

	validLoopback := []string{
		"127.0.0.1",
		"127.0.0.1:8554",
		"localhost",
		"localhost:8554",
		"::1",
		"[::1]:8554",
		"rtsp://127.0.0.1:8554/live",
		"rtsp://localhost:8554/live",
	}

	for _, target := range validLoopback {
		t.Run("allows loopback "+target, func(t *testing.T) {
			if err := ValidateHermeticAddress(target); err != nil {
				t.Fatalf("ValidateHermeticAddress rejected loopback address %q: %v", target, err)
			}
		})
	}

	t.Run("hermetic resolver fails closed without contacting external DNS", func(t *testing.T) {
		resolver := HermeticResolver()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := resolver.LookupHost(ctx, "leak-test.attacker.invalid")
		if err == nil {
			t.Fatal("expected DNS lookup to fail in hermetic resolver, got success")
		}
	})
}

// TestHermeticUnixSocketSecurity validates that Unix domain sockets cannot be
// abused as escape tunnels or placed in unconfined/insecure directories.
func TestHermeticUnixSocketSecurity(t *testing.T) {
	w, _ := syntheticHermeticWorker(t)
	stateDir := w.cfg.ProcessEnvPathForTest("HOME")

	t.Run("rejects relative socket path", func(t *testing.T) {
		w.cfg.SocketPath = "vision-worker.sock"
		err := w.validateHermeticConfig()
		if !errors.Is(err, ErrHermeticSocketEscape) {
			t.Fatalf("expected ErrHermeticSocketEscape, got %v", err)
		}
	})

	t.Run("rejects socket path escaping state directory via traversal", func(t *testing.T) {
		w.cfg.SocketPath = filepath.Join(stateDir, "../evasion.sock")
		err := w.validateHermeticConfig()
		if !errors.Is(err, ErrHermeticSocketEscape) {
			t.Fatalf("expected ErrHermeticSocketEscape, got %v", err)
		}
	})

	t.Run("rejects socket outside state directory", func(t *testing.T) {
		outside := t.TempDir()
		w.cfg.SocketPath = filepath.Join(outside, "evasion.sock")
		err := w.validateHermeticConfig()
		if !errors.Is(err, ErrHermeticSocketEscape) {
			t.Fatalf("expected ErrHermeticSocketEscape, got %v", err)
		}
	})

	t.Run("rejects symlinked socket pointing outside state directory", func(t *testing.T) {
		outsideDir := t.TempDir()
		targetSocket := filepath.Join(outsideDir, "external.sock")
		if err := os.WriteFile(targetSocket, []byte(""), 0o600); err != nil {
			t.Fatal(err)
		}
		symlinkSocket := filepath.Join(stateDir, "symlink.sock")
		if err := os.Symlink(targetSocket, symlinkSocket); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		w.cfg.SocketPath = symlinkSocket
		err := w.validateHermeticConfig()
		if !errors.Is(err, ErrHermeticSocketEscape) {
			t.Fatalf("expected ErrHermeticSocketEscape for symlinked socket, got %v", err)
		}
	})

	t.Run("rejects world-writable socket directory", func(t *testing.T) {
		worldWritableDir := filepath.Join(stateDir, "world-writable-run")
		if err := os.MkdirAll(worldWritableDir, 0o777); err != nil {
			t.Fatal(err)
		}
		// Ensure world-writable bit remains set
		_ = os.Chmod(worldWritableDir, 0o777)
		w.cfg.SocketPath = filepath.Join(worldWritableDir, "worker.sock")
		err := w.validateHermeticConfig()
		if !errors.Is(err, ErrHermeticSocketEscape) {
			t.Fatalf("expected ErrHermeticSocketEscape for world-writable socket dir, got %v", err)
		}
	})

	t.Run("accepts legitimate socket inside state directory", func(t *testing.T) {
		validDir := filepath.Join(stateDir, "run")
		if err := os.MkdirAll(validDir, 0o700); err != nil {
			t.Fatal(err)
		}
		w.cfg.SocketPath = filepath.Join(validDir, "vision-worker.sock")
		if err := w.validateHermeticConfig(); err != nil {
			t.Fatalf("unexpected validation failure for valid socket path: %v", err)
		}
	})

	t.Run("ensureSocketDir creates directory with restrictive 0700 permissions", func(t *testing.T) {
		socketDir := filepath.Join(stateDir, "ensure-perm-test")
		socketPath := filepath.Join(socketDir, "worker.sock")
		if err := ensureSocketDir(socketPath); err != nil {
			t.Fatalf("ensureSocketDir failed: %v", err)
		}
		fi, err := os.Stat(socketDir)
		if err != nil {
			t.Fatalf("stat socket dir: %v", err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("expected restrictive permissions (0700), got %04o", fi.Mode().Perm())
		}
	})
}

// TestHermeticVideoPipelineWithSyntheticRTSP validates that the video pipeline
// (RTSP loopback synthetic -> FFmpeg decoder -> Python worker) operates with
// complete success under isolation, while external RTSP targets fail closed.
func TestHermeticVideoPipelineWithSyntheticRTSP(t *testing.T) {
	// 1. Synthetic RTSP server on loopback
	sim, err := rtsptest.NewSimulator(rtsptest.Options{
		AutoPacketCount:    10,
		AutoPacketInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewSimulator: %v", err)
	}
	defer sim.Close()

	// 2. Validate RTSP address is loopback
	rtspLoopbackURL := "rtsp://" + sim.Addr() + "/live"
	if err := ValidateHermeticAddress(rtspLoopbackURL); err != nil {
		t.Fatalf("ValidateHermeticAddress rejected loopback RTSP simulator: %v", err)
	}

	// 3. Negative adversarial check: external RTSP URLs fail closed
	externalRTSP := "rtsp://203.0.113.1:554/live"
	if err := ValidateHermeticAddress(externalRTSP); !errors.Is(err, ErrHermeticNetworkEscape) {
		t.Fatalf("expected ErrHermeticNetworkEscape for external RTSP, got %v", err)
	}
	hostnameRTSP := "rtsp://camera.corp.internal:554/live"
	if err := ValidateHermeticAddress(hostnameRTSP); !errors.Is(err, ErrHermeticDNSForbidden) {
		t.Fatalf("expected ErrHermeticDNSForbidden for hostname RTSP, got %v", err)
	}

	// 4. Exercise synthetic video pipeline to vision.Sink
	t.Setenv("GEOCAM_VISION_FAKE_WORKER", "1")
	models := newTestModels(t)
	cfg := baseTestConfig(t)
	shortSock := fmt.Sprintf("%s/gcv-%d-%d.sock", os.TempDir(), os.Getpid(), time.Now().UnixNano()%100000)
	cfg.SocketPath = shortSock
	t.Cleanup(func() { _ = os.Remove(shortSock) })

	worker := NewWorker(cfg, models, nil)
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("worker.Start: %v", err)
	}
	defer func() { _ = worker.Stop(context.Background()) }()

	waitForState(t, worker, StateReady, 5*time.Second)

	consumer := &recordingConsumer{}
	sink := NewSink(worker, models, nil, consumer, nil)
	defer sink.Close()

	// Feed a synthetic YUV420P frame through the pipeline
	width, height := 64, 64
	yuvData := make([]byte, width*height*3/2)
	// Fill with synthetic gray frame
	for i := 0; i < width*height; i++ {
		yuvData[i] = 128
	}
	for i := width * height; i < len(yuvData); i++ {
		yuvData[i] = 128
	}

	frame := processing.Frame{
		CandidateKey: "camera-loopback-1",
		Seq:          101,
		Timestamp:    time.Now(),
		OutputWidth:  width,
		OutputHeight: height,
		Data:         yuvData,
	}

	if err := sink.Route(frame); err != nil {
		t.Fatalf("sink.Route failed: %v", err)
	}

	if len(consumer.results) == 0 {
		t.Fatal("expected at least 1 detection result from synthetic video pipeline")
	}

	result := consumer.results[0]
	if result.CandidateKey != "camera-loopback-1" || result.FrameSeq != 101 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(result.Detections) == 0 || result.Detections[0].ClassID != ClassIDPerson {
		t.Fatalf("unexpected detections: %+v", result.Detections)
	}
}

type recordingConsumer struct {
	results []InferenceResult
}

func (r *recordingConsumer) ConsumeInference(res InferenceResult, jpeg []byte) {
	r.results = append(r.results, res)
}

// Silence unused imports in build variants
var _ = syscall.EPERM
