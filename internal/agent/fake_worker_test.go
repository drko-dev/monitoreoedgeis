package agent

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("GEOCAM_VISION_FAKE_WORKER") == "1" {
		fakeWorkerMain()
		return
	}
	os.Exit(m.Run())
}

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

	conn, err := ln.Accept()
	if err != nil {
		os.Exit(2)
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			return
		}
		switch req.Type {
		case "health":
			resp, _ := json.Marshal(map[string]any{
				"type":          "health_ok",
				"ready":         true,
				"device":        "cpu",
				"models_loaded": []string{"yolo11s-pose.pt", "yolo11n.pt"},
			})
			_, _ = conn.Write(append(resp, '\n'))
		case "shutdown":
			resp, _ := json.Marshal(map[string]any{"type": "health_ok"})
			_, _ = conn.Write(append(resp, '\n'))
			return
		}
	}
}

func flagValue(flag string) string {
	for i := 0; i+1 < len(os.Args); i++ {
		if os.Args[i] == flag {
			return os.Args[i+1]
		}
		if strings.HasPrefix(os.Args[i], flag+"=") {
			return strings.TrimPrefix(os.Args[i], flag+"=")
		}
	}
	return ""
}
