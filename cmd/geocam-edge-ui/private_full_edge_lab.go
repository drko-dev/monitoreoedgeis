//go:build privatefullgelab

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// PrivateFullEdgeLabResult is deliberately NOT a release-readiness report.
// This test uses user-owned, local files and a loopback RTSP simulator only.
type PrivateFullEdgeLabResult struct {
	Passed bool   `json:"passed"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type privateLabHarnessReport struct {
	Status               string `json:"status"`
	RequiredMarkerFound  bool   `json:"required_marker_found"`
	SandboxedCommandExit int    `json:"sandboxed_command_exit"`
}

func labReadableFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("fixture is not a regular file")
	}
	return nil
}

// RunPrivateFullEdgeLabTest is compiled ONLY with privatefullgelab.
// It offers no user-supplied command, credentials, camera address, model
// redistribution, installation, deploy or production readiness control.
// It uses the locally existing user models and a loopback-only test stream.
func (a *App) RunPrivateFullEdgeLabTest() (PrivateFullEdgeLabResult, error) {
	fail := func(detail string) (PrivateFullEdgeLabResult, error) {
		return PrivateFullEdgeLabResult{Passed: false, Status: "LAB_TEST_FAILED", Detail: detail}, nil
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return fail("Esta prueba privada requiere macOS Apple Silicon.")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fail("No se pudo encontrar el directorio personal.")
	}
	exe, err := os.Executable()
	if err != nil {
		return fail("No se encontró el ejecutable.")
	}
	resources := filepath.Clean(filepath.Join(filepath.Dir(exe), "..", "Resources"))
	binary := filepath.Join(resources, "geocam-lab-e2e.test")
	runner := filepath.Join(resources, "h7b-egress-cert.py")
	policy := filepath.Join(resources, "mac-h7b-loopback-egress.sb")
	pack := filepath.Join(home, "Documents", "proyectos", "monitoreoedgeis-worktrees",
		"offline-h6d", "runtime-lab", "packs", "lab-2026.10.14-0e8657f-r4")
	fixture := filepath.Join(home, "Documents", "proyectos", "monitoreoedgeis-worktrees",
		"offline-h6d", "runtime-lab", "media-smoke", "person-annexb.h264")
	models := filepath.Join(home, "geocam-edge-data-fulledge", "models")
	for _, p := range []string{binary, runner, policy, fixture,
		filepath.Join(models, "yolo11n.pt"),
		filepath.Join(models, "yolo11s-pose.pt"),
		filepath.Join(pack, "lab-manifest.json")} {
		if err := labReadableFile(p); err != nil {
			return fail("Falta un componente o modelo local necesario para el test.")
		}
	}
	if info, err := os.Stat(binary); err != nil || info.Mode().Perm()&0111 == 0 {
		return fail("El comprobador local no tiene permiso de ejecución.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	if a.ctx != nil {
		select {
		case <-a.ctx.Done():
			return fail("La aplicación se cerró.")
		default:
		}
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/python3",
		runner, "--require-marker", "RTSP_GO_FFMPEG_YOLO_PASS",
		"--", binary, "-test.run", "^TestLabRTSPToRealYOLO$", "-test.v")
	cmd.Dir = resources
	cmd.Env = []string{
		"HOME=" + home, "USER=" + filepath.Base(home),
		"TMPDIR=" + os.TempDir(), "PATH=/usr/bin:/bin",
		"H6D_LAB_PACK=" + pack,
		"H6D_LAB_MODELS=" + models,
		"H6D_LAB_H264=" + fixture,
		"H6D_LAB_DEVICE=mps",
		"HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1",
	}
	output, runErr := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fail("El test local excedió el tiempo máximo.")
	}
	// Only report the vetted runner's sanitized JSON, not raw model paths or
	// potentially sensitive output from a worker subprocess.
	var report privateLabHarnessReport
	if err := json.Unmarshal(output, &report); err != nil {
		return fail("La prueba local no devolvió un resultado verificable.")
	}
	if runErr != nil || report.Status != "LAB_EGRESS_EVIDENCE_PASS_NOT_CERTIFIED" ||
		!report.RequiredMarkerFound || report.SandboxedCommandExit != 0 {
		return fail("No se confirmó detección de persona. Revisar el laboratorio local.")
	}
	return PrivateFullEdgeLabResult{
		Passed: true,
		Status: report.Status,
		Detail: strings.Join([]string{
			"Persona detectada con modelos locales.",
			"RTSP simulado → FFmpeg → Worker Python → YOLO (MPS).",
			"Conexiones externas bloqueadas para el proceso de prueba.",
			"Sin cámaras reales, sin publicar modelos."}, " "),
	}, nil
}
