package perf

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// HostInfo records the machine a measurement ran on. It exists so no number
// in a report can be read without its hardware context: a decode or inference
// FPS is meaningless without the CPU, the OS, and — for inference — which
// accelerator was actually available.
//
// Nothing here is inferred from a product name: every field is either read
// from the running kernel/toolchain or left empty.
type HostInfo struct {
	GOOS        string `json:"goos"`
	GOARCH      string `json:"goarch"`
	GoVersion   string `json:"go_version"`
	NumCPU      int    `json:"num_cpu"`
	CPUModel    string `json:"cpu_model,omitempty"`
	MemoryBytes uint64 `json:"memory_bytes,omitempty"`
	OSVersion   string `json:"os_version,omitempty"`

	FFmpegVersion      string `json:"ffmpeg_version,omitempty"`
	FFprobeVersion     string `json:"ffprobe_version,omitempty"`
	PythonVersion      string `json:"python_version,omitempty"`
	TorchVersion       string `json:"torch_version,omitempty"`
	UltralyticsVersion string `json:"ultralytics_version,omitempty"`

	// CUDAReported / MPSReported are what the installed PyTorch build itself
	// reports, not what any documentation claims: "available", "unavailable",
	// or "unknown" when torch could not be queried at all.
	CUDAReported string `json:"torch_cuda_reported"`
	MPSReported  string `json:"torch_mps_reported"`
	NvidiaSMI    string `json:"nvidia_smi,omitempty"`

	Notes []string `json:"notes,omitempty"`
}

type torchProbe struct {
	Torch       string `json:"torch"`
	Ultralytics string `json:"ultralytics"`
	CUDA        *bool  `json:"cuda"`
	MPS         *bool  `json:"mps"`
	CUDAName    string `json:"cuda_name"`
	DeviceCount int    `json:"device_count"`
	Err         string `json:"err"`
}

const torchProbeScript = `import json
out = {"torch": "", "ultralytics": "", "cuda": None, "mps": None, "cuda_name": "", "device_count": 0, "err": ""}
try:
    import torch
    out["torch"] = torch.__version__
    out["cuda"] = bool(torch.cuda.is_available())
    out["device_count"] = int(torch.cuda.device_count())
    if out["cuda"]:
        out["cuda_name"] = torch.cuda.get_device_name(0)
    try:
        out["mps"] = bool(torch.backends.mps.is_available())
    except Exception:
        out["mps"] = False
except Exception as exc:
    out["err"] = str(exc)
try:
    import ultralytics
    out["ultralytics"] = ultralytics.__version__
except Exception as exc:
    if not out["err"]:
        out["err"] = str(exc)
print(json.dumps(out))
`

// ProbeHost gathers host facts. pythonPath may be empty, in which case the
// PyTorch/Ultralytics block is reported as unknown rather than guessed.
func ProbeHost(ctx context.Context, pythonPath string) HostInfo {
	h := HostInfo{
		GOOS:         runtime.GOOS,
		GOARCH:       runtime.GOARCH,
		GoVersion:    runtime.Version(),
		NumCPU:       runtime.NumCPU(),
		CUDAReported: "unknown",
		MPSReported:  "unknown",
	}
	h.CPUModel, h.MemoryBytes = readCPUAndMemory(ctx)
	h.OSVersion = readOSVersion(ctx)
	h.FFmpegVersion = firstLine(commandOutput(ctx, "ffmpeg", "-hide_banner", "-version"))
	h.FFprobeVersion = firstLine(commandOutput(ctx, "ffprobe", "-hide_banner", "-version"))
	if out := commandOutput(ctx, "nvidia-smi", "-L"); len(out) > 0 {
		h.NvidiaSMI = strings.TrimSpace(string(out))
	}

	if pythonPath != "" {
		h.PythonVersion = firstLine(commandOutput(ctx, pythonPath, "--version"))
		if out, err := commandOutputErr(ctx, pythonPath, "-c", torchProbeScript); err == nil {
			var p torchProbe
			if json.Unmarshal(lastLine(out), &p) == nil {
				h.TorchVersion = p.Torch
				h.UltralyticsVersion = p.Ultralytics
				h.CUDAReported = boolLabel(p.CUDA)
				h.MPSReported = boolLabel(p.MPS)
				if p.CUDAName != "" {
					h.Notes = append(h.Notes, "torch cuda device 0: "+p.CUDAName)
				}
				if p.Err != "" {
					h.Notes = append(h.Notes, "torch probe note: "+p.Err)
				}
			}
		} else {
			h.Notes = append(h.Notes, "python interpreter at "+pythonPath+" could not be queried for torch/ultralytics")
		}
	}
	return h
}

func boolLabel(v *bool) string {
	switch {
	case v == nil:
		return "unknown"
	case *v:
		return "available"
	default:
		return "unavailable"
	}
}

// readCPUAndMemory reads the CPU model and total memory from the OS. Both are
// best-effort: an unavailable value is left empty rather than filled with a
// plausible-looking guess.
func readCPUAndMemory(ctx context.Context) (string, uint64) {
	switch runtime.GOOS {
	case "darwin":
		model := strings.TrimSpace(string(commandOutput(ctx, "sysctl", "-n", "machdep.cpu.brand_string")))
		var mem uint64
		if out := strings.TrimSpace(string(commandOutput(ctx, "sysctl", "-n", "hw.memsize"))); out != "" {
			mem, _ = strconv.ParseUint(out, 10, 64)
		}
		return model, mem
	case "linux":
		var model string
		var mem uint64
		if f, err := os.Open("/proc/cpuinfo"); err == nil {
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				line := sc.Text()
				if strings.HasPrefix(line, "model name") || strings.HasPrefix(line, "Model") {
					if i := strings.Index(line, ":"); i >= 0 {
						model = strings.TrimSpace(line[i+1:])
						break
					}
				}
			}
			f.Close()
		}
		if f, err := os.Open("/proc/meminfo"); err == nil {
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				fields := strings.Fields(sc.Text())
				if len(fields) >= 2 && fields[0] == "MemTotal:" {
					if kb, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
						mem = kb * 1024
					}
					break
				}
			}
			f.Close()
		}
		return model, mem
	default:
		return "", 0
	}
}

func readOSVersion(ctx context.Context) string {
	switch runtime.GOOS {
	case "darwin":
		name := strings.TrimSpace(string(commandOutput(ctx, "sw_vers", "-productName")))
		version := strings.TrimSpace(string(commandOutput(ctx, "sw_vers", "-productVersion")))
		build := strings.TrimSpace(string(commandOutput(ctx, "sw_vers", "-buildVersion")))
		return strings.TrimSpace(strings.Join(nonEmpty(name, version, build), " "))
	case "linux":
		if b, err := os.ReadFile("/etc/os-release"); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "PRETTY_NAME=") {
					return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
				}
			}
		}
		return ""
	default:
		return ""
	}
}

func nonEmpty(parts ...string) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func lastLine(b []byte) []byte {
	trimmed := strings.TrimSpace(string(b))
	if i := strings.LastIndexByte(trimmed, '\n'); i >= 0 {
		return []byte(trimmed[i+1:])
	}
	return []byte(trimmed)
}

// LookPath is a small helper so benchmarks can report which interpreter they
// resolved instead of silently falling back.
func LookPath(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}
