package flaky

import (
	"os"
	"path/filepath"
	"testing"
)

// first reports whether this is the first execution of step across the
// processes of one runner.Run, which share the directory MUTRIM_FLAKY_STATE
// names; Mkdir both asks and records. Without that directory nothing
// flakes, so the fixture passes under a plain `go test`.
func first(step string) bool {
	dir := os.Getenv("MUTRIM_FLAKY_STATE")
	if dir == "" {
		return false
	}
	return os.Mkdir(filepath.Join(dir, step), 0o700) == nil
}

// flakyMutant reports whether the mutant the run selected is the one the
// fixture was told to fail against once.
func flakyMutant() bool {
	id := os.Getenv("GOMUTANT_ID")
	return id != "" && id == os.Getenv("MUTRIM_FLAKY_MUTANT")
}

// TestStable asserts on Compared and never flakes, so its kills are real.
func TestStable(t *testing.T) {
	if !Compared(1, 2) || Compared(2, 2) {
		t.Error("Compared")
	}
	Unasserted(1, 2)
}

// TestFlakyKill passes on its own but fails the first time the flaky
// mutant is active, so a single run records a kill that a rerun does not
// reproduce.
func TestFlakyKill(t *testing.T) {
	Unasserted(3, 4)
	if flakyMutant() && first("kill") {
		t.Error("flaky failure under the mutant")
	}
}

// TestFlakyBaseline fails the first time it is traced (only tracing sets
// GOMUTANT_TRACE) and once more under the flaky mutant, so one trace run
// calls it broken while two call it flaky, and its failure under the
// mutant must never count as a kill.
func TestFlakyBaseline(t *testing.T) {
	Unasserted(5, 6)
	if os.Getenv("GOMUTANT_TRACE") != "" && first("baseline") {
		t.Error("flaky failure on its own")
	}
	if flakyMutant() && first("baseline-mutant") {
		t.Error("flaky failure under the mutant")
	}
}
