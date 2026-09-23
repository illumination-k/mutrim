package mutator

import (
	"flag"
	"testing"

	"golang.org/x/tools/go/packages"
)

// benchPkg adds one more package pattern to the benchmark, e.g.
// -benchpkg go/types (about 2300 mutants).
var benchPkg = flag.String("benchpkg", "", "extra package pattern to benchmark")

// benchPackages returns a tiny fixture and a mid-sized standard library
// package, so the numbers say something about real code while staying fast.
func benchPackages() []string {
	pkgs := []string{"./testdata/control", "go/parser"}
	if *benchPkg != "" {
		pkgs = append(pkgs, *benchPkg)
	}
	return pkgs
}

func loadBench(b *testing.B, pattern string) *packages.Package {
	b.Helper()
	pkgs, err := Load(".", pattern)
	if err != nil {
		b.Fatal(err)
	}
	return pkgs[0]
}

// BenchmarkGenerate measures the full pipeline with the pre-filter on and
// reports the cost per mutant.
func BenchmarkGenerate(b *testing.B) {
	for _, pattern := range benchPackages() {
		b.Run(pattern, func(b *testing.B) {
			pkg := loadBench(b, pattern)
			var n int
			for b.Loop() {
				n = len(Generate(pkg, Options{TypeCheck: true}))
			}
			b.ReportMetric(float64(n), "mutants")
			b.ReportMetric(b.Elapsed().Seconds()/float64(b.N)/float64(n), "sec/mutant")
		})
	}
}
