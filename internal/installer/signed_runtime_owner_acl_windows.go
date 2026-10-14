//go:build windows

package installer

func auditInstalledPackAccessACL(_ string) error {
	return ErrRuntimePackOwnershipNotSeparated
}
