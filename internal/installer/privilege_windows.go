//go:build windows

package installer

import "golang.org/x/sys/windows"

// currentPrivilegeLevel returns the current process privilege tier on Windows.
func currentPrivilegeLevel() PrivilegeLevel {
	var token windows.Token
	currentProcess := windows.CurrentProcess()
	err := windows.OpenProcessToken(currentProcess, windows.TOKEN_QUERY, &token)
	if err != nil {
		return PrivilegeUnknown
	}
	defer token.Close()

	if token.IsElevated() {
		return PrivilegeAdministrator
	}
	return PrivilegeStandardUser
}
