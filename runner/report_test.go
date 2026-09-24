package runner_test

import (
	"path/filepath"
	"testing"

	"github.com/illumination-k/mutrim/mutator"
	"github.com/illumination-k/mutrim/runner"
)

// A report written by Run reads back unchanged.
func TestReadReportRoundTrip(t *testing.T) {
	want := &runner.Report{
		BaselineMS: 12, TimeoutMS: 10000,
		Tests:   []runner.Test{{Name: "TestA", DurationMS: 3, Sites: []string{"a", "b"}}},
		Results: []runner.Result{{MutantID: "a", Status: runner.Killed, TestsRun: 1, KilledBy: []string{"TestA"}, DurationMS: 4}},
	}
	path := filepath.Join(t.TempDir(), "report.json")
	if err := writeJSON(t, path, want); err != nil {
		t.Fatal(err)
	}
	got, err := runner.ReadReport(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.BaselineMS != want.BaselineMS || got.TimeoutMS != want.TimeoutMS || len(got.Tests) != 1 || len(got.Results) != 1 {
		t.Errorf("ReadReport = %+v, want %+v", got, want)
	}
	if r := got.Results[0]; r.MutantID != "a" || r.Status != runner.Killed || r.KilledBy[0] != "TestA" || r.DurationMS != 4 {
		t.Errorf("result = %+v", r)
	}
}

// Weak spots count every surviving status per function, across shard
// reports, keyed by package so that same-named functions of two packages
// stay apart; the most survivors come first, ties by name.
func TestWeakSpots(t *testing.T) {
	mutants := []mutator.Mutant{
		{ID: "1", Pkg: "p", Func: "Lived", File: "p.go", Line: 20},
		{ID: "2", Pkg: "p", Func: "Lived", File: "p.go", Line: 10},
		{ID: "3", Pkg: "p", Func: "Lived", File: "p.go", Line: 30},
		{ID: "4", Pkg: "p", Func: "Unreached", File: "p.go", Line: 40},
		{ID: "5", Pkg: "q", Func: "Unreached", File: "q.go", Line: 1},
		{ID: "6", Pkg: "p", Func: "Fine", File: "p.go", Line: 50},
		{ID: "7", Pkg: "p", Func: "Fine", File: "p.go", Line: 51},
		{ID: "8", Pkg: "p", Func: "Absent", File: "p.go", Line: 60},
	}
	shard0 := &runner.Report{Results: []runner.Result{
		{MutantID: "1", Status: runner.Lived},
		{MutantID: "3", Status: runner.Killed},
		{MutantID: "5", Status: runner.NoCoverage},
		{MutantID: "7", Status: runner.Timeout},
	}}
	shard1 := &runner.Report{Results: []runner.Result{
		{MutantID: "2", Status: runner.Lived},
		{MutantID: "4", Status: runner.NoCoverage},
		{MutantID: "6", Status: runner.Killed},
	}}
	got := runner.WeakSpots(mutants, shard0, shard1)
	want := []runner.Spot{
		{Func: "Lived", File: "p.go", Line: 10, Killed: 1, Lived: 2},
		{Func: "Unreached", File: "p.go", Line: 40, NoCoverage: 1},
		{Func: "Unreached", File: "q.go", Line: 1, NoCoverage: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("weak spots = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("spot[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Uncovered lists the functions with a block no test of any report
// reaches, keyed by package, most unreached blocks first, each at its
// first unreached line; a package no report ran is left out.
func TestUncovered(t *testing.T) {
	blocks := []mutator.Block{
		{ID: "a", Pkg: "p", Func: "Part", File: "p.go", Line: 10},
		{ID: "b", Pkg: "p", Func: "Part", File: "p.go", Line: 12},
		{ID: "c", Pkg: "p", Func: "Part", File: "p.go", Line: 11},
		{ID: "d", Pkg: "p", Func: "None", File: "p.go", Line: 20},
		{ID: "e", Pkg: "p", Func: "None", File: "p.go", Line: 21},
		{ID: "f", Pkg: "p", Func: "Full", File: "p.go", Line: 30},
		{ID: "g", Pkg: "q", Func: "Part", File: "q.go", Line: 1},
		{ID: "h", Pkg: "r", Func: "NotRun", File: "r.go", Line: 1},
	}
	r0 := &runner.Report{Pkg: "p", Tests: []runner.Test{{Name: "TestA", Blocks: []string{"a"}}}}
	r1 := &runner.Report{Pkg: "q", Tests: []runner.Test{{Name: "q.TestB", Blocks: []string{"f"}, Flaky: true}}}
	got := runner.Uncovered(blocks, r0, r1)
	want := []runner.Gap{
		{Func: "None", File: "p.go", Line: 20, Blocks: 2, Unreached: 2},
		{Func: "Part", File: "p.go", Line: 11, Blocks: 3, Unreached: 2},
		{Func: "Part", File: "q.go", Line: 1, Blocks: 1, Unreached: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("uncovered = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("uncovered[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
