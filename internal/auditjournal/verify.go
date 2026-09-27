package auditjournal

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Status is the outcome of walking a journal's hash chain.
type Status string

const (
	// StatusEmpty means the journal does not exist yet, or exists and has
	// zero records — a valid, initialized-but-empty journal.
	StatusEmpty Status = "EMPTY"
	// StatusPass means every record forms a valid, unbroken hash chain.
	StatusPass Status = "PASS"
	// StatusCorrupt means the chain is broken by a modified/deleted/
	// reordered record, or the file contains malformed JSON that is not
	// explainable as a truncated final write.
	StatusCorrupt Status = "CORRUPT"
	// StatusTruncatedTail means every record up to the last one forms a
	// valid chain, and only the FINAL line is incomplete/invalid JSON —
	// consistent with a crash mid-append rather than tampering. The valid
	// prefix is still trustworthy; the incomplete tail is simply discarded.
	StatusTruncatedTail Status = "TRUNCATED_LAST_WRITE"
)

// Result is the outcome of verifying a journal's full hash chain.
type VerifyResult struct {
	Status        Status
	RecordCount   uint64
	FirstSequence uint64
	LastSequence  uint64
	LastHash      string
	FailureReason string // set only when Status is CORRUPT or TRUNCATED_LAST_WRITE
}

// VerifyFile opens path and verifies its full hash chain from the start. A
// missing file is a valid empty journal, not an error.
func VerifyFile(path string) (VerifyResult, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return VerifyResult{Status: StatusEmpty, LastHash: GenesisHash}, nil
	}
	if err != nil {
		return VerifyResult{}, fmt.Errorf("auditjournal: open %s: %w", path, err)
	}
	defer f.Close()
	return verifyReader(f), nil
}

// verifyReader walks every record, checking JSON validity, strict sequence
// monotonicity (no gaps, no duplicates), and the SHA-256 hash chain.
//
// Lines are buffered in memory before verification (audit events are
// low-volume by design — see docs/security/audit.md's performance section)
// so the final line can be distinguished from an interior one, which is
// what lets TRUNCATED_LAST_WRITE be told apart from CORRUPT.
func verifyReader(r io.Reader) VerifyResult {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)

	var lines [][]byte
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		line := make([]byte, len(raw))
		copy(line, raw)
		lines = append(lines, line)
	}
	scanErr := scanner.Err()

	prevHash := GenesisHash
	var count, firstSeq, lastSeq uint64

	for i, line := range lines {
		isLast := i == len(lines)-1

		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			if isLast && scanErr == nil {
				return VerifyResult{
					Status: StatusTruncatedTail, RecordCount: count,
					FirstSequence: firstSeq, LastSequence: lastSeq, LastHash: prevHash,
					FailureReason: fmt.Sprintf("record %d: incomplete/invalid JSON at end of file (consistent with a crash mid-write)", i+1),
				}
			}
			return VerifyResult{
				Status: StatusCorrupt, RecordCount: count,
				FirstSequence: firstSeq, LastSequence: lastSeq, LastHash: prevHash,
				FailureReason: fmt.Sprintf("record %d: malformed JSON", i+1),
			}
		}

		wantSeq := lastSeq + 1
		if rec.Sequence != wantSeq {
			return VerifyResult{
				Status: StatusCorrupt, RecordCount: count,
				FirstSequence: firstSeq, LastSequence: lastSeq, LastHash: prevHash,
				FailureReason: fmt.Sprintf("record %d: sequence %d, expected %d (gap, duplicate, or reorder)", i+1, rec.Sequence, wantSeq),
			}
		}
		if rec.PrevHash != prevHash {
			return VerifyResult{
				Status: StatusCorrupt, RecordCount: count,
				FirstSequence: firstSeq, LastSequence: lastSeq, LastHash: prevHash,
				FailureReason: fmt.Sprintf("record %d: prev_hash mismatch (chain broken, record deleted, or reordered)", i+1),
			}
		}

		wantHash, err := computeRecordHash(prevHash, rec)
		if err != nil {
			return VerifyResult{
				Status: StatusCorrupt, RecordCount: count,
				FirstSequence: firstSeq, LastSequence: lastSeq, LastHash: prevHash,
				FailureReason: fmt.Sprintf("record %d: cannot canonicalize: %v", i+1, err),
			}
		}
		if wantHash != rec.RecordHash {
			return VerifyResult{
				Status: StatusCorrupt, RecordCount: count,
				FirstSequence: firstSeq, LastSequence: lastSeq, LastHash: prevHash,
				FailureReason: fmt.Sprintf("record %d: record_hash mismatch (record modified)", i+1),
			}
		}

		if count == 0 {
			firstSeq = rec.Sequence
		}
		lastSeq = rec.Sequence
		prevHash = rec.RecordHash
		count++
	}

	if scanErr != nil {
		return VerifyResult{
			Status: StatusCorrupt, RecordCount: count,
			FirstSequence: firstSeq, LastSequence: lastSeq, LastHash: prevHash,
			FailureReason: fmt.Sprintf("read error: %v", scanErr),
		}
	}
	if count == 0 {
		return VerifyResult{Status: StatusEmpty, LastHash: GenesisHash}
	}
	return VerifyResult{Status: StatusPass, RecordCount: count, FirstSequence: firstSeq, LastSequence: lastSeq, LastHash: prevHash}
}
