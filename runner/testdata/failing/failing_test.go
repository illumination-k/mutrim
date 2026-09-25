package failing

import (
	"os"
	"testing"
)

// broken reports whether the fixture's tests are made to fail.
func broken() bool { return os.Getenv("MUTRIM_FAILING") != "" }

// unmutated reports whether no mutant is selected, as in the baseline and
// the trace runs.
func unmutated() bool { return os.Getenv("GOMUTANT_ID") == "" }

// tracing reports whether the run is traced, which only the trace runs and
// the mutants' runs are.
func tracing() bool { return os.Getenv("GOMUTANT_TRACE") != "" }

func TestDouble(t *testing.T) {
	if Double(2) != 4 {
		t.Error("Double(2)")
	}
}

// TestBroken fails whenever it runs.
func TestBroken(t *testing.T) {
	Double(1)
	if broken() {
		t.Error("broken")
	}
}

// TestPanics takes the whole process down, so the baseline never starts
// the tests after it.
func TestPanics(t *testing.T) {
	if broken() {
		panic("broken")
	}
}

// TestOrder fails in the baseline, which runs the whole suite, and passes
// on its own.
func TestOrder(t *testing.T) {
	if broken() && unmutated() && !tracing() {
		t.Error("depends on the tests before it")
	}
}

// TestAlone passes in the baseline and fails when traced on its own.
func TestAlone(t *testing.T) {
	if broken() && unmutated() && tracing() {
		t.Error("depends on the tests before it")
	}
}

// TestAfter runs only once the baseline skips TestPanics.
func TestAfter(t *testing.T) {
	if Double(3) != 6 {
		t.Error("Double(3)")
	}
}
