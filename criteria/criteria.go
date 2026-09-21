// Package criteria turns the observations of a mutation run into a
// test × requirement matrix. A Criterion names, for every test, the
// requirements that test satisfies; Compose unions several criteria, each
// with a weight, into one Matrix that the minimize package works on.
//
// Two criteria come from the runner's report: SiteCoverage (the mutant
// sites a test reaches: cheap, and what narrows the mutation run) and
// Mutation (the mutants a test kills: the real objective). Surviving
// mutants are no requirement at all, so they never keep a test.
package criteria

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"

	"github.com/bits-and-blooms/bitset"
)

// Criterion is one source of requirements.
type Criterion interface {
	// Name prefixes the criterion's requirement labels, so that the same
	// label under two criteria stays two requirements.
	Name() string
	// Rows maps each test name to the labels of the requirements it
	// satisfies. Tests absent here satisfy none of them.
	Rows() map[string][]string
}

// SiteCoverage maps each test to the IDs of the mutant sites it reaches.
type SiteCoverage map[string][]string

// Name returns "site".
func (SiteCoverage) Name() string { return "site" }

// Rows returns the map itself.
func (c SiteCoverage) Rows() map[string][]string { return c }

// Mutation maps each test to the IDs of the mutants it kills.
type Mutation map[string][]string

// Name returns "kill".
func (Mutation) Name() string { return "kill" }

// Rows returns the map itself.
func (m Mutation) Rows() map[string][]string { return m }

// Weighted is a criterion with the weight of each of its requirements.
type Weighted struct {
	Criterion
	Weight float64
}

// Requirement is one column of the matrix.
type Requirement struct {
	// Label is "<criterion name>:<label>".
	Label  string  `json:"label"`
	Weight float64 `json:"weight"`
}

// Test is one row of the matrix.
type Test struct {
	Name string
	// DurationMS is how long the test takes on its own; zero when unknown.
	DurationMS int64
	// Covers has bit i set when the test satisfies Requirements[i].
	Covers *bitset.BitSet
}

// Matrix is the test × requirement bit matrix. Its JSON form lists each
// test's requirements as indices, which is the export for an external
// exact solver.
type Matrix struct {
	Requirements []Requirement `json:"requirements"`
	Tests        []Test        `json:"tests"`
}

type testJSON struct {
	Name       string `json:"name"`
	DurationMS int64  `json:"duration_ms"`
	Covers     []uint `json:"covers"`
}

// MarshalJSON lists the satisfied requirements as indices.
func (t Test) MarshalJSON() ([]byte, error) {
	covers := []uint{}
	for i, ok := t.Covers.NextSet(0); ok; i, ok = t.Covers.NextSet(i + 1) {
		covers = append(covers, i)
	}
	return json.Marshal(testJSON{Name: t.Name, DurationMS: t.DurationMS, Covers: covers})
}

// UnmarshalJSON is the inverse of MarshalJSON.
func (t *Test) UnmarshalJSON(data []byte) error {
	var v testJSON
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*t = Test{Name: v.Name, DurationMS: v.DurationMS, Covers: bitset.New(0)}
	for _, i := range v.Covers {
		t.Covers.Set(i)
	}
	return nil
}

// Compose builds the matrix of every criterion's requirements. The rows
// are the union of the criteria's tests and of durations' keys, sorted by
// name, so a test that satisfies nothing still appears; requirements are
// sorted by label.
func Compose(durations map[string]int64, criteria ...Weighted) *Matrix {
	m := &Matrix{}
	index := map[string]uint{}
	names := map[string]bool{}
	for name := range durations {
		names[name] = true
	}
	for _, c := range criteria {
		for name, labels := range c.Rows() {
			names[name] = true
			for _, l := range labels {
				label := c.Name() + ":" + l
				if _, ok := index[label]; !ok {
					index[label] = 0
					m.Requirements = append(m.Requirements, Requirement{Label: label, Weight: c.Weight})
				}
			}
		}
	}
	slices.SortFunc(m.Requirements, func(a, b Requirement) int { return cmp.Compare(a.Label, b.Label) })
	for i, r := range m.Requirements {
		index[r.Label] = uint(i) //nolint:gosec // a slice index
	}

	for _, name := range slices.Sorted(maps.Keys(names)) {
		t := Test{Name: name, DurationMS: durations[name], Covers: bitset.New(uint(len(m.Requirements)))} //nolint:gosec // a slice length
		for _, c := range criteria {
			for _, l := range c.Rows()[name] {
				t.Covers.Set(index[c.Name()+":"+l])
			}
		}
		m.Tests = append(m.Tests, t)
	}
	return m
}
