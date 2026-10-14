package installer

import (
	"errors"
	"os"
	"testing"
)

func TestSignedOwnerPolicySyntheticBoundaryCases(t *testing.T) {
	const workerUID uint32 = 501
	const packOwnerUID uint32 = 0
	for _, tc := range []struct {
		name       string
		owner      uint32
		mode       os.FileMode
		inPack     bool
		wantReject bool
	}{
		{"independently-owned read-only executable", packOwnerUID, 0o555, true, false},
		{"independently-owned read-only directory", packOwnerUID, os.ModeDir | 0o555, true, false},
		{"independently-owned ancestor mode 0755", packOwnerUID, os.ModeDir | 0o755, false, false},
		{"same user owns signed binary", workerUID, 0o555, true, true},
		{"same user owns ancestor", workerUID, os.ModeDir | 0o755, false, true},
		{"different untrusted owner inside pack", 502, 0o444, true, true},
		{"group writable pack file", packOwnerUID, 0o664, true, true},
		{"owner writable pack file", packOwnerUID, 0o644, true, true},
		{"other writable pack directory", packOwnerUID, os.ModeDir | 0o557, true, true},
		{"group writable ancestor", packOwnerUID, os.ModeDir | 0o775, false, true},
		{"world writable sticky parent", packOwnerUID, os.ModeDir | os.ModeSticky | 0o777, false, true},
		{"symlink", packOwnerUID, os.ModeSymlink | 0o777, true, true},
		{"named pipe", packOwnerUID, os.ModeNamedPipe | 0o444, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := evaluateSignedPackOwnerAttrs("/synthetic/runtime", tc.owner, workerUID,
				packOwnerUID, tc.mode, tc.inPack)
			if tc.wantReject != errors.Is(err, ErrRuntimePackOwnershipNotSeparated) {
				t.Fatalf("ownership policy result %v, want rejection %t", err, tc.wantReject)
			}
		})
	}
}
