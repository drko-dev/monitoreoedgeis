//go:build !windows

package installer

import "os"

// currentPrivilegeLevel returns the current process privilege tier on POSIX systems.
func currentPrivilegeLevel() PrivilegeLevel {
	if os.Geteuid() == 0 {
		return PrivilegeRoot
	}
	return PrivilegeStandardUser
}
