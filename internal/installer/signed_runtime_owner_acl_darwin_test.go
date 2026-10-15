//go:build darwin && cgo

package installer

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Only disposable fixture ACLs are changed. No real installation, host
// account or runtime-lab filesystem ACL is ever modified by this test.
func TestSignedOwnerPolicyMacNativeACL(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "acl-only-file")
	if err := os.WriteFile(file, []byte("fixture"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := auditInstalledPackAccessACL(file); err != nil {
		t.Fatalf("ordinary file incorrectly rejected before fixture ACL grant: %v", err)
	}
	cmd := exec.Command("/bin/chmod", "+a", "everyone allow read,write", file)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("macOS fixture ACL creation unavailable: %v: %s", err, output)
	}
	if err := auditInstalledPackAccessACL(file); !errors.Is(err, ErrRuntimePackOwnershipNotSeparated) {
		t.Fatalf("extended macOS ACL bypassed strict ownership audit: %v", err)
	}
	// Mode bits were unchanged by granting extra ACL rights.
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o222 != 0 {
		t.Fatalf("fixture ACL test only meaningful with read-only POSIX bits: %04o", info.Mode().Perm())
	}
}

func TestSignedOwnerPolicyMacNativeACLRejectsUnsafePaths(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	if err := auditInstalledPackAccessACL(missing); !errors.Is(err, ErrRuntimePackOwnershipNotSeparated) {
		t.Fatalf("missing file admitted as ACL-absent: %v", err)
	}
	file := filepath.Join(root, "regular")
	if err := os.WriteFile(file, []byte("fixture"), 0o444); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "symlink")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if err := auditInstalledPackAccessACL(link); !errors.Is(err, ErrRuntimePackOwnershipNotSeparated) {
		t.Fatalf("ACL check followed symlink without refusal: %v", err)
	}
	subdir := filepath.Join(root, "folder")
	if err := os.Mkdir(subdir, 0o555); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("/bin/chmod", "+a", "everyone allow read,write", subdir).CombinedOutput(); err != nil {
		t.Skipf("macOS fixture directory ACL creation unavailable: %v: %s", err, output)
	}
	if err := auditInstalledPackAccessACL(subdir); !errors.Is(err, ErrRuntimePackOwnershipNotSeparated) {
		t.Fatalf("ACL-granted directory admitted: %v", err)
	}
}
