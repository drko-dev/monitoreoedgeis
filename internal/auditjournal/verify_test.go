package auditjournal

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// buildJournal appends n valid records and returns the raw lines plus the
// on-disk path, so tests can tamper with a byte-identical copy.
func buildJournal(t *testing.T, n int) (lines [][]byte, path string) {
	t.Helper()
	j, path := newTestJournal(t)
	for i := 0; i < n; i++ {
		if _, err := j.Append(Record{EventType: EventControlCommandReceived, Result: ResultSuccess, EdgeID: "edge-1"}); err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, l := range bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n")) {
		cp := make([]byte, len(l))
		copy(cp, l)
		lines = append(lines, cp)
	}
	return lines, path
}

func writeTampered(t *testing.T, lines [][]byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tampered.jsonl")
	var buf bytes.Buffer
	for _, l := range lines {
		buf.Write(l)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestVerify_DetectsModifiedRecord(t *testing.T) {
	lines, _ := buildJournal(t, 3)
	var rec Record
	if err := json.Unmarshal(lines[1], &rec); err != nil {
		t.Fatal(err)
	}
	rec.EdgeID = "edge-attacker-modified" // record_hash no longer matches
	tampered, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	lines[1] = tampered

	result, err := VerifyFile(writeTampered(t, lines))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusCorrupt {
		t.Fatalf("status = %s, want CORRUPT", result.Status)
	}
}

func TestVerify_DetectsDeletedMiddleRecord(t *testing.T) {
	lines, _ := buildJournal(t, 5)
	lines = append(lines[:2], lines[3:]...) // delete record #3 (index 2)

	result, err := VerifyFile(writeTampered(t, lines))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusCorrupt {
		t.Fatalf("status = %s, want CORRUPT", result.Status)
	}
}

func TestVerify_DetectsReorderedRecords(t *testing.T) {
	lines, _ := buildJournal(t, 4)
	lines[1], lines[2] = lines[2], lines[1]

	result, err := VerifyFile(writeTampered(t, lines))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusCorrupt {
		t.Fatalf("status = %s, want CORRUPT", result.Status)
	}
}

func TestVerify_DetectsBadPrevHash(t *testing.T) {
	lines, _ := buildJournal(t, 3)
	var rec Record
	if err := json.Unmarshal(lines[2], &rec); err != nil {
		t.Fatal(err)
	}
	rec.PrevHash = "0000000000000000000000000000000000000000000000000000000000aa"
	tampered, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	lines[2] = tampered

	result, err := VerifyFile(writeTampered(t, lines))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusCorrupt {
		t.Fatalf("status = %s, want CORRUPT", result.Status)
	}
}

func TestVerify_DetectsBadRecordHash(t *testing.T) {
	lines, _ := buildJournal(t, 2)
	var rec Record
	if err := json.Unmarshal(lines[0], &rec); err != nil {
		t.Fatal(err)
	}
	rec.RecordHash = "0000000000000000000000000000000000000000000000000000000000bb"
	tampered, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	lines[0] = tampered

	result, err := VerifyFile(writeTampered(t, lines))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusCorrupt {
		t.Fatalf("status = %s, want CORRUPT", result.Status)
	}
}

func TestVerify_DetectsMalformedJSON_NotAtEOF(t *testing.T) {
	lines, _ := buildJournal(t, 3)
	lines[1] = []byte("{not valid json")

	result, err := VerifyFile(writeTampered(t, lines))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusCorrupt {
		t.Fatalf("status = %s, want CORRUPT (malformed JSON mid-file is not a truncation)", result.Status)
	}
}

func TestVerify_DuplicateSequenceDetected(t *testing.T) {
	lines, _ := buildJournal(t, 3)
	var rec Record
	if err := json.Unmarshal(lines[2], &rec); err != nil {
		t.Fatal(err)
	}
	rec.Sequence = 2 // duplicate of the previous record's sequence
	// Recompute the hash so this failure is attributable to the sequence
	// check alone, not an incidental record_hash mismatch.
	prevHash, _ := recordHashFor(t, lines[1])
	rec.RecordHash, _ = computeRecordHash(prevHash, rec)
	tampered, _ := json.Marshal(rec)
	lines[2] = tampered

	result, err := VerifyFile(writeTampered(t, lines))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusCorrupt {
		t.Fatalf("status = %s, want CORRUPT", result.Status)
	}
}

func recordHashFor(t *testing.T, line []byte) (string, error) {
	t.Helper()
	var rec Record
	if err := json.Unmarshal(line, &rec); err != nil {
		return "", err
	}
	return rec.RecordHash, nil
}

func TestVerify_TruncatedTail_DistinctFromCorrupt(t *testing.T) {
	lines, _ := buildJournal(t, 3)
	dir := t.TempDir()
	path := filepath.Join(dir, "truncated.jsonl")
	var buf bytes.Buffer
	for _, l := range lines[:2] {
		buf.Write(l)
		buf.WriteByte('\n')
	}
	// Final record written with no trailing newline and cut mid-object —
	// simulates a crash mid-append, not tampering.
	partial, _ := json.Marshal(struct {
		SchemaVersion int `json:"schema_version"`
	}{SchemaVersion: 1})
	buf.Write(partial[:len(partial)/2])
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := VerifyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusTruncatedTail {
		t.Fatalf("status = %s, want TRUNCATED_LAST_WRITE", result.Status)
	}
	if result.RecordCount != 2 {
		t.Fatalf("record count = %d, want 2 (valid prefix preserved)", result.RecordCount)
	}
}

func TestVerify_EmptyJournalIsValid(t *testing.T) {
	result, err := VerifyFile(filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusEmpty {
		t.Fatalf("status = %s, want EMPTY", result.Status)
	}
}

func TestVerify_ValidJournalPasses(t *testing.T) {
	_, path := buildJournal(t, 10)
	result, err := VerifyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusPass {
		t.Fatalf("status = %s, want PASS: %s", result.Status, result.FailureReason)
	}
}
