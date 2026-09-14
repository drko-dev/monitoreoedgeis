package platform

import (
	"runtime"
	"testing"
)

func TestDetectDoesNotPanic(t *testing.T) {
	info := Detect() // must not panic on any host, supported or not

	if info.Hostname == "" {
		t.Error("Hostname is empty")
	}
	if info.GOOS != runtime.GOOS {
		t.Errorf("GOOS = %q, want %q", info.GOOS, runtime.GOOS)
	}
	if info.GOARCH != runtime.GOARCH {
		t.Errorf("GOARCH = %q, want %q", info.GOARCH, runtime.GOARCH)
	}
	if info.CPUCount < 1 {
		t.Errorf("CPUCount = %d, want >= 1", info.CPUCount)
	}
	if info.OS == "" {
		t.Error("OS is empty")
	}
	if info.Kernel == "" {
		t.Errorf("Kernel is empty, want a value or %q", Unknown)
	}
}

func TestArchSupported(t *testing.T) {
	tests := map[string]bool{
		"amd64":   true,
		"arm64":   true,
		"386":     false,
		"riscv64": false,
		"":        false,
	}
	for arch, want := range tests {
		if got := ArchSupported(arch); got != want {
			t.Errorf("ArchSupported(%q) = %v, want %v", arch, got, want)
		}
	}
}
