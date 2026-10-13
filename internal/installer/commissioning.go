package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/drko-dev/monitoreoedgeis/internal/config"
	"github.com/drko-dev/monitoreoedgeis/internal/credentials"
	"github.com/drko-dev/monitoreoedgeis/internal/identity"
	"github.com/drko-dev/monitoreoedgeis/internal/service"
)

// CommissioningStepStatus represents the status of a specific milestone in activation.
type CommissioningStepStatus string

const (
	StepStatusPending CommissioningStepStatus = "PENDING"
	StepStatusRunning CommissioningStepStatus = "RUNNING"
	StepStatusSuccess CommissioningStepStatus = "SUCCESS"
	StepStatusFailed  CommissioningStepStatus = "FAILED"
	StepStatusSkipped CommissioningStepStatus = "SKIPPED"
)

// CommissioningStatus represents the multi-stage operational readiness of the Edge host.
type CommissioningStatus struct {
	DaemonInstalled   bool                    `json:"daemon_installed"`
	ServiceRegistered bool                    `json:"service_registered"`
	DaemonRunning     bool                    `json:"daemon_running"`
	HealthzOK         bool                    `json:"healthz_ok"`
	ReadyzOK          bool                    `json:"readyz_ok"`
	OperationalState  string                  `json:"operational_state"`
	OperationalOK     bool                    `json:"operational_ok"`
	SaaSSyncOK        bool                    `json:"saas_sync_ok"`
	HeartbeatState    string                  `json:"heartbeat_state"`
	ProcessingMode    string                  `json:"processing_mode"`
	EffectiveProfile  string                  `json:"effective_profile"`
	VisionWorkerReady bool                    `json:"vision_worker_ready"`
	CameraCount       int                     `json:"camera_count"`
	OverallReady      bool                    `json:"overall_ready"`
	SafeMessage       string                  `json:"safe_message"`
}

// CommissionOutcome represents the terminal result of the commissioning workflow.
type CommissionOutcome string

const (
	CommissionOutcomeSuccess        CommissionOutcome = "SUCCESS"
	CommissionOutcomeActionRequired CommissionOutcome = "ACTION_REQUIRED"
	CommissionOutcomeFailed         CommissionOutcome = "FAILED"
)

// CommissionResult is the response payload returned after executing daemon activation.
type CommissionResult struct {
	Outcome     CommissionOutcome    `json:"outcome"`
	Status      *CommissioningStatus `json:"status"`
	SafeMessage string               `json:"safe_message"`
	Details     []string             `json:"details,omitempty"`
}

// StagedDaemonPath returns the canonical path where the official geocam-edge binary resides.
func (s *Service) StagedDaemonPath() string {
	binName := "geocam-edge"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	return filepath.Join(s.DataDir, "bin", binName)
}

// EnsureDaemonBinary locates the companion daemon binary and installs it to <DataDir>/bin/geocam-edge.
func (s *Service) EnsureDaemonBinary() (string, error) {
	target := s.StagedDaemonPath()
	if info, err := os.Stat(target); err == nil && !info.IsDir() && (info.Mode()&0111 != 0 || runtime.GOOS == "windows") {
		return target, nil
	}

	src, err := s.locateCompanionDaemon()
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", fmt.Errorf("create daemon bin directory: %w", err)
	}

	if err := copyExecutableFile(src, target); err != nil {
		return "", fmt.Errorf("stage daemon executable: %w", err)
	}

	return target, nil
}

func (s *Service) locateCompanionDaemon() (string, error) {
	binName := "geocam-edge"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}

	// 1. Check next to running executable (e.g. inside macOS .app bundle Contents/MacOS/)
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidate := filepath.Join(dir, binName)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}

	// 2. Check in PATH
	if path, err := exec.LookPath(binName); err == nil {
		return path, nil
	}

	// 3. Check common development and system locations
	candidates := []string{
		filepath.Join(".", "bin", binName),
		filepath.Join(".", binName),
		filepath.Join(s.DataDir, "bin", binName),
	}
	for _, c := range candidates {
		if abs, err := filepath.Abs(c); err == nil {
			if info, err := os.Stat(abs); err == nil && !info.IsDir() {
				return abs, nil
			}
		}
	}

	return "", fmt.Errorf("official daemon binary %s could not be located in application bundle or system path", binName)
}

func copyExecutableFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}

	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	_ = os.Chmod(tmp, 0755)
	return os.Rename(tmp, dst)
}

// GetCommissioningStatus inspects all lifecycle, service, HTTP health, and SaaS sync layers.
func (s *Service) GetCommissioningStatus(ctx context.Context) (*CommissioningStatus, error) {
	status := &CommissioningStatus{}

	daemonPath := s.StagedDaemonPath()
	if info, err := os.Stat(daemonPath); err == nil && !info.IsDir() {
		status.DaemonInstalled = true
	}

	status.ServiceRegistered = s.checkServiceInstalled()
	status.DaemonRunning = s.checkDaemonRunning(ctx)

	if status.DaemonRunning {
		status.HealthzOK = s.probeHTTP(ctx, "/healthz") == http.StatusOK
		status.ReadyzOK = s.probeHTTP(ctx, "/readyz") == http.StatusOK

		if snap, ok := s.fetchFullStatus(ctx); ok {
			status.ProcessingMode = snap.ProcessingMode
			status.EffectiveProfile = string(snap.Profile)
			status.CameraCount = len(snap.Cameras)

			if snap.Vision != nil && snap.Vision.Worker != nil {
				status.VisionWorkerReady = snap.Vision.Worker.State == "ready"
			}

			if snap.Heartbeat != nil {
				status.HeartbeatState = snap.Heartbeat.State
				status.SaaSSyncOK = snap.Heartbeat.State == "running" && !snap.Heartbeat.LastSuccessAt.IsZero()
			}

			status.OperationalState = snap.Operational.State
			status.OperationalOK = snap.Operational.State == "READY" ||
				(snap.Operational.State == "NOT_CONFIGURED" && snap.Profile == config.ProfileGatewayNoMedia)
		}
	}

	// Overall ready requires service registered, running, healthz 200, and heartbeat active
	if status.ServiceRegistered && status.DaemonRunning && status.HealthzOK && status.SaaSSyncOK {
		status.OverallReady = true
		status.SafeMessage = "GEO CAM Edge está activo, conectado y reportando a la nube."
	} else if !status.ServiceRegistered {
		status.SafeMessage = "El servicio en segundo plano de GEO CAM Edge no está instalado."
	} else if !status.DaemonRunning {
		status.SafeMessage = "El servicio está instalado pero el daemon no está en ejecución."
	} else if !status.SaaSSyncOK {
		status.SafeMessage = "El daemon está ejecutándose localmente pero aún no sincroniza con el SaaS."
	} else {
		status.SafeMessage = "GEO CAM Edge se encuentra en proceso de inicialización."
	}

	return status, nil
}

// CommissionDaemon executes the full idempotent commissioning flow:
// 1. Verify existing identity and credentials.
// 2. Stage official geocam-edge executable into <DataDir>/bin/geocam-edge.
// 3. Register service (install).
// 4. Start/kickstart service (start).
// 5. Poll loopback endpoints (/healthz, /readyz, /status) until verified or timeout.
func (s *Service) CommissionDaemon(ctx context.Context) (*CommissionResult, error) {
	var details []string

	// 1. Identity & Credentials check
	ident, err := identity.LoadExisting(s.DataDir)
	if err != nil || !ident.IsEnrolled() {
		return &CommissionResult{
			Outcome:     CommissionOutcomeActionRequired,
			SafeMessage: "El equipo debe ser vinculado antes de activar el servicio.",
		}, nil
	}

	creds, err := credentials.Load(s.DataDir)
	if err != nil || !creds.IsEnrolled() {
		return &CommissionResult{
			Outcome:     CommissionOutcomeActionRequired,
			SafeMessage: "Las credenciales locales no son válidas. Se requiere vinculación.",
		}, nil
	}
	details = append(details, fmt.Sprintf("Identidad verificada (Edge ID: %s)", ident.EdgeID))

	// 2. Stage binary
	daemonExe, err := s.EnsureDaemonBinary()
	if err != nil {
		return &CommissionResult{
			Outcome:     CommissionOutcomeFailed,
			SafeMessage: fmt.Sprintf("No se pudo preparar el binario del servicio: %v", err),
		}, nil
	}
	details = append(details, fmt.Sprintf("Binario del daemon preparado en: %s", daemonExe))

	// 3. Persistent config path
	configPath := s.ConfigFilePath
	if configPath == "" {
		if resolved, err := config.PersistentConfigPath(); err == nil {
			configPath = resolved
		}
	}
	if configPath == "" || !fileExists(configPath) {
		return &CommissionResult{
			Outcome:     CommissionOutcomeActionRequired,
			SafeMessage: "El archivo de configuración persistente edge.env no fue encontrado.",
		}, nil
	}

	// 4. Register and Start Service via service.Command
	opts := service.Options{
		Executable: daemonExe,
		DataDir:    s.DataDir,
		ConfigFile: configPath,
	}

	if err := service.Command("install", opts); err != nil {
		return &CommissionResult{
			Outcome:     CommissionOutcomeFailed,
			SafeMessage: fmt.Sprintf("Fallo al registrar el servicio del sistema: %v", err),
			Details:     details,
		}, nil
	}
	details = append(details, "Servicio registrado correctamente en el sistema")

	if err := service.Command("start", opts); err != nil {
		return &CommissionResult{
			Outcome:     CommissionOutcomeFailed,
			SafeMessage: fmt.Sprintf("Fallo al iniciar el servicio: %v", err),
			Details:     details,
		}, nil
	}
	details = append(details, "Comando de inicio del servicio enviado")

	// 5. Verification poll loop (up to 15 seconds)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if s.checkDaemonRunning(ctx) {
			status, _ := s.GetCommissioningStatus(ctx)
			if status.DaemonRunning && status.HealthzOK {
				details = append(details, "Endpoint /healthz respondiendo 200 OK")
				if status.SaaSSyncOK {
					details = append(details, "Sincronización con el SaaS confirmada")
				}
				return &CommissionResult{
					Outcome:     CommissionOutcomeSuccess,
					Status:      status,
					SafeMessage: "GEO CAM Edge activado y funcionando correctamente.",
					Details:     details,
				}, nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	finalStatus, _ := s.GetCommissioningStatus(ctx)
	if finalStatus.DaemonRunning {
		return &CommissionResult{
			Outcome:     CommissionOutcomeSuccess,
			Status:      finalStatus,
			SafeMessage: "El daemon está activo en el puerto 8091. Sincronización en curso.",
			Details:     details,
		}, nil
	}

	return &CommissionResult{
		Outcome:     CommissionOutcomeFailed,
		Status:      finalStatus,
		SafeMessage: "El servicio fue iniciado pero el daemon no respondió en el tiempo esperado.",
		Details:     details,
	}, nil
}

func (s *Service) probeHTTP(ctx context.Context, path string) int {
	url := "http://" + s.HealthAddr + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

type fullStatusSnapshot struct {
	Status         string         `json:"status"`
	ProcessingMode string         `json:"processing_mode"`
	Profile        config.Profile `json:"profile"`
	Heartbeat      *struct {
		State         string    `json:"state"`
		LastSuccessAt time.Time `json:"last_success_at"`
	} `json:"heartbeat"`
	Vision *struct {
		Worker *struct {
			State string `json:"state"`
		} `json:"worker"`
	} `json:"vision"`
	Operational struct {
		State string `json:"state"`
	} `json:"operational_readiness"`
	Cameras []interface{} `json:"cameras"`
}

func (s *Service) fetchFullStatus(ctx context.Context) (snap fullStatusSnapshot, ok bool) {
	url := "http://" + s.HealthAddr + "/status"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return snap, false
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return snap, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return snap, false
	}
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return snap, false
	}
	return snap, true
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
