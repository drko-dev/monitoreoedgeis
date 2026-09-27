package auditjournal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// GenesisHash is the explicit previous-hash value for the very first record
// in a journal. It is never ambiguous with a real SHA-256 digest (always 64
// lowercase hex chars) because of its distinct shape, so a verifier can
// always distinguish "valid empty/initialized journal" from "corrupted:
// missing prev_hash".
const GenesisHash = "genesis:v1"

// computeRecordHash is the single source of truth for canonicalization and
// hashing, used identically by Append (to compute) and by verification (to
// recheck) — so the two can never drift apart.
//
// record_hash = SHA256(prev_hash || canonical_json(record without record_hash))
//
// This is a hash chain, not a signature: it proves the record has not been
// altered relative to the chain this process wrote, not who authored it.
func computeRecordHash(prevHash string, rec Record) (string, error) {
	rec.PrevHash = prevHash
	rec.RecordHash = ""
	payload, err := json.Marshal(rec)
	if err != nil {
		return "", fmt.Errorf("auditjournal: canonicalize record: %w", err)
	}
	h := sha256.New()
	h.Write([]byte(prevHash))
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil)), nil
}
