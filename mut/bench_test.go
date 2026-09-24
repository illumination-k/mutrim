package mut

import (
	"path/filepath"
	"strconv"
	"testing"
)

// BenchmarkSite is the cost of one schemata site on the hot path, the
// lowering of `a < b` in a loop over 64 sites, in each state a process
// runs it in: the identity build (no mutant selected), a mutant's run
// (another mutant selected), and a traced run, which every trace and
// every mutant's run is, from one goroutine and from GOMAXPROCS.
func BenchmarkSite(b *testing.B) {
	ids := make([]string, 64)
	for i := range ids {
		ids[i] = strconv.FormatUint(0xfedcba9876543210+uint64(i), 16)
	}
	for _, bc := range []struct {
		name     string
		active   string
		traced   bool
		parallel bool
	}{
		{name: "inactive"},
		{name: "other-active", active: "0123456789abcdef"},
		{name: "traced", active: "0123456789abcdef", traced: true},
		{name: "traced-parallel", active: "0123456789abcdef", traced: true, parallel: true},
	} {
		b.Run(bc.name, func(b *testing.B) {
			prevActive, prevTrace := active, trace
			b.Cleanup(func() { active, trace = prevActive, prevTrace })
			active, trace = bc.active, nil
			if bc.traced {
				trace = newTracer(filepath.Join(b.TempDir(), "trace"))
			}
			loop := func(next func() bool) {
				n := 0
				for i := 0; next(); i++ {
					if Cmp(ids[i&63], i, 1000, "<", "<=") {
						n++
					}
				}
				_ = n
			}
			if bc.parallel {
				b.RunParallel(func(pb *testing.PB) { loop(pb.Next) })
			} else {
				loop(b.Loop)
			}
		})
	}
}

// BenchmarkReach is the cost of the block trace at the head of every
// block of a schemata source: untraced, as in the identity build, and
// traced, as every trace and mutant run is.
func BenchmarkReach(b *testing.B) {
	ids := make([]string, 64)
	for i := range ids {
		ids[i] = strconv.FormatUint(0xfedcba9876543210+uint64(i), 16)
	}
	for _, traced := range []bool{false, true} {
		b.Run(map[bool]string{false: "untraced", true: "traced"}[traced], func(b *testing.B) {
			prev := trace
			b.Cleanup(func() { trace = prev })
			trace = nil
			if traced {
				trace = newTracer(filepath.Join(b.TempDir(), "trace"))
			}
			for i := 0; b.Loop(); i++ {
				Reach(ids[i&63])
			}
		})
	}
}
