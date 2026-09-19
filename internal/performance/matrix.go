package performance

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// EvidenceStatus states what a capacity row actually proves.
type EvidenceStatus string

const (
	StatusMeasured     EvidenceStatus = "MEASURED"
	StatusDerived      EvidenceStatus = "DERIVED"
	StatusNotValidated EvidenceStatus = "NOT_VALIDATED"
)

// RunEvidence makes every measured/derived row traceable to a reproducible run.
type RunEvidence struct {
	Date       string            `json:"date,omitempty"`
	CommitSHA  string            `json:"commit_sha,omitempty"`
	HardwareID string            `json:"hardware_id,omitempty"`
	OS         string            `json:"os,omitempty"`
	Arch       string            `json:"arch,omitempty"`
	Config     map[string]string `json:"config,omitempty"`
	Input      string            `json:"input,omitempty"`
	Command    string            `json:"command,omitempty"`
	ResultPath string            `json:"result_path,omitempty"`
}

// CapacityRow is the versionable evidence format used to consolidate Hito X.
// Numeric fields are pointers so an unavailable/not-run value is omitted rather
// than silently serialized as a real zero measurement.
type CapacityRow struct {
	Status EvidenceStatus `json:"status"`

	HardwareID   string `json:"hardware_id,omitempty"`
	Architecture string `json:"architecture,omitempty"`
	CPU          string `json:"cpu,omitempty"`
	Cores        *int   `json:"cores,omitempty"`
	RAMBytes     *int64 `json:"ram_bytes,omitempty"`
	GPU          string `json:"gpu,omitempty"`
	Mode         string `json:"mode,omitempty"`
	Resolution   string `json:"resolution,omitempty"`

	InputFPS      *float64 `json:"input_fps,omitempty"`
	CameraCount   *int     `json:"camera_count,omitempty"`
	DecodeFPS     *float64 `json:"decode_fps,omitempty"`
	InferenceFPS  *float64 `json:"inference_fps,omitempty"`
	CPUPercent    *float64 `json:"cpu_percent,omitempty"`
	RAMUsageBytes *int64   `json:"ram_usage_bytes,omitempty"`
	NetworkBPS    *float64 `json:"network_bytes_per_second,omitempty"`
	DurationSec   *float64 `json:"duration_seconds,omitempty"`

	Derivation string      `json:"derivation,omitempty"`
	Notes      []string    `json:"notes,omitempty"`
	Evidence   RunEvidence `json:"evidence,omitempty"`
}

// Validate rejects rows that would blur measured, derived and unvalidated
// evidence. In particular NOT_VALIDATED rows cannot carry numeric benchmark
// values that look measured.
func (r CapacityRow) Validate() error {
	switch r.Status {
	case StatusMeasured:
		if err := r.validateMeasuredEvidence(); err != nil {
			return err
		}
	case StatusDerived:
		if strings.TrimSpace(r.Derivation) == "" {
			return errors.New("performance: DERIVED row requires derivation")
		}
		if strings.TrimSpace(r.Evidence.CommitSHA) == "" {
			return errors.New("performance: DERIVED row requires source commit_sha")
		}
	case StatusNotValidated:
		if r.hasNumericMetrics() {
			return errors.New("performance: NOT_VALIDATED row cannot contain numeric benchmark metrics")
		}
	default:
		return fmt.Errorf("performance: invalid status %q", r.Status)
	}
	return nil
}

func (r CapacityRow) validateMeasuredEvidence() error {
	if strings.TrimSpace(r.Evidence.Date) == "" ||
		strings.TrimSpace(r.Evidence.CommitSHA) == "" ||
		strings.TrimSpace(r.Evidence.HardwareID) == "" ||
		strings.TrimSpace(r.Evidence.OS) == "" ||
		strings.TrimSpace(r.Evidence.Arch) == "" ||
		strings.TrimSpace(r.Evidence.Command) == "" {
		return errors.New("performance: MEASURED row requires date, commit_sha, hardware_id, os, arch and command evidence")
	}
	if r.DurationSec == nil || *r.DurationSec <= 0 {
		return errors.New("performance: MEASURED row requires positive duration_seconds")
	}
	return nil
}

func (r CapacityRow) hasNumericMetrics() bool {
	return r.Cores != nil ||
		r.RAMBytes != nil ||
		r.InputFPS != nil ||
		r.CameraCount != nil ||
		r.DecodeFPS != nil ||
		r.InferenceFPS != nil ||
		r.CPUPercent != nil ||
		r.RAMUsageBytes != nil ||
		r.NetworkBPS != nil ||
		r.DurationSec != nil
}

// Marshal validates before producing JSON so invalid evidence cannot be emitted
// accidentally by reporting tooling.
func (r CapacityRow) Marshal() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(r, "", "  ")
}
