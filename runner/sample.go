package runner

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
	"strconv"

	"github.com/illumination-k/mutrim/mutator"
)

// sampleHash is where a mutant falls in a sample: the fraction of the
// uint64 range that a SHA-256 of its ID and the seed occupies. A mutant
// is in the sample when that is below the sample fraction, a uniform
// draw over the mutant IDs, which are content hashes (mutator.mutantID):
// the same ID and seed always hash the same, so with one seed every
// shard of a run and every incremental run (run -previous) keeps the
// same mutants, and a mutant gen regenerates keeps its sample decision
// with its ID. A ~10% sample loses under a point of score (Gopinath et
// al., ICSE 2015/2016, TSE 2017), so the draw needs nothing finer than
// uniform.
func sampleHash(id string, seed int64) float64 {
	h := sha256.New()
	h.Write([]byte(strconv.FormatInt(seed, 10)))
	h.Write([]byte{0})
	h.Write([]byte(id))
	return float64(binary.BigEndian.Uint64(h.Sum(nil)[:8])) / float64(math.MaxUint64)
}

// keeps reports whether the sample of Options.Sample keeps the mutant id:
// its hash with Options.Seed below the fraction. Zero (no sample) and
// one (every mutant) keep everything, so a run without -sample is the
// same as one whose sample is everything.
func (o Options) keeps(id string) bool {
	if o.Sample <= 0 || o.Sample >= 1 {
		return true
	}
	return sampleHash(id, o.Seed) < o.Sample
}

// selectable reports whether the sample could select m: m is within the
// diff's scope (or the run has no diff) and passes the exclusions that
// precede the sample in Run's switch, so it would run without one.
// Mutants it rejects are reported Ignored, NotViable, Equivalent or
// Skipped by the diff, and no sample applies to them.
func selectable(scope map[string]bool, m mutator.Mutant) bool {
	return (scope == nil || scope[m.ID]) && m.Ignored == "" && m.Equivalent == "" && m.Viable
}
