// Package report renders a run's mutants and report.json in the formats
// other tools already read: the Stryker mutation-testing-elements schema,
// the single-file HTML view of that schema, and GitHub Actions
// annotations.
package report

import (
	"cmp"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/illumination-k/mutrim/mutator"
	"github.com/illumination-k/mutrim/runner"
)

// schemaURL is the schema the report validates against; the
// mutation-test-report-app web component and the Stryker dashboard both
// accept it.
const schemaURL = "https://raw.githubusercontent.com/stryker-mutator/mutation-testing-elements/master/packages/report-schema/src/mutation-testing-report-schema.json"

// Stryker is a mutation-testing-report-schema v2 report.
type Stryker struct {
	Schema        string              `json:"$schema"`
	SchemaVersion string              `json:"schemaVersion"`
	Thresholds    Thresholds          `json:"thresholds"`
	Files         map[string]File     `json:"files"`
	TestFiles     map[string]TestFile `json:"testFiles,omitempty"`
}

// Thresholds are the mutation-score bounds the viewer colors by.
type Thresholds struct {
	High int `json:"high"`
	Low  int `json:"low"`
}

// File is one mutated source file.
type File struct {
	Language string   `json:"language"`
	Source   string   `json:"source"`
	Mutants  []Mutant `json:"mutants"`
}

// Mutant is one mutant of a File.
type Mutant struct {
	ID           string   `json:"id"`
	MutatorName  string   `json:"mutatorName"`
	Description  string   `json:"description,omitempty"`
	Replacement  string   `json:"replacement,omitempty"`
	Location     Location `json:"location"`
	Status       Status   `json:"status"`
	StatusReason string   `json:"statusReason,omitempty"`
	CoveredBy    []string `json:"coveredBy,omitempty"`
	KilledBy     []string `json:"killedBy,omitempty"`
	// TestsCompleted is absent for a mutant that never ran, which the
	// viewer shows differently from one that ran against no test.
	TestsCompleted *int  `json:"testsCompleted,omitempty"`
	Duration       int64 `json:"duration,omitzero"`
}

// Location is the mutated span, one-based lines and columns, with end
// one past the last character.
type Location struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Position is a point in a source file.
type Position struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

// TestFile holds the tests of the run; mutrim knows their names but not
// which file declares them, so they all live under one empty key.
type TestFile struct {
	Tests []Test `json:"tests"`
}

// Test is one row of the run's kill matrix (a top-level test, or a
// subtest under `mutrim run -subtests`), referenced by CoveredBy and
// KilledBy.
type Test struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Status is a mutation-testing-report-schema mutant status.
type Status string

// The statuses mutrim produces, in the schema's vocabulary: a mutant
// whose run died from infrastructure is a RuntimeError, which the
// schema keeps out of the score.
const (
	Killed       Status = "Killed"
	Survived     Status = "Survived"
	NoCoverage   Status = "NoCoverage"
	Timeout      Status = "Timeout"
	CompileError Status = "CompileError"
	RuntimeError Status = "RuntimeError"
	Ignored      Status = "Ignored"
)

// status maps a runner status onto the schema's vocabulary. A mutant the
// type-check or the lowering rejected never built, which is what the
// schema calls a compile error; one a diff or a filter excluded, or one
// proven or suspected equivalent, is Ignored, the schema's status for a
// mutant left out of the score on purpose, unless r counts it as a
// survivor (runner.Report.CountSuspect).
func status(r *runner.Report, s runner.Status) Status {
	switch {
	case s == runner.Killed:
		return Killed
	case s == runner.Lived, s == runner.SuspectEquivalent && r.CountSuspect:
		return Survived
	case s == runner.Timeout:
		return Timeout
	case s == runner.NoCoverage:
		return NoCoverage
	case s == runner.RunError:
		return RuntimeError
	case s == runner.Ignored, s == runner.Skipped, s == runner.Equivalent, s == runner.SuspectEquivalent:
		return Ignored
	default:
		return CompileError
	}
}

// defaultThresholds are Stryker's own defaults.
var defaultThresholds = Thresholds{High: 80, Low: 60}

// ToStryker renders the mutants and the reports of their run (the shards
// of a package, or several packages) as a Stryker report. Source text
// comes from srcs, which may be nil; a mutant no report mentions is left
// out, so a single shard's report yields that shard only.
func ToStryker(mutants []mutator.Mutant, reports []*runner.Report, srcs *Sources) *Stryker {
	results := map[string]runner.Result{}
	from := map[string]*runner.Report{} // mutant ID -> the report of its result
	tests := map[string]bool{}
	covered := map[string][]string{} // mutant ID -> test names, in report order
	for _, r := range reports {
		for _, res := range r.Results {
			results[res.MutantID] = res
			from[res.MutantID] = r
		}
		for _, t := range r.Tests {
			if tests[t.Name] {
				continue // identical across the shards of one package
			}
			tests[t.Name] = true
			for _, id := range t.Sites {
				covered[id] = append(covered[id], t.Name)
			}
		}
	}

	out := &Stryker{
		Schema:        schemaURL,
		SchemaVersion: "2",
		Thresholds:    defaultThresholds,
		Files:         map[string]File{},
	}
	cwd, _ := os.Getwd()
	for _, m := range mutants {
		res, ok := results[m.ID]
		if !ok {
			continue
		}
		name := displayPath(cwd, m.File)
		f, ok := out.Files[name]
		if !ok {
			f = File{Language: "go", Source: srcs.Read(m.File)}
		}
		f.Mutants = append(f.Mutants, toMutant(m, res, status(from[m.ID], res.Status), covered[m.ID]))
		out.Files[name] = f
	}
	if len(tests) > 0 {
		all := make([]Test, 0, len(tests))
		for _, name := range slices.Sorted(maps.Keys(tests)) {
			all = append(all, Test{ID: name, Name: name})
		}
		out.TestFiles = map[string]TestFile{"": {Tests: all}}
	}
	return out
}

func toMutant(m mutator.Mutant, res runner.Result, st Status, coveredBy []string) Mutant {
	out := Mutant{
		ID:          m.ID,
		MutatorName: m.Operator,
		Description: description(m),
		Replacement: replacement(m.Description),
		Location: Location{
			Start: Position{Line: m.Line, Column: m.Col},
			End:   Position{Line: m.EndLine, Column: m.EndCol},
		},
		Status:    st,
		CoveredBy: coveredBy,
		KilledBy:  res.KilledBy,
		Duration:  res.DurationMS,
	}
	if m.EndLine == 0 { // mutants.json written before end positions existed
		out.Location.End = out.Location.Start
	}
	if res.Status.Executed() {
		n := res.TestsRun
		out.TestsCompleted = &n
	}
	switch {
	case res.Status == runner.Skipped:
		out.StatusReason = "not in the diff"
	case m.Ignored != "":
		out.StatusReason = strings.TrimSpace(m.Ignored + " " + m.Reason)
	case m.Equivalent != "":
		out.StatusReason = "equivalent: " + m.Equivalent
	case res.Status == runner.SuspectEquivalent:
		out.StatusReason = "suspect equivalent: no test failed and the tests reached the same sites"
	}
	return out
}

// description names the mutant's function and rewrite, followed by the
// mutant's diff from mutants.json, which the viewer shows next to the
// source, when it has one.
func description(m mutator.Mutant) string {
	d := m.Func + " " + m.Description
	if m.Diff != "" {
		d += "\n\n" + m.Diff
	}
	return d
}

// replacement is the right-hand side of a "from -> to" description, which
// every operator spells that way; the viewer shows it in place of the
// original code.
func replacement(description string) string {
	_, to, ok := strings.Cut(description, " -> ")
	if !ok {
		return ""
	}
	return to
}

// displayPath prefers a path relative to the working directory, so that a
// `go list` run (absolute paths) reads like a Bazel one (execroot-relative).
func displayPath(cwd, file string) string {
	if cwd == "" || !filepath.IsAbs(file) {
		return filepath.ToSlash(file)
	}
	rel, err := filepath.Rel(cwd, file)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(file)
	}
	return filepath.ToSlash(rel)
}

// Survivors lists the mutants of reports that no test killed, in source
// order, paired with the status that says why.
func Survivors(mutants []mutator.Mutant, reports []*runner.Report) []Survivor {
	return survivors(mutants, reports, (*runner.Report).Survived)
}

// survivors is Survivors with the statuses that count as survived in a
// report decided by survived.
func survivors(mutants []mutator.Mutant, reports []*runner.Report, survived func(*runner.Report, runner.Status) bool) []Survivor {
	results := map[string]runner.Status{}
	kept := map[string]bool{}
	for _, r := range reports {
		for _, res := range r.Results {
			results[res.MutantID] = res.Status
			kept[res.MutantID] = survived(r, res.Status)
		}
	}
	out := []Survivor{}
	for _, m := range mutants {
		if kept[m.ID] {
			out = append(out, Survivor{Mutant: m, Status: results[m.ID]})
		}
	}
	slices.SortStableFunc(out, func(a, b Survivor) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Col, b.Col))
	})
	return out
}

// Survivor is a mutant no test killed.
type Survivor struct {
	mutator.Mutant
	Status runner.Status
}
