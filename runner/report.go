package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Status is the outcome of one mutant.
type Status string

const (
	// Killed means at least one test failed with the mutant active.
	Killed Status = "KILLED"
	// Lived means every test passed with the mutant active.
	Lived Status = "LIVED"
	// Timeout means the test binary exceeded the per-mutant timeout; it
	// counts as killed in the totals, since the original suite passed.
	Timeout Status = "TIMEOUT"
	// NotViable means the mutant was never executed: it did not
	// type-check or could not be embedded as schemata.
	NotViable Status = "NOT_VIABLE"
)

// Result is one entry of report.json.
type Result struct {
	MutantID string `json:"mutant_id"`
	Status   Status `json:"status"`
	// TestsRun counts the top-level tests that ran (fewer than the whole
	// suite when a failure stopped the run early).
	TestsRun int `json:"tests_run"`
	// KilledBy lists the top-level tests that failed. The run stops at the
	// first failure, so this is not necessarily every test that would kill
	// the mutant.
	KilledBy   []string `json:"killed_by,omitempty"`
	DurationMS int64    `json:"duration_ms"`
}

// Totals summarizes a report. Score is (killed + timeout) / executed, or
// zero when nothing was executed.
type Totals struct {
	Mutants   int     `json:"mutants"`
	Killed    int     `json:"killed"`
	Lived     int     `json:"lived"`
	Timeout   int     `json:"timeout"`
	NotViable int     `json:"not_viable"`
	Score     float64 `json:"score"`
}

// Report is the JSON written by Run.
type Report struct {
	BaselineMS int64    `json:"baseline_ms"`
	TimeoutMS  int64    `json:"timeout_ms"`
	Results    []Result `json:"results"`
	Totals     Totals   `json:"totals"`
}

// ReadReport loads a report written by an earlier run.
func ReadReport(path string) (*Report, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("runner: parse %s: %w", path, err)
	}
	return &r, nil
}

func (r *Report) total() {
	var t Totals
	for _, res := range r.Results {
		t.Mutants++
		switch res.Status {
		case Killed:
			t.Killed++
		case Lived:
			t.Lived++
		case Timeout:
			t.Timeout++
		case NotViable:
			t.NotViable++
		}
	}
	if executed := t.Killed + t.Timeout + t.Lived; executed > 0 {
		t.Score = float64(t.Killed+t.Timeout) / float64(executed)
	}
	r.Totals = t
}
