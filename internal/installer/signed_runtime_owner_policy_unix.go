//go:build darwin || linux

package installer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func auditInstalledPackOwnerBoundary(ctx context.Context, root string, report SignedPackOwnershipAudit) (SignedPackOwnershipAudit, error) {
	euid := os.Geteuid()
	// Root can rewrite any ordinary inode despite Unix mode bits. Do not
	// certify ownership separation when running a privileged Worker.
	if euid == 0 {
		return report, fmt.Errorf("%w: privileged effective UID 0 cannot be isolated by file ownership", ErrRuntimePackOwnershipNotSeparated)
	}
	report.ProcessUID = uint32(euid)
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return report, err
	}
	st, ok := rootInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return report, fmt.Errorf("%w: missing native owner metadata", ErrRuntimePackOwnershipNotSeparated)
	}
	report.PackOwnerUID = st.Uid
	// Parent traversal verifies that a process cannot rename/swap an entire
	// trusted tree through a same-UID or group/world-writable ancestor.
	for parent := filepath.Dir(root); ; parent = filepath.Dir(parent) {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		info, err := os.Lstat(parent)
		if err != nil {
			return report, err
		}
		attrs, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return report, ErrRuntimePackOwnershipNotSeparated
		}
		if err := evaluateSignedPackOwnerAttrs(parent, attrs.Uid, report.ProcessUID,
			report.PackOwnerUID, info.Mode(), false); err != nil {
			return report, err
		}
		if err := auditInstalledPackAccessACL(parent); err != nil {
			return report, err
		}
		report.Ancestors++
		if parent == string(filepath.Separator) {
			break
		}
	}
	err = filepath.WalkDir(root, func(path string, _ os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		attrs, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return ErrRuntimePackOwnershipNotSeparated
		}
		if err := evaluateSignedPackOwnerAttrs(path, attrs.Uid, report.ProcessUID,
			report.PackOwnerUID, info.Mode(), true); err != nil {
			return err
		}
		if err := auditInstalledPackAccessACL(path); err != nil {
			return err
		}
		report.Entries++
		return nil
	})
	return report, err
}
