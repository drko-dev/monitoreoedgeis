package fulledge

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
)

// DeviceMode defines the hardware accelerator target for inference.
type DeviceMode string

const (
	DeviceCPU  DeviceMode = "cpu"
	DeviceCUDA DeviceMode = "cuda"
	DeviceAuto DeviceMode = "auto"
)

// ParseDeviceMode validates and returns a DeviceMode.
func ParseDeviceMode(raw string) (DeviceMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(DeviceCPU):
		return DeviceCPU, nil
	case string(DeviceCUDA):
		return DeviceCUDA, nil
	case string(DeviceAuto):
		return DeviceAuto, nil
	default:
		return "", fmt.Errorf("%w: %q (must be cpu, cuda, or auto)", ErrInvalidDevice, raw)
	}
}

// NPUStatusMessage is the explicit and honest status string required by Milestone K5.
const NPUStatusMessage = "NPU ADAPTER/CAPABILITY READY, BACKEND REAL PENDING"

// NPUCapability describes local NPU runtime capability honestly without fabricating support.
type NPUCapability struct {
	AdapterReady  bool   `json:"adapter_ready"`
	BackendActive bool   `json:"backend_active"`
	Status        string `json:"status"`
}

// HardwareStatus captures hardware acceleration capability and runtime device resolution.
type HardwareStatus struct {
	ConfiguredDevice  string        `json:"configured_device"`
	CurrentDevice     string        `json:"current_device"`
	CUDAAvailable     bool          `json:"cuda_available"`
	CUDAFallbackCount int64         `json:"cuda_fallback_count"`
	NPU               NPUCapability `json:"npu"`
}

// HardwareDetector discovers real GPU/accelerator hardware.
type HardwareDetector interface {
	DetectCUDA() bool
}

// DefaultHardwareDetector checks for CUDA device nodes and nvidia-smi tool without cgo.
type DefaultHardwareDetector struct{}

// DetectCUDA reports whether genuine CUDA hardware is accessible on this host.
func (d DefaultHardwareDetector) DetectCUDA() bool {
	// 1. Check for standard Linux NVIDIA GPU device nodes
	if _, err := os.Stat("/dev/nvidia0"); err == nil {
		return true
	}
	if _, err := os.Stat("/dev/nvhost-ctrl"); err == nil {
		return true
	}

	// 2. Check for nvidia-smi in PATH and execute a fast query if present
	if smiPath, err := exec.LookPath("nvidia-smi"); err == nil && smiPath != "" {
		cmd := exec.Command("nvidia-smi", "-L")
		if out, err := cmd.Output(); err == nil && len(out) > 0 {
			return true
		}
	}

	return false
}

// HardwareManager orchestrates honest device selection, fallback, and status reporting.
type HardwareManager struct {
	configuredDevice DeviceMode
	detector         HardwareDetector
	cudaAvailable    bool
	currentDevice    string
	fallbackCount    atomic.Int64
	logger           *slog.Logger
}

// NewHardwareManager creates a HardwareManager and resolves the active device.
func NewHardwareManager(configured DeviceMode, detector HardwareDetector, logger *slog.Logger) *HardwareManager {
	if detector == nil {
		detector = DefaultHardwareDetector{}
	}
	cudaAvail := detector.DetectCUDA()
	mgr := &HardwareManager{
		configuredDevice: configured,
		detector:         detector,
		cudaAvailable:    cudaAvail,
		logger:           logger,
	}
	mgr.resolve()
	return mgr
}

func (m *HardwareManager) resolve() {
	switch m.configuredDevice {
	case DeviceCUDA:
		if m.cudaAvailable {
			m.currentDevice = string(DeviceCUDA)
		} else {
			// Controlled fallback to CPU when CUDA is missing
			m.currentDevice = string(DeviceCPU)
			m.fallbackCount.Add(1)
			if m.logger != nil {
				m.logger.Warn("CUDA requested but not available; falling back to CPU",
					slog.String("configured", string(m.configuredDevice)),
					slog.String("fallback", m.currentDevice))
			}
		}
	case DeviceCPU:
		m.currentDevice = string(DeviceCPU)
	case DeviceAuto:
		fallthrough
	default:
		if m.cudaAvailable {
			m.currentDevice = string(DeviceCUDA)
		} else {
			m.currentDevice = string(DeviceCPU)
		}
	}
}

// CurrentDevice returns the currently resolved inference device ("cpu" or "cuda").
func (m *HardwareManager) CurrentDevice() string {
	return m.currentDevice
}

// FallbackCount returns how many times CUDA fell back to CPU.
func (m *HardwareManager) FallbackCount() int64 {
	return m.fallbackCount.Load()
}

// Status returns a point-in-time snapshot of hardware capabilities and device selection.
func (m *HardwareManager) Status() HardwareStatus {
	return HardwareStatus{
		ConfiguredDevice:  string(m.configuredDevice),
		CurrentDevice:     m.currentDevice,
		CUDAAvailable:     m.cudaAvailable,
		CUDAFallbackCount: m.fallbackCount.Load(),
		NPU: NPUCapability{
			AdapterReady:  true,
			BackendActive: false,
			Status:        NPUStatusMessage,
		},
	}
}
