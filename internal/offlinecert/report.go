// Package offlinecert provides test-only building blocks for offline runtime
// certification. It does not provision software or alter the host system.
package offlinecert

import (
	"encoding/json"
	"errors"
	"io"
	"sort"
)

const ReportSchemaVersion = "1.0.0"

type Status string

const (
	StatusPassed  Status = "passed"
	StatusFailed  Status = "failed"
	StatusPending Status = "pending"
	StatusSkipped Status = "skipped"
)

// Check is one certification assertion. Pending checks are explicit and must
// never be interpreted as passing evidence.
type Check struct {
	ID        string `json:"id"`
	Category  string `json:"category"`
	Status    Status `json:"status"`
	Evidence  string `json:"evidence,omitempty"`
	Blocker   string `json:"blocker,omitempty"`
	Guarantee string `json:"guarantee,omitempty"`
}

type Report struct {
	SchemaVersion string  `json:"schema_version"`
	Framework     string  `json:"framework"`
	BaseSHA       string  `json:"base_sha,omitempty"`
	Scope         string  `json:"scope"`
	Checks        []Check `json:"checks"`
}

func NewReport(scope string) *Report {
	return &Report{SchemaVersion: ReportSchemaVersion, Framework: "geocam-offline-cert", Scope: scope}
}

func (r *Report) Add(check Check) error {
	if r == nil || check.ID == "" || check.Category == "" {
		return errors.New("report and check id/category are required")
	}
	switch check.Status {
	case StatusPassed, StatusFailed, StatusPending, StatusSkipped:
	default:
		return errors.New("unsupported certification status")
	}
	for _, existing := range r.Checks {
		if existing.ID == check.ID {
			return errors.New("duplicate certification check id")
		}
	}
	r.Checks = append(r.Checks, check)
	sort.Slice(r.Checks, func(i, j int) bool { return r.Checks[i].ID < r.Checks[j].ID })
	return nil
}

// WriteJSON emits deterministic JSON: no wall-clock or machine-specific fields
// are included, and checks are sorted by ID before encoding.
func (r *Report) WriteJSON(w io.Writer) error {
	if r == nil || r.SchemaVersion == "" || r.Scope == "" {
		return errors.New("incomplete certification report")
	}
	checks := append([]Check(nil), r.Checks...)
	sort.Slice(checks, func(i, j int) bool { return checks[i].ID < checks[j].ID })
	copyReport := *r
	copyReport.Checks = checks
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(copyReport)
}
