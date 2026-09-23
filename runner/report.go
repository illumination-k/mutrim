package runner

import (
	"cmp"
	"encoding/json"
	"errors"
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
	// RunError means the test binary died from outside the tests — the
	// runtime hit a fatal error, the process was killed by a signal, or
	// the binary exited with a code the testing package never uses — so
	// nothing the run observed says anything about the mutant. It is no
	// kill (nothing failed) and no survivor (nothing passed either): it
	// counts towards no score and is executed again on the next run.
	RunError Status = "RUN_ERROR"
	// NoCoverage means no test reaches the mutant's site, so it was not
	// executed; it counts as surviving in the totals.
	NoCoverage Status = "NO_COVERAGE"
	// NotViable means the mutant was never executed: it did not
	// type-check or could not be embedded as schemata.
	NotViable Status = "NOT_VIABLE"
	// Skipped means a diff scoped the run (Options.InDiff) or the run's
	// sample left the mutant out (Options.Sample), so it was never
	// executed; like Ignored it counts towards no score.
	Skipped Status = "SKIPPED"
	// Ignored means a gen filter or an inline //mutrim:disable directive
	// excluded the mutant, so it was never built or executed; like
	// NotViable it counts towards no score, but it says the suppression
	// was asked for.
	Ignored Status = "IGNORED"
	// Equivalent means a static rule proved the mutant computes what the
	// original does (mutator.Mutant.Equivalent), so it was never built
	// or executed; it counts towards no score.
	Equivalent Status = "EQUIVALENT"
	// SuspectEquivalent means every test reaching the mutant passed, as
	// for LIVED, and reached exactly the sites it reaches without the
	// mutant: the mutant changed neither the outcome nor the path taken,
	// which predicts an equivalent mutant (Schuler & Zeller, STVR 2013).
	// It counts towards no score unless Report.CountSuspect.
	SuspectEquivalent Status = "SUSPECT_EQUIVALENT"
)

// Executed reports whether the status came from running the tests, and
// so is worth copying forward from a previous report. A RUN_ERROR came
// from outside the tests, so it says nothing about the mutant: copying
// it forward would hide a mutant whose runs keep dying from
// infrastructure, so it is executed again instead.
func (s Status) Executed() bool {
	return s == Killed || s == Lived || s == Timeout || s == SuspectEquivalent
}

// Test is one entry of the report's tests, a row of the kill matrix: a
// top-level test, or with Options.Subtests a subtest, run on its own
// against the unmutated package.
type Test struct {
	// Name is the test's name, qualified as Pkg.TestX when it belongs to
	// another package than the mutated one (Options.ExtraTests).
	Name string `json:"name"`
	// Pkg is the import path of the package of an extra test binary; empty
	// for the mutated package's own tests.
	Pkg string `json:"pkg,omitempty"`
	// Parent is the test that runs this one as a subtest (`TestX` for
	// `TestX/case`), qualified like Name; empty for a top-level test.
	Parent string `json:"parent,omitempty"`
	// Hash is the SHA-256 of the source text of the test function (of
	// the parent for a subtest), read from Options.TestSrcs or
	// Binary.Srcs; empty without them. Options.Previous copies a result
	// forward only while the tests it was observed with keep their hash.
	Hash       string `json:"hash,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	// Sites lists the IDs of the mutants whose site the test reaches; only
	// these can be killed by it.
	Sites []string `json:"sites"`
	// Flaky says the test failed in some of the Options.ConfirmBaseline
	// runs against the unmutated package and passed in others. Its
	// failures are no kills (they land in Result.SuspiciousBy), and
	// minimize leaves it out of the cover entirely.
	Flaky bool `json:"flaky,omitempty"`
}

// Result is one entry of report.json.
type Result struct {
	MutantID string `json:"mutant_id"`
	Status   Status `json:"status"`
	// Class is the mutant's class (mutator.Mutant.Class), which
	// Totals.Classes groups the score by.
	Class string `json:"class,omitempty"`
	// TestsRun counts the rows (see Test) that ran: those reaching the
	// mutant's site, fewer when a panic stopped the run early.
	TestsRun int `json:"tests_run"`
	// KilledBy lists the rows that failed, or on a timeout the ones that
	// never finished, and whose failure Options.ConfirmKills reproduced.
	KilledBy []string `json:"killed_by,omitempty"`
	// SuspiciousBy lists the rows whose failure is not trusted: a kill a
	// rerun did not reproduce (mutmut's SUSPICIOUS), or any failure of a
	// row Test.Flaky marks. They are no kills, so a mutant only these
	// rows failed against is LIVED.
	SuspiciousBy []string `json:"suspicious_by,omitempty"`
	DurationMS   int64    `json:"duration_ms"`
	// TimeoutMS is the timeout each of the mutant's test processes ran
	// under, so a TIMEOUT can be audited; zero when nothing ran.
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
	// Changed says the mutant lies on a line the diff scoping the run
	// adds (Options.InDiff), as opposed to one only its tests reach.
	Changed bool `json:"changed,omitempty"`
}

// Totals summarizes a report. Score is (killed + timeout) / (killed +
// timeout + lived + no_coverage), or zero when nothing is viable; with
// Report.CountSuspect, suspect_equivalent joins the denominator.
// CoveredScore is the same without no_coverage (PIT's test strength): how
// well the tests check the code they reach. Coverage is the fraction of
// viable mutants a test reaches: covered / (covered + no_coverage), or
// zero when nothing is viable. It answers how much the tests reach at
// all; Score and CoveredScore answer how well the reached ones are
// checked (issue #34). With a sample (run -sample), the mutants it did
// not keep are Skipped and stay out of every score; Sampled records the
// fraction of the mutants it could select that it did select.
type Totals struct {
	Mutants int `json:"mutants"`
	Killed  int `json:"killed"`
	Lived   int `json:"lived"`
	Timeout int `json:"timeout"`
	// RunError counts the mutants whose run died from infrastructure: no
	// test failed, so nothing the run observed says anything about them.
	// They count towards no score and are never copied forward.
	RunError   int `json:"run_error"`
	NoCoverage int `json:"no_coverage"`
	NotViable  int `json:"not_viable"`
	Ignored    int `json:"ignored"`
	Skipped    int `json:"skipped"`
	Equivalent int `json:"equivalent"`
	// SuspectEquivalent counts the survivors whose trace the mutant left
	// unchanged; see SuspectEquivalent.
	SuspectEquivalent int `json:"suspect_equivalent"`
	// Suspicious counts the mutants with at least one unconfirmed kill
	// (Result.SuspiciousBy). It overlaps the statuses above rather than
	// partitioning them, and says how much of the run is flaky.
	Suspicious   int     `json:"suspicious"`
	Score        float64 `json:"score"`
	CoveredScore float64 `json:"covered_score"`
	// Coverage is the fraction of viable mutants a test reaches: covered /
	// (covered + no_coverage), or zero when nothing is viable.
	Coverage float64 `json:"coverage"`
	// Sampled is the fraction of the mutants the sample could select
	// (run -sample) that it kept: 1 without a sample or when the sample
	// kept everything. The mutants it did not keep are Skipped, so the
	// score is computed over the sample.
	Sampled float64 `json:"sampled"`
	// Classes breaks the score down by mutant class (mutator.ClassErrPath,
	// ClassConcurrency, ClassDefault), so the score of error-handling code
	// can be read next to the overall one.
	Classes map[string]ClassTotals `json:"classes,omitempty"`
	// Diff is the score of a run a diff scoped (a result is Skipped or
	// Changed); nil otherwise.
	Diff *DiffTotals `json:"diff,omitempty"`
}

// DiffTotals scores a run scoped to a diff twice: over the mutants on its
// changed lines, and over every commit-relevant mutant the run kept
// (Options.DiffExpand), which the two report apart since they correlate
// only weakly (Ma et al., ICSME 2020).
type DiffTotals struct {
	ChangedLines   ClassTotals `json:"changed_lines"`
	CommitRelevant ClassTotals `json:"commit_relevant"`
}

// ClassTotals is the score of a group of mutants, counted as in Totals:
// Killed includes timeouts, Survived the mutants the score counts as
// survivors (Report.Survived).
type ClassTotals struct {
	Mutants  int     `json:"mutants"`
	Killed   int     `json:"killed"`
	Survived int     `json:"survived"`
	Score    float64 `json:"score"`
}

// Report is the JSON written by Run.
type Report struct {
	// Pkg is the import path of the mutated package, taken from its
	// mutants; empty when there were none.
	Pkg        string `json:"pkg"`
	BaselineMS int64  `json:"baseline_ms"`
	// TimeoutMS is the longest a mutant's test process may run:
	// Options.Timeout, or the cap of the derived per-mutant timeouts.
	TimeoutMS int64 `json:"timeout_ms"`
	// CountSuspect says SUSPECT_EQUIVALENT mutants count as survivors:
	// in the score, the weak spots and the exported reports.
	CountSuspect bool     `json:"count_suspect,omitempty"`
	Tests        []Test   `json:"tests"`
	Results      []Result `json:"results"`
	Totals       Totals   `json:"totals"`
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
	t := Totals{Classes: map[string]ClassTotals{}}
	var diff DiffTotals
	for _, res := range r.Results {
		t.Mutants++
		class := cmp.Or(res.Class, mutator.ClassDefault)
		c := t.Classes[class]
		r.add(&c, res.Status)
		t.Classes[class] = c
		if res.Status != Skipped {
			r.add(&diff.CommitRelevant, res.Status)
		}
		if res.Changed {
			r.add(&diff.ChangedLines, res.Status)
		}
		if len(res.SuspiciousBy) > 0 {
			t.Suspicious++
		}
		switch res.Status {
		case Killed:
			t.Killed++
		case Lived:
			t.Lived++
		case Timeout:
			t.Timeout++
		case RunError:
			t.RunError++
		case NoCoverage:
			t.NoCoverage++
		case NotViable:
			t.NotViable++
		case Ignored:
			t.Ignored++
		case Skipped:
			t.Skipped++
		case Equivalent:
			t.Equivalent++
		case SuspectEquivalent:
			t.SuspectEquivalent++
		}
	}
	if t.Skipped > 0 || diff.ChangedLines.Mutants > 0 {
		t.Diff = &diff
	}
	killed, covered := r.scored(t)
	if viable := covered + t.NoCoverage; viable > 0 {
		t.Score = float64(killed) / float64(viable)
		t.Coverage = float64(covered) / float64(viable)
	}
	if covered > 0 {
		t.CoveredScore = float64(killed) / float64(covered)
	}
	r.Totals = t
}

// add counts a mutant with status s into c.
func (r *Report) add(c *ClassTotals, s Status) {
	c.Mutants++
	switch {
	case s == Killed || s == Timeout:
		c.Killed++
	case r.Survived(s):
		c.Survived++
	}
	if c.Killed+c.Survived > 0 {
		c.Score = float64(c.Killed) / float64(c.Killed+c.Survived)
	}
}

// scored returns the killed mutants of t and the covered ones the score
// counts: killed and survivors other than NO_COVERAGE.
func (r *Report) scored(t Totals) (killed, covered int) {
	killed = t.Killed + t.Timeout
	covered = killed + t.Lived
	if r.CountSuspect {
		covered += t.SuspectEquivalent
	}
	return killed, covered
}

// Threshold is the minimum Score and CoveredScore of a passing run; a zero
// bound is off.
type Threshold struct {
	Score, Covered float64
}

// Check returns an error naming each score of r below its bound. A score
// with nothing to count (an empty shard, a run whose every mutant was
// skipped) passes.
func (th Threshold) Check(r *Report) error {
	killed, covered := r.scored(r.Totals)
	var errs []error
	if viable := covered + r.Totals.NoCoverage; th.Score > 0 && viable > 0 && r.Totals.Score < th.Score {
		errs = append(errs, fmt.Errorf("score %.4f (%d of %d killed) is below the threshold %g", r.Totals.Score, killed, viable, th.Score))
	}
	if th.Covered > 0 && covered > 0 && r.Totals.CoveredScore < th.Covered {
		errs = append(errs, fmt.Errorf("covered score %.4f (%d of %d covered killed) is below the threshold %g", r.Totals.CoveredScore, killed, covered, th.Covered))
	}
	return errors.Join(errs...)
}

// Survived reports whether a mutant with status s counts as a survivor
// in r: LIVED and NO_COVERAGE, and SUSPECT_EQUIVALENT when CountSuspect.
func (r *Report) Survived(s Status) bool {
	return s == Lived || s == NoCoverage || (s == SuspectEquivalent && r.CountSuspect)
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
//
// A SUSPECT_EQUIVALENT mutant is a survivor only in a report that
// counts it (Report.CountSuspect).
//
// A RUN_ERROR mutant is no survivor — the run died from infrastructure,
// so nothing was observed about the mutant — and counts towards nothing:
// the function it is in is a weak spot only through its other mutants.
func WeakSpots(mutants []mutator.Mutant, reports ...*Report) []Spot {
	status := map[string]Status{}
	for _, r := range reports {
		for _, res := range r.Results {
			status[res.MutantID] = res.Status
			if res.Status == SuspectEquivalent && r.Survived(res.Status) {
				status[res.MutantID] = Lived
			}
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
