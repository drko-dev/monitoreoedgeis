package identity

import (
	"crypto/rand"
	"fmt"
	"regexp"
)

// uuidPattern accepts the canonical RFC 4122 textual form (8-4-4-4-12 hex,
// version nibble 1-5, variant nibble 8/9/a/b). It is intentionally not
// limited to version 4 only, in case a future path ever validates a UUID
// this package did not generate itself.
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// newUUIDv4 generates a random RFC 4122 version-4 UUID using crypto/rand.
func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("identity: generate uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// validUUID reports whether s is a well-formed RFC 4122 UUID string.
func validUUID(s string) bool { return uuidPattern.MatchString(s) }
