// Package minimize picks a subset of tests that satisfies every
// requirement of a criteria.Matrix and reports the rest as redundant.
// It is a weighted greedy set cover: at each step the test with the best
// gain, the weight of the requirements it newly satisfies per millisecond
// of its own run time, is selected until nothing gains anything.
//
// Nothing is deleted. Protected tests are selected first whatever their
// gain, and every redundant test comes with the selected tests that
// subsume it, so a person (or an LLM) can decide.
package minimize

import (
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

// Selection is a test the greedy cover kept, in selection order.
type Selection struct {
	Name string `json:"name"`
	// Protected tests are selected first, before any gain is computed.
	Protected bool `json:"protected,omitempty"`
	// New counts the requirements no earlier selection satisfied.
	New int `json:"new"`
	// Gain is the weight of those requirements per millisecond of the
	// test's run time (a zero duration counts as one).
	Gain float64 `json:"gain"`
}

// Redundancy is a test whose every requirement the selection satisfies.
type Redundancy struct {
	Name string `json:"name"`
	// SubsumedBy lists the selected tests that each satisfy every
	// requirement of this one; empty when only their union does.
	SubsumedBy []string `json:"subsumed_by"`
}

// Result is the outcome of Greedy.
type Result struct {
	Selected  []Selection  `json:"selected"`
	Redundant []Redundancy `json:"redundant"`
}

// Greedy runs the weighted greedy set cover over m.
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
		fresh := t.Covers.Difference(covered)
		res.Selected = append(res.Selected, Selection{
			Name:      t.Name,
			Protected: protected,
			New:       int(fresh.Count()), //nolint:gosec // bounded by len(m.Requirements)
			Gain:      gain(m, fresh, t.DurationMS),
		})
		covered.InPlaceUnion(t.Covers)
		delete(remaining, t.Name)
	}

	for _, t := range m.Tests {
		if o.Protected != nil && o.Protected(t.Name) {
			select_(t, true)
		}
	}
	for {
		var best criteria.Test
		bestGain := 0.0
		for _, t := range m.Tests {
			if !remaining[t.Name] {
				continue
			}
			if g := gain(m, t.Covers.Difference(covered), t.DurationMS); g > bestGain {
				best, bestGain = t, g
			}
		}
		if bestGain == 0 {
			break
		}
		select_(best, false)
	}

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

// gain is the weight of the requirements in fresh per millisecond.
func gain(m *criteria.Matrix, fresh *bitset.BitSet, durationMS int64) float64 {
	weight := 0.0
	for i, ok := fresh.NextSet(0); ok; i, ok = fresh.NextSet(i + 1) {
		weight += m.Requirements[i].Weight
	}
	return weight / float64(max(durationMS, 1))
}
