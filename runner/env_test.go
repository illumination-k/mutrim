package runner

import (
	"slices"
	"testing"
)

// The test binary sees the mutant and nothing of Bazel's test protocol,
// which the rules_go test main would otherwise act on a second time.
func TestChildEnv(t *testing.T) {
	env := []string{
		"PATH=/bin",
		"GOMUTANT_ID=stale",
		"GOMUTANT_TRACE=/tmp/stale",
		"TEST_TOTAL_SHARDS=4",
		"TEST_SHARD_INDEX=1",
		"TEST_SHARD_STATUS_FILE=/tmp/status",
		"TESTBRIDGE_TEST_ONLY=TestFoo",
		"TESTBRIDGE_TEST_RUNNER_FAIL_FAST=1",
		"TEST_TIMEOUT=300",
		"XML_OUTPUT_FILE=/tmp/test.xml",
		"TEST_UNDECLARED_OUTPUTS_DIR=/tmp/outputs",
		"GO_TEST_RUN_FROM_BAZEL=1",
	}
	want := []string{"PATH=/bin", "TEST_UNDECLARED_OUTPUTS_DIR=/tmp/outputs", "GO_TEST_RUN_FROM_BAZEL=1", "GOMUTANT_ID=abc"}
	if got := childEnv(env, "abc", ""); !slices.Equal(got, want) {
		t.Errorf("childEnv = %v, want %v", got, want)
	}
	if got := childEnv(nil, "", "/tmp/t"); !slices.Equal(got, []string{"GOMUTANT_ID=", "GOMUTANT_TRACE=/tmp/t"}) {
		t.Errorf("trace env = %v", got)
	}
}

// Killers are every failing top-level test, plus, on a timeout, the ones
// that started and never finished; subtests are not counted.
func TestParseOutput(t *testing.T) {
	out := []byte(`=== RUN   TestA
=== RUN   TestA/sub
    --- FAIL: TestA/sub (0.00s)
--- FAIL: TestA (0.00s)
=== RUN   TestB
--- PASS: TestB (0.00s)
=== RUN   TestC
--- SKIP: TestC (0.00s)
=== RUN   TestD
`)
	if started, failed := parseOutput(out, false); started != 4 || !slices.Equal(failed, []string{"TestA"}) {
		t.Errorf("parseOutput = %d, %v", started, failed)
	}
	if started, failed := parseOutput(out, true); started != 4 || !slices.Equal(failed, []string{"TestA", "TestD"}) {
		t.Errorf("parseOutput(timed out) = %d, %v", started, failed)
	}
}

// Only statuses that came from running the tests are copied forward from a
// previous report.
func TestStatusExecuted(t *testing.T) {
	for s, want := range map[Status]bool{Killed: true, Lived: true, Timeout: true, NoCoverage: false, NotViable: false, "": false} {
		if got := s.executed(); got != want {
			t.Errorf("%q.executed() = %v, want %v", s, got, want)
		}
	}
}

// Totals count every status; the score counts timeouts as kills and
// unreached mutants as survivors, and is zero when nothing is viable.
func TestTotals(t *testing.T) {
	r := &Report{Results: []Result{
		{Status: Killed},
		{Status: Killed},
		{Status: Timeout},
		{Status: Lived},
		{Status: NoCoverage},
		{Status: NotViable},
		{Status: NotViable},
	}}
	r.total()
	want := Totals{Mutants: 7, Killed: 2, Lived: 1, Timeout: 1, NoCoverage: 1, NotViable: 2, Score: 0.6}
	if r.Totals != want {
		t.Errorf("totals = %+v, want %+v", r.Totals, want)
	}
	r = &Report{Results: []Result{{Status: NotViable}}}
	r.total()
	if r.Totals.Score != 0 || r.Totals.Mutants != 1 {
		t.Errorf("totals of nothing viable = %+v", r.Totals)
	}
}
