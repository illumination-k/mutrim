package runner

import (
	"fmt"
	"math"
	"testing"
)

// The sample draw is deterministic, uniform over the IDs, and moved by the
// seed: the same ID and seed hash the same, different seeds hash
// differently, and a sample of a half keeps about half of many mutants.
func TestSampleHash(t *testing.T) {
	id := "0123456789abcdef"
	if a, b := sampleHash(id, 7), sampleHash(id, 7); a != b {
		t.Errorf("sampleHash(%q, 7) = %v and %v: the same ID and seed must hash the same", id, a, b)
	}
	if sampleHash(id, 7) == sampleHash(id, 8) {
		t.Errorf("seeds 7 and 8 hash %q the same: the seed must move the sample", id)
	}
	var kept int
	for i := range 200 {
		if (Options{Sample: 0.5, Seed: 42}).keeps(fmt.Sprintf("%016x", i)) {
			kept++
		}
	}
	if diff := math.Abs(float64(kept)/200 - 0.5); diff > 0.15 {
		t.Errorf("a sample of 0.5 kept %d of 200 mutants, want about half", kept)
	}

	// Zero and one keep everything: a run without a sample is the same as
	// one whose sample is everything.
	for _, sample := range []float64{0, 1} {
		o := Options{Sample: sample}
		if !o.keeps(id) {
			t.Errorf("Sample %g dropped a mutant: zero and one keep everything", sample)
		}
	}
}
