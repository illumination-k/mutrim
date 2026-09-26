// Package ingest reads what other languages' tools observe about a test
// suite into Observations, the rows `mutrim minimize` builds its matrix
// from next to (or instead of) Go report.json files. Each adapter reads one
// tool's output: Stryker's mutation-testing-report-schema JSON and
// cargo-mutants' mutants.out for the kills, an Istanbul coverage-final.json
// (vitest, jest) and an llvm-cov JSON export (cargo llvm-cov) for the code
// one test reaches, and JUnit XML for test durations. The adapters agree
// on the row names, so the files of one suite merge by test name.
//
// Rows are named after the test runner's own convention: a Stryker test is
// "<test file>#<name>", the describe blocks and the test joined by spaces
// as StrykerJS names them; a Rust test is "<crate>::<path>", the crate
// being the test binary's (the library for a unit test, the file name for
// an integration test).
package ingest

import (
	"encoding/json/v2"
	"fmt"
	"path/filepath"
	"strings"
)

// Observations are the rows of one tool's run.
type Observations struct {
	// Source names the tool the rows come from ("stryker",
	// "cargo-mutants", "istanbul", "llvm-cov", "junit"). It is what tells
	// minimize an observations file from a report.json.
	Source string `json:"source"`
	Tests  []Test `json:"tests"`
	// Survived lists the mutants no test killed; they are no requirement,
	// and only count in the minimize totals.
	Survived []string `json:"survived,omitempty"`
	// Mutants locates the killed and surviving mutants in their
	// functions, for the minimize weak spots; only the adapters whose tool
	// names the function (cargo-mutants) fill it.
	Mutants []Mutant `json:"mutants,omitempty"`
}

// Mutant places one mutant, named by its label in Kills and Survived.
type Mutant struct {
	ID   string `json:"id"`
	File string `json:"file"`
	Func string `json:"func"`
	Line int    `json:"line"`
}

// Test is one row: what a test reaches and kills. The labels are opaque
// to minimize; a label two files share is one requirement.
type Test struct {
	Name string `json:"name"`
	// DurationMS is how long the test takes on its own; zero when unknown.
	DurationMS int64 `json:"duration_ms,omitzero"`
	// Sites are the mutant locations the test reaches.
	Sites []string `json:"sites,omitempty"`
	// Blocks are the code regions (statements, llvm-cov regions) the test
	// executes.
	Blocks []string `json:"blocks,omitempty"`
	// Kills are the mutants the test fails against.
	Kills []string `json:"kills,omitempty"`
}

// Read loads observations written by `mutrim import`.
func Read(data []byte) (*Observations, error) {
	var o Observations
	if err := json.Unmarshal(data, &o); err != nil {
		return nil, err
	}
	if o.Source == "" {
		return nil, fmt.Errorf("ingest: no source: not an observations file")
	}
	return &o, nil
}

// IsObservations reports whether data is an observations file rather
// than a report.json, by its source field.
func IsObservations(data []byte) bool {
	var v struct {
		Source string `json:"source"`
	}
	return json.Unmarshal(data, &v) == nil && v.Source != ""
}

// span is a source range; the label of a site or a block.
func span(file string, line, col, endLine, endCol int) string {
	return fmt.Sprintf("%s:%d:%d-%d:%d", file, line, col, endLine, endCol)
}

// Paths makes the files a coverage tool reports relative to root, and
// leaves out those outside it, those include does not match (when set) and
// those exclude matches: code that is not the suite's to test
// (dependencies, the standard library, other packages of a workspace, the
// tests).
type Paths struct {
	Root    string
	Include func(rel string) bool
	Exclude func(rel string) bool
}

// rel returns path relative to the root, slash-separated, and whether it
// is kept.
func (p Paths) rel(path string) (string, bool) {
	if filepath.IsAbs(path) && p.Root != "" {
		r, err := filepath.Rel(p.Root, path)
		if err != nil || r == ".." || strings.HasPrefix(r, "../") {
			return "", false
		}
		path = r
	}
	path = filepath.ToSlash(path)
	if (p.Include != nil && !p.Include(path)) || (p.Exclude != nil && p.Exclude(path)) {
		return "", false
	}
	return path, true
}
