//go:build windows

package service

import (
	"errors"
	"testing"
)

func TestWindowsNativeInstallFailsClosed(t *testing.T) {
	if err := serviceCommand("install", Options{}); !errors.Is(err, ErrWindowsServiceInstallUnsupported) {
		t.Fatalf("serviceCommand(install) error=%v, want ErrWindowsServiceInstallUnsupported", err)
	}
}
