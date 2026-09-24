// Package minimize picks a subset of tests that satisfies every
// requirement of a criteria.Matrix and reports the rest as redundant.
// It is a weighted greedy set cover: at each step the test with the best
// gain, the weight of the requirements it newly satisfies per millisecond
// of its own run time, is selected until nothing gains anything. A
// selected test whose requirements the other selected tests also satisfy
// (a fast test picked early, then overtaken by a broader one) is dropped
// afterwards.
//
// Nothing is deleted. Protected tests are selected first whatever their
// gain, and every redundant test comes with the selected tests that
// subsume it, so a person (or an LLM) can decide. A selected test that
// satisfies a requirement no other test in the whole suite does is
// essential: Exclusives reports it with that requirement's label, and a
// redundant test with how many other tests share its requirements. The
// transpose that needs is the size of the matrix itself, which is why it
// stays out of Greedy.
package minimize

import (
	"cmp"
	"container/heap"
	"slices"

	"github.com/bits-and-blooms/bitset"

	"github.com/illumination-k/mutrim/criteria"
)

// Options configures Greedy.
type Options struct {
	// Protected reports whether a test is kept regardless of its gain;
	// nil protects nothing.
	Protected func(name string) bool
}

// Selection is a test the greedy cover kept: after Exclusives the
// essential ones come first, then by gain, ties keeping the selection
// order.
type Selection struct {
	Name string `json:"name"`
	// Protected tests are selected first, before any gain is computed.
	Protected bool `json:"protected,omitzero"`
	// New counts the requirements no earlier selection satisfied.
	New int `json:"new"`
	// Gain is the weight of those requirements per millisecond of the
	// test's run time (a zero duration counts as one).
	Gain float64 `json:"gain"`
	// Essential marks a test that satisfies a requirement no other test
	// in the whole suite does: dropping it loses that requirement. The
	// greedy selects it anyway; this is reporting only.
	Essential bool `json:"essential,omitzero"`
	// Unique lists, by label, the requirements no other test in the
	// whole suite satisfies.
	Unique []string `json:"unique"`
	// UniqueCount is len(Unique).
	UniqueCount int `json:"unique_count"`
}

// Redundancy is a test whose every requirement the selection satisfies.
type Redundancy struct {
	Name string `json:"name"`
	// SubsumedBy lists the selected tests that each satisfy every
	// requirement of this one; empty when only their union does.
	SubsumedBy []string `json:"subsumed_by"`
	// SharedWith counts the other tests that satisfy at least one of its
	// requirements: how many tests its coverage is spread over. One is a
	// test with a single dominator, one deletion away from essential;
	// zero satisfies nothing.
	SharedWith int `json:"shared_with"`
}

// Result is the outcome of Greedy, which Exclusives completes.
type Result struct {
	Selected  []Selection  `json:"selected"`
	Redundant []Redundancy `json:"redundant"`
}

// Greedy runs the weighted greedy set cover over m.
//
// A test's gain only shrinks as the cover grows, so the search is lazy
// (Minoux's accelerated greedy): candidates wait in a heap keyed by their
// last computed gain, and only the top is recomputed. Once its fresh gain
// still beats every other key, no other test can beat it. Ties go to the
// test first in m.Tests, as in a plain scan.
func Greedy(m *criteria.Matrix, o Options) Result {
	res := Result{Selected: []Selection{}}
	covered := bitset.New(uint(len(m.Requirements))) //nolint:gosec // a slice length
	byName := map[string]criteria.Test{}
	remaining := map[string]bool{}
	for _, t := range m.Tests {
		byName[t.Name] = t
		remaining[t.Name] = true
	}
	select_ := func(t criteria.Test, protected bool) {
		res.Selected = append(res.Selected, Selection{
			Name:      t.Name,
			Protected: protected,
			New:       int(t.Covers.DifferenceCardinality(covered)), //nolint:gosec // bounded by len(m.Requirements)
			Gain:      gain(m, t, covered),
		})
		covered.InPlaceUnion(t.Covers)
		delete(remaining, t.Name)
	}

	for _, t := range m.Tests {
		if o.Protected != nil && o.Protected(t.Name) {
			select_(t, true)
		}
	}
	h := candidates{}
	for i, t := range m.Tests {
		if remaining[t.Name] {
			h = append(h, candidate{i, gain(m, t, covered)})
		}
	}
	heap.Init(&h)
	for h.Len() > 0 {
		c := heap.Pop(&h).(candidate) //nolint:errcheck,forcetypeassert // the heap holds candidates only
		if c.gain = gain(m, m.Tests[c.test], covered); c.gain == 0 {
			continue
		}
		if h.Len() > 0 && h.before(h[0], c) {
			heap.Push(&h, c)
			continue
		}
		select_(m.Tests[c.test], false)
	}
	prune(m, byName, covered, remaining, &res, select_)

	res.Redundant = []Redundancy{}
	for _, t := range m.Tests { // m.Tests is sorted by name
		if !remaining[t.Name] {
			continue
		}
		r := Redundancy{Name: t.Name, SubsumedBy: []string{}}
		for _, s := range res.Selected {
			if byName[s.Name].Covers.IsSuperSet(t.Covers) {
				r.SubsumedBy = append(r.SubsumedBy, s.Name)
			}
		}
		slices.Sort(r.SubsumedBy)
		res.Redundant = append(res.Redundant, r)
	}
	return res
}

// Exclusives fills the reporting a person deciding about a test needs:
// for each selected test the requirements no other test in the whole
// suite satisfies (Unique, by label, making it Essential) and for each
// redundant test how many other tests satisfy at least one of its
// requirements (SharedWith): how far it is from unique, a test with a
// single sharer being one deletion away from essential. The greedy cover
// is unchanged by this; an essential test is selected anyway, which is
// why it always survives the cover (Harrold, Gupta, Soffa, TOSEM 1993;
// Chen & Lau, IST 1998: the essential tests are the fixed part of every
// reduction). It then brings the essential tests to the front of the
// selected list, ahead of the rest by gain.
func Exclusives(m *criteria.Matrix, res *Result) {
	byName := map[string]criteria.Test{}
	for _, t := range m.Tests {
		byName[t.Name] = t
	}
	exclusives(m, byName, res)
	sortSelected(res.Selected)
}

// candidate is a test of the matrix, by index, with an upper bound of its
// gain: the gain it had when last computed.
type candidate struct {
	test int
	gain float64
}

// candidates is a max-heap of candidates by gain, then by index.
type candidates []candidate

func (h candidates) Len() int { return len(h) }

func (h candidates) Less(i, j int) bool { return h.before(h[i], h[j]) }

func (candidates) before(a, b candidate) bool {
	return a.gain > b.gain || (a.gain == b.gain && a.test < b.test)
}

func (h candidates) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *candidates) Push(x any) { *h = append(*h, x.(candidate)) } //nolint:forcetypeassert // only candidates are pushed

func (h *candidates) Pop() any {
	old := *h
	c := old[len(old)-1]
	*h = old[:len(old)-1]
	return c
}

// prune drops every selected test whose requirements the other selected
// tests satisfy between them, later selections first, so that of two
// tests with the same requirements the earlier, higher-gain one stays.
// Protected tests are never dropped. A test is dropped when each of its
// requirements is satisfied by at least one other kept test, which a count
// of the kept tests per requirement answers without a union. The
// selection is then replayed so that New and Gain describe the final
// order.
func prune(m *criteria.Matrix, byName map[string]criteria.Test, covered *bitset.BitSet, remaining map[string]bool, res *Result, select_ func(criteria.Test, bool)) {
	kept := slices.Clone(res.Selected)
	count := make([]int, len(m.Requirements))
	for _, s := range kept {
		covers := byName[s.Name].Covers
		for i, ok := covers.NextSet(0); ok; i, ok = covers.NextSet(i + 1) {
			count[i]++
		}
	}
	for i := len(kept) - 1; i >= 0; i-- {
		if kept[i].Protected {
			continue
		}
		covers := byName[kept[i].Name].Covers
		shared := true
		for j, ok := covers.NextSet(0); ok && shared; j, ok = covers.NextSet(j + 1) {
			shared = count[j] > 1
		}
		if !shared {
			continue
		}
		for j, ok := covers.NextSet(0); ok; j, ok = covers.NextSet(j + 1) {
			count[j]--
		}
		remaining[kept[i].Name] = true
		kept[i].Name = ""
	}
	res.Selected = []Selection{}
	covered.ClearAll()
	for _, s := range kept {
		if s.Name != "" {
			select_(byName[s.Name], s.Protected)
		}
	}
}

// gain is the weight of the requirements t satisfies and covered does not,
// per millisecond of t's run time.
func gain(m *criteria.Matrix, t criteria.Test, covered *bitset.BitSet) float64 {
	weight := 0.0
	for i, ok := t.Covers.NextSet(0); ok; i, ok = t.Covers.NextSet(i + 1) {
		if !covered.Test(i) {
			weight += m.Requirements[i].Weight
		}
	}
	return weight / float64(max(t.DurationMS, 1))
}

// exclusives fills the reporting a person deciding about a test needs: for
// each selected test the requirements no other test in the whole suite
// satisfies (Unique, by label, making it Essential) and for each redundant
// test how many other tests satisfy at least one of its requirements
// (SharedWith): how far it is from unique, a test with a single sharer
// being one deletion away from essential. The greedy is unchanged by this;
// an essential test is selected anyway, which is why it always survives
// the cover (Harrold, Gupta, Soffa, TOSEM 1993; Chen & Lau, IST 1998:
// the essential tests are the fixed part of every reduction).
func exclusives(m *criteria.Matrix, byName map[string]criteria.Test, res *Result) {
	// columns[i] holds the tests that satisfy requirement i, so both
	// sides read off the transposed matrix: a test's unique
	// requirements are those with one satisfying test (itself), its
	// sharers the union of the columns of its requirements.
	columns := make([]bitset.BitSet, len(m.Requirements))
	for j, t := range m.Tests {
		for i, ok := t.Covers.NextSet(0); ok; i, ok = t.Covers.NextSet(i + 1) {
			columns[i].Set(uint(j)) //nolint:gosec // a slice index
		}
	}
	for k := range res.Selected {
		s := &res.Selected[k]
		s.Unique = []string{}
		for i, ok := byName[s.Name].Covers.NextSet(0); ok; i, ok = byName[s.Name].Covers.NextSet(i + 1) {
			if columns[i].Count() == 1 { // this test alone satisfies i
				s.Unique = append(s.Unique, m.Requirements[i].Label)
			}
		}
		s.UniqueCount = len(s.Unique)
		s.Essential = s.UniqueCount > 0
	}
	for k := range res.Redundant {
		r := &res.Redundant[k]
		shared := bitset.New(uint(len(m.Tests))) //nolint:gosec // a slice length
		for i, ok := byName[r.Name].Covers.NextSet(0); ok; i, ok = byName[r.Name].Covers.NextSet(i + 1) {
			shared.InPlaceUnion(&columns[i])
		}
		// The test satisfies its requirements itself, so it always
		// counts as one sharer too many.
		if byName[r.Name].Covers.Count() > 0 {
			r.SharedWith = int(shared.Count()) - 1 //nolint:gosec // bounded by len(m.Tests)
		}
	}
}

// sortSelected orders the kept tests so the report reads top-down as
// must keep → keep for now: the essential tests first, then by gain, ties
// keeping the selection order.
func sortSelected(selected []Selection) {
	slices.SortStableFunc(selected, func(a, b Selection) int {
		if a.Essential != b.Essential {
			if b.Essential {
				return 1
			}
			return -1
		}
		return cmp.Compare(b.Gain, a.Gain) // the higher gain first
	})
}
