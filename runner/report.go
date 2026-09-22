package runner

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/illumination-k/mutrim/mutator"
)

// Status is the outcome of one mutant.
type Status string

const (
	// Killed means at least one test failed with the mutant active.
	Killed Status = "KILLED"
	// Lived means every test that reaches the mutant passed with it active.
	Lived Status = "LIVED"
	// Timeout means the test binary exceeded the per-mutant timeout; it
	// counts as killed in the totals, since the original suite passed.
	Timeout Status = "TIMEOUT"
	// NoCoverage means no test reaches the mutant's site, so it was not
	// executed; it counts as surviving in the totals.
	NoCoverage Status = "NO_COVERAGE"
	// NotViable means the mutant was never executed: it did not
	// type-check or could not be embedded as schemata.
	NotViable Status = "NOT_VIABLE"
	// Skipped means a diff scoped the run (Options.InDiff) and the
	// mutant lies outside it, so it was never executed; like Ignored it
	// counts towards no score.
	Skipped Status = "SKIPPED"
	// Ignored means a gen filter or an inline //mutrim:disable directive
	// excluded the mutant, so it was never built or executed; like
	// NotViable it counts towards no score, but it says the suppression
	// was asked for.
	Ignored Status = "IGNORED"
)

// Executed reports whether the status came from running the tests, and
// so is worth copying forward from a previous report.
func (s Status) Executed() bool {
	return s == Killed || s == Lived || s == Timeout
}

// Test is one entry of the report's tests, a row of the kill matrix: a
// top-level test, or with Options.Subtests a subtest, run on its own
// against the unmutated package.
type Test struct {
	Name string `json:"name"`
	// Parent is the test that runs this one as a subtest (`TestX` for
	// `TestX/case`); empty for a top-level test.
	Parent     string `json:"parent,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	// Sites lists the IDs of the mutants whose site the test reaches; only
	// these can be killed by it.
	Sites []string `json:"sites"`
}

// Result is one entry of report.json.
type Result struct {
	MutantID string `json:"mutant_id"`
	Status   Status `json:"status"`
	// TestsRun counts the rows (see Test) that ran: those reaching the
	// mutant's site, fewer when a panic stopped the run early.
	TestsRun int `json:"tests_run"`
	// KilledBy lists the rows that failed, or on a timeout the ones that
	// never finished.
	KilledBy   []string `json:"killed_by,omitempty"`
	DurationMS int64    `json:"duration_ms"`
}

// Totals summarizes a report. Score is (killed + timeout) / (killed +
// timeout + lived + no_coverage), or zero when nothing is viable.
type Totals struct {
	Mutants    int     `json:"mutants"`
	Killed     int     `json:"killed"`
	Lived      int     `json:"lived"`
	Timeout    int     `json:"timeout"`
	NoCoverage int     `json:"no_coverage"`
	NotViable  int     `json:"not_viable"`
	Ignored    int     `json:"ignored"`
	Skipped    int     `json:"skipped"`
	Score      float64 `json:"score"`
}

// Report is the JSON written by Run.
type Report struct {
	// Pkg is the import path of the mutated package, taken from its
	// mutants; empty when there were none.
	Pkg        string   `json:"pkg"`
	BaselineMS int64    `json:"baseline_ms"`
	TimeoutMS  int64    `json:"timeout_ms"`
	Tests      []Test   `json:"tests"`
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
		case NoCoverage:
			t.NoCoverage++
		case NotViable:
			t.NotViable++
		case Ignored:
			t.Ignored++
		case Skipped:
			t.Skipped++
		}
	}
	if viable := t.Killed + t.Timeout + t.Lived + t.NoCoverage; viable > 0 {
		t.Score = float64(t.Killed+t.Timeout) / float64(viable)
	}
	r.Totals = t
}

// Spot is a function with surviving mutants: either no test reaches them
// or the tests that do cannot tell the difference.
type Spot struct {
	Func string `json:"func"`
	File string `json:"file"`
	// Line is the first mutated line of the function.
	Line       int `json:"line"`
	Killed     int `json:"killed"`
	Lived      int `json:"lived"`
	NoCoverage int `json:"no_coverage"`
}

// WeakSpots lists the functions of mutants that have a surviving mutant
// in reports, most survivors first. Mutants absent from every report are
// ignored, so shard reports can be passed together.
func WeakSpots(mutants []mutator.Mutant, reports ...*Report) []Spot {
	status := map[string]Status{}
	for _, r := range reports {
		for _, res := range r.Results {
			status[res.MutantID] = res.Status
		}
	}
	spots := map[string]*Spot{} // keyed by package and function, since reports may span packages
	for _, m := range mutants {
		key := m.Pkg + "." + m.Func
		s, ok := spots[key]
		if !ok {
			s = &Spot{Func: m.Func, File: m.File, Line: m.Line}
			spots[key] = s
		}
		s.Line = min(s.Line, m.Line)
		switch status[m.ID] {
		case Killed, Timeout:
			s.Killed++
		case Lived:
			s.Lived++
		case NoCoverage:
			s.NoCoverage++
		}
	}
	out := []Spot{}
	for _, s := range spots {
		if s.Lived+s.NoCoverage > 0 {
			out = append(out, *s)
		}
	}
	slices.SortFunc(out, func(a, b Spot) int {
		// Same-named functions of two packages are told apart by file.
		return cmp.Or(cmp.Compare(b.Lived+b.NoCoverage, a.Lived+a.NoCoverage), cmp.Compare(a.Func, b.Func), cmp.Compare(a.File, b.File))
	})
	return out
}
