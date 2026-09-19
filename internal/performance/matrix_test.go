package performance

import (
	"encoding/json"
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestMeasuredRowRequiresTraceableEvidence(t *testing.T) {
	row := CapacityRow{
		Status:      StatusMeasured,
		HardwareID:  "runner-amd64",
		CameraCount: ptr(5),
		DurationSec: ptr(3.0),
		Evidence: RunEvidence{
			Date:       "2026-09-19T20:00:00Z",
			CommitSHA:  "abc123",
			HardwareID: "runner-amd64",
			OS:         "linux",
			Arch:       "amd64",
			Command:    "GEOCAM_PERF=1 go test ./internal/perf",
		},
	}
	if err := row.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	data, err := row.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var round CapacityRow
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if round.Status != StatusMeasured || round.CameraCount == nil || *round.CameraCount != 5 {
		t.Fatalf("unexpected round-trip: %#v", round)
	}
}

func TestNotValidatedRejectsNumericMetrics(t *testing.T) {
	row := CapacityRow{
		Status:      StatusNotValidated,
		HardwareID:  "raspberry-pi-5",
		CameraCount: ptr(10),
		Notes:       []string{"hardware not tested"},
	}
	err := row.Validate()
	if err == nil || !strings.Contains(err.Error(), "cannot contain numeric") {
		t.Fatalf("expected numeric-metric rejection, got %v", err)
	}
}

func TestNotValidatedAllowsDescriptiveHardwareOnly(t *testing.T) {
	row := CapacityRow{
		Status:       StatusNotValidated,
		HardwareID:   "candidate-arm64-sbc",
		Architecture: "arm64",
		Notes:        []string{"physical hardware NOT VALIDATED"},
	}
	if err := row.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestDerivedRequiresFormulaAndSourceCommit(t *testing.T) {
	row := CapacityRow{Status: StatusDerived}
	if err := row.Validate(); err == nil {
		t.Fatal("expected missing derivation error")
	}

	row.Derivation = "network_bytes / duration_seconds"
	if err := row.Validate(); err == nil {
		t.Fatal("expected missing source commit error")
	}

	row.Evidence.CommitSHA = "abc123"
	if err := row.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
