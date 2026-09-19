package vision

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"
)

// TestMain re-executes this same test binary as the "worker subprocess" when
// GEOCAM_VISION_FAKE_WORKER=1 is set, the standard Go pattern for testing
// exec.Command-based supervisors (see os/exec_test.go) — avoids depending on
// a real python3 + Ultralytics install in CI. Test functions set
// Config.WorkerCmd to os.Args[0] with the right flags/env instead of a
// real interpreter path.
func TestMain(m *testing.M) {
	if os.Getenv("GEOCAM_VISION_FAKE_WORKER") == "1" {
		fakeWorkerMain()
		return
	}
	os.Exit(m.Run())
}

// fakeWorkerMain implements just enough of the wire protocol to exercise
// Worker/Sink: health handshake, one inference reply with a canned
// detection, and shutdown. Behavior is steered by env vars so individual
// tests can simulate a crash, a hang (timeout), or a rejected handshake
// without needing separate binaries.
func fakeWorkerMain() {
	socketPath := flagValue("--socket")
	if socketPath == "" {
		os.Exit(2)
	}
	_ = os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		os.Exit(2)
	}
	defer ln.Close()

	if os.Getenv("GEOCAM_VISION_FAKE_REFUSE_HEALTH") == "1" {
		conn, err := ln.Accept()
		if err != nil {
			os.Exit(2)
		}
		defer conn.Close()
		_, _ = readLine(conn) // health request
		writeResp(conn, wireResponse{Type: "health_ok", Ready: false, Error: "model load failed"})
		time.Sleep(time.Hour) // caller kills this process right after rejecting the handshake
	}

	conn, err := ln.Accept()
	if err != nil {
		os.Exit(2)
	}
	defer conn.Close()

	for {
		req, err := readRequest(conn)
		if err != nil {
			return
		}
		switch req.Type {
		case "health":
			// The effective device defaults to "cpu" and can be overridden, so a
			// test can assert that the effective and requested devices are
			// reported separately rather than one being echoed as the other.
			effective := os.Getenv("GEOCAM_VISION_FAKE_EFFECTIVE_DEVICE")
			if effective == "" {
				effective = "cpu"
			}
			requested := os.Getenv("GEOCAM_VISION_FAKE_REQUESTED_DEVICE")
			if requested == "" {
				requested = effective
			}
			writeResp(conn, wireResponse{
				Type: "health_ok", Ready: true, Device: effective, DeviceRequested: requested,
				ModelsLoaded: []string{"yolo11s-pose.pt", "yolo11n.pt"},
			})
		case "infer":
			if os.Getenv("GEOCAM_VISION_FAKE_CRASH_ON_INFER") == "1" {
				os.Exit(1) // simulates the subprocess dying mid-request
			}
			if d := os.Getenv("GEOCAM_VISION_FAKE_INFER_DELAY_MS"); d != "" {
				if ms, err := time.ParseDuration(d + "ms"); err == nil {
					time.Sleep(ms)
				}
			}
			writeResp(conn, wireResponse{
				Type:        "result",
				FrameSeq:    req.FrameSeq,
				InferenceMS: 12.5,
				Detections: []Detection{
					{ClassID: ClassIDPerson, Label: "person", Type: DetectionTypePerson, Confidence: 0.91, BBox: [4]float64{1, 2, 3, 4}},
				},
			})
		case "shutdown":
			writeResp(conn, wireResponse{Type: "health_ok"})
			return
		}
	}
}

func flagValue(name string) string {
	for i, a := range os.Args {
		if a == name && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return ""
}

func readRequest(conn net.Conn) (wireRequest, error) {
	line, err := readLine(conn)
	if err != nil {
		return wireRequest{}, err
	}
	var req wireRequest
	err = json.Unmarshal(line, &req)
	return req, err
}

func readLine(conn net.Conn) ([]byte, error) {
	rd := bufio.NewReader(conn)
	return rd.ReadBytes('\n')
}

func writeResp(conn net.Conn, resp wireResponse) {
	b, _ := json.Marshal(resp)
	b = append(b, '\n')
	_, _ = conn.Write(b)
}
