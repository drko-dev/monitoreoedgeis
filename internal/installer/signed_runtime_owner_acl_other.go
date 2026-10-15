//go:build !darwin && !linux && !windows

package installer

func auditInstalledPackAccessACL(_ string) error {
	return ErrRuntimePackOwnershipNotSeparated
}
