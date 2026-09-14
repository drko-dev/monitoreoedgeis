package credentials

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// credentialPrefix marks every credential this agent generates. Kept as a
// visible prefix (not a secret) so a leaked value is instantly recognizable
// in logs/incident reports as a GEO CAM Edge device credential.
const credentialPrefix = "edg_live_"

// GenerateCredential creates a new device credential locally. The SaaS never
// generates or sees this value in plaintext (zero-knowledge model) — only
// HashCredential's output is ever sent over the wire. 32 random bytes from
// crypto/rand gives the same entropy as Python's secrets.token_urlsafe(32).
func GenerateCredential() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("credentials: generate credential: %w", err)
	}
	return credentialPrefix + base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// HashCredential returns the lowercase hex SHA-256 digest of cred (including
// its prefix) — the device_key_hash sent to the SaaS in place of the
// credential itself.
func HashCredential(cred string) string {
	sum := sha256.Sum256([]byte(cred))
	return hex.EncodeToString(sum[:])
}
