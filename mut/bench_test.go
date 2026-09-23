package mut

import (
	"path/filepath"
	"strconv"
	"testing"
)

// benchIDs are site IDs shaped like the mutator's: 16 hex digits.
var benchIDs = func() []string {
	ids := make([]string, 64)
	for i := range ids {
		ids[i] = strconv.FormatUint(0xfedcba9876543210+uint64(i), 16)
	}
	return ids
}()

// setTrace traces to a temp file for the duration of the benchmark.
func setTrace(b *testing.B) {
	b.Helper()
	prev := trace
	trace = newTracer(filepath.Join(b.TempDir(), "trace"))
	b.Cleanup(func() { trace = prev })
}

// BenchmarkCmp is the cost of one inactive site on the hot path: the
// schemata of `a < b` in a loop, with no mutant selected and no trace.
func BenchmarkCmp(b *testing.B) {
	prev := active
	active = ""
	b.Cleanup(func() { active = prev })
	var n int
	for i := 0; b.Loop(); i++ {
		if Cmp(benchIDs[i&63], i, 1000, "<", "<=") {
			n++
		}
	}
	_ = n
}

// BenchmarkCmpActive is BenchmarkCmp while another mutant is selected, as
// in every mutant's run.
func BenchmarkCmpActive(b *testing.B) {
	prev := active
	active = "0123456789abcdef"
	b.Cleanup(func() { active = prev })
	var n int
	for i := 0; b.Loop(); i++ {
		if Cmp(benchIDs[i&63], i, 1000, "<", "<=") {
			n++
		}
	}
	_ = n
}

// BenchmarkCmpTraced is BenchmarkCmpActive with GOMUTANT_TRACE set, as in
// the trace runs and every mutant's run: sites already recorded dominate.
func BenchmarkCmpTraced(b *testing.B) {
	prev := active
	active = "0123456789abcdef"
	b.Cleanup(func() { active = prev })
	setTrace(b)
	var n int
	for i := 0; b.Loop(); i++ {
		if Cmp(benchIDs[i&63], i, 1000, "<", "<=") {
			n++
		}
	}
	_ = n
}

// BenchmarkCmpTracedParallel is BenchmarkCmpTraced from parallel
// goroutines, as in a test using t.Parallel or a concurrent library.
func BenchmarkCmpTracedParallel(b *testing.B) {
	prev := active
	active = "0123456789abcdef"
	b.Cleanup(func() { active = prev })
	setTrace(b)
	b.RunParallel(func(pb *testing.PB) {
		var n, i int
		for pb.Next() {
			if Cmp(benchIDs[i&63], i, 1000, "<", "<=") {
				n++
			}
			i++
		}
		_ = n
	})
}
