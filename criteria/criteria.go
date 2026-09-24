// Package criteria turns the observations of a mutation run into a
// test × requirement matrix. A Criterion names, for every test, the
// requirements that test satisfies; Compose unions several criteria, each
// with a weight, into one Matrix that the minimize package works on.
//
// Three criteria come from the runner's report: SiteCoverage (the mutant
// sites a test reaches: cheap, and what narrows the mutation run),
// BlockCoverage (the blocks a test reaches, which also sees code without a
// mutant site) and Mutation (the mutants a test kills: the real
// objective). Surviving mutants are no requirement at all, so they never
// keep a test, and Mutation.Dominators drops the killed mutants another
// one subsumes.
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

// BlockCoverage maps each test to the IDs of the blocks it reaches
// (mutator.Block). A function of straight-line calls and assignments has
// no mutant site, so a test exercising only it satisfies no site
// requirement; its block keeps that test.
type BlockCoverage map[string][]string

// Name returns "block".
func (BlockCoverage) Name() string { return "block" }

// Rows returns the map itself.
func (c BlockCoverage) Rows() map[string][]string { return c }

// Mutation maps each test to the IDs of the mutants it kills.
type Mutation map[string][]string

// Name returns "kill".
func (Mutation) Name() string { return "kill" }

// Rows returns the map itself.
func (m Mutation) Rows() map[string][]string { return m }

// Mutants returns the IDs of the mutants some test kills, sorted.
func (m Mutation) Mutants() []string {
	ids := map[string]bool{}
	for _, killed := range m {
		for _, id := range killed {
			ids[id] = true
		}
	}
	return slices.Sorted(maps.Keys(ids))
}

// Dominators keeps only the dominator mutants of the dynamic subsumption
// graph (Ammann, Delamaro, Offutt, ICST 2014; Kurtz et al., FSE 2016).
// Mutant a subsumes b when every test killing a also kills b, so any test
// set that kills a kills b too and b constrains nothing: a mutant whose
// kill set is a strict superset of another's is dropped, and the mutants
// with one kill set are merged into one requirement, labeled with the
// first of their IDs. Without it the cover over-rewards a test killing
// many trivial variants of one site. Every test of m keeps its row.
func (m Mutation) Dominators() Mutation {
	tests := slices.Sorted(maps.Keys(m))
	killers := map[string]*bitset.BitSet{}
	for j, t := range tests {
		for _, id := range m[t] {
			if killers[id] == nil {
				killers[id] = bitset.New(uint(len(tests))) //nolint:gosec // a slice length
			}
			killers[id].Set(uint(j)) //nolint:gosec // a slice index
		}
	}
	// One class per kill set, represented by its first mutant.
	type class struct {
		id      string
		killers *bitset.BitSet
		count   uint
	}
	var classes []class
	sets := map[string]bool{}
	for _, id := range m.Mutants() {
		if key := killers[id].String(); !sets[key] {
			sets[key] = true
			classes = append(classes, class{id, killers[id], killers[id].Count()})
		}
	}
	// A strict subset is smaller, so by size the dominators subsuming a
	// class are all kept before it: a subsumed one is subsumed by a
	// dominator too, by transitivity.
	slices.SortStableFunc(classes, func(a, b class) int { return cmp.Compare(a.count, b.count) })
	// A subset's first killer is one of the superset's killers, so the
	// kept dominators are indexed by their first killer and a class is
	// only compared with those under its own killers.
	dominators := map[string]bool{}
	byFirst := make([][]*bitset.BitSet, len(tests))
	for _, c := range classes {
		subsumed := false
		for j, ok := c.killers.NextSet(0); ok && !subsumed; j, ok = c.killers.NextSet(j + 1) {
			subsumed = slices.ContainsFunc(byFirst[j], c.killers.IsSuperSet)
		}
		if !subsumed {
			dominators[c.id] = true
			first, _ := c.killers.NextSet(0)
			byFirst[first] = append(byFirst[first], c.killers)
		}
	}
	out := Mutation{}
	for _, t := range tests {
		killed := map[string]bool{}
		for _, id := range m[t] {
			if dominators[id] {
				killed[id] = true
			}
		}
		out[t] = slices.Sorted(maps.Keys(killed))
	}
	return out
}

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
	// columns maps each criterion's labels to their requirement, so the
	// rows below look a label up without building "<name>:<label>".
	columns := make([]map[string]uint, len(criteria))
	for k, c := range criteria {
		columns[k] = map[string]uint{}
		for name, labels := range c.Rows() {
			names[name] = true
			for _, l := range labels {
				if _, ok := columns[k][l]; ok {
					continue
				}
				columns[k][l] = 0
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
	for k, c := range criteria {
		for l := range columns[k] {
			columns[k][l] = index[c.Name()+":"+l]
		}
	}

	for _, name := range slices.Sorted(maps.Keys(names)) {
		t := Test{Name: name, DurationMS: durations[name], Covers: bitset.New(uint(len(m.Requirements)))} //nolint:gosec // a slice length
		for k, c := range criteria {
			for _, l := range c.Rows()[name] {
				t.Covers.Set(columns[k][l])
			}
		}
		m.Tests = append(m.Tests, t)
	}
	return m
}
