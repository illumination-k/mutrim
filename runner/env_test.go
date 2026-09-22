package runner

import (
	"slices"
	"testing"
	"time"
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

// Killers are the failing rows, plus, on a timeout, the rows that started
// and never finished. Without subtests the rows are the top-level tests;
// with them a failing parent is explained by its failing subtest, and a
// parent failing on its own (before or after its subtests) is attributed
// to every row under it.
func TestParseOutput(t *testing.T) {
	out := []byte(`=== RUN   TestA
=== RUN   TestA/sub
=== RUN   TestA/other
--- FAIL: TestA (0.00s)
    --- FAIL: TestA/sub (0.00s)
    --- PASS: TestA/other (0.00s)
=== RUN   TestB
--- PASS: TestB (0.00s)
=== RUN   TestC
--- SKIP: TestC (0.00s)
=== RUN   TestD
=== RUN   TestD/x
`)
	top := []string{"TestA", "TestB", "TestC", "TestD"}
	if started, failed := parseOutput(out, false, top); started != 4 || !slices.Equal(failed, []string{"TestA"}) {
		t.Errorf("parseOutput = %d, %v", started, failed)
	}
	if started, failed := parseOutput(out, true, top); started != 4 || !slices.Equal(failed, []string{"TestA", "TestD"}) {
		t.Errorf("parseOutput(timed out) = %d, %v", started, failed)
	}
	sub := []string{"TestA/sub", "TestA/other", "TestB", "TestC", "TestD/x"}
	if started, failed := parseOutput(out, false, sub); started != 5 || !slices.Equal(failed, []string{"TestA/sub"}) {
		t.Errorf("parseOutput(subtests) = %d, %v", started, failed)
	}
	if started, failed := parseOutput(out, true, sub); started != 5 || !slices.Equal(failed, []string{"TestA/sub", "TestD/x"}) {
		t.Errorf("parseOutput(subtests, timed out) = %d, %v", started, failed)
	}
	if started, failed := parseOutput([]byte("=== RUN   TestB\n--- PASS: TestB (0.00s)\n"), false, []string{"TestB"}); started != 1 || failed != nil {
		t.Errorf("parseOutput(passing) = %d, %#v, want no killers", started, failed)
	}

	// The parent fails after its subtests passed, and a parent that fails
	// before running any of them: both count for every row asked for.
	parentOnly := []byte(`=== RUN   TestA
=== RUN   TestA/sub
=== RUN   TestA/other
--- FAIL: TestA (0.00s)
    --- PASS: TestA/sub (0.00s)
    --- PASS: TestA/other (0.00s)
=== RUN   TestE
--- FAIL: TestE (0.00s)
`)
	rows := []string{"TestA/sub", "TestA/other", "TestE/never"}
	if started, failed := parseOutput(parentOnly, false, rows); started != 2 || !slices.Equal(failed, []string{"TestA/sub", "TestA/other", "TestE/never"}) {
		t.Errorf("parseOutput(parent fails) = %d, %v", started, failed)
	}
}

// Rows are the top-level tests, or with subtests the tests that have no
// subtest of their own; examples and fuzz targets are never rows.
func TestRows(t *testing.T) {
	names := []string{"TestA", "TestA/x", "TestA/x/deep", "TestA/y", "TestB", "ExampleC", "FuzzD", "FuzzD/seed#0"}
	if got, want := rows(names, false), []string{"TestA", "TestB"}; !slices.Equal(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
	if got, want := rows(names, true), []string{"TestA/x/deep", "TestA/y", "TestB"}; !slices.Equal(got, want) {
		t.Errorf("rows(subtests) = %v, want %v", got, want)
	}
	if got := rows(nil, true); got == nil || len(got) != 0 {
		t.Errorf("rows of nothing = %#v, want an empty list", got)
	}
}

// A -test.run pattern names the parent's elements one by one and the rows
// as an alternation, quoted, so it selects exactly those rows; the rows
// reaching a mutant are grouped by parent, one pattern each.
func TestTestPattern(t *testing.T) {
	cases := map[string]struct {
		rows []string
		want string
	}{
		"all":       {nil, ""},
		"top-level": {[]string{"TestA", "TestB"}, "^(TestA|TestB)$"},
		"subtests":  {[]string{"TestA/x", "TestA/2+3"}, `^TestA$/^(x|2\+3)$`},
		"nested":    {[]string{"TestA/x/deep"}, "^TestA$/^x$/^(deep)$"},
	}
	for name, tc := range cases {
		if got := testPattern(tc.rows); got != tc.want {
			t.Errorf("%s: testPattern(%v) = %q, want %q", name, tc.rows, got, tc.want)
		}
	}
	groups := groupByParent([]string{"TestB/y", "TestA", "TestB/x", "TestA/x/deep", "TestC"})
	want := [][]string{{"TestA", "TestC"}, {"TestA/x/deep"}, {"TestB/y", "TestB/x"}}
	if len(groups) != len(want) {
		t.Fatalf("groups = %v, want %v", groups, want)
	}
	for i := range want {
		if !slices.Equal(groups[i], want[i]) {
			t.Errorf("group %d = %v, want %v", i, groups[i], want[i])
		}
	}
}

// Only statuses that came from running the tests are copied forward from a
// previous report; a RUN_ERROR came from outside them, so it is executed
// again instead.
func TestStatusExecuted(t *testing.T) {
	for s, want := range map[Status]bool{Killed: true, Lived: true, Timeout: true, RunError: false, NoCoverage: false, NotViable: false, "": false} {
		if got := s.Executed(); got != want {
			t.Errorf("%q.Executed() = %v, want %v", s, got, want)
		}
	}
}

// Totals count every status; the score counts timeouts as kills and
// unreached mutants as survivors, and is zero when nothing is viable. A
// RUN_ERROR counts towards no score, so it is not in the denominator
// either.
func TestTotals(t *testing.T) {
	r := &Report{Results: []Result{
		{Status: Killed},
		{Status: Killed},
		{Status: Timeout},
		{Status: Lived},
		{Status: RunError},
		{Status: NoCoverage},
		{Status: NotViable},
		{Status: NotViable},
	}}
	r.total()
	want := Totals{Mutants: 8, Killed: 2, Lived: 1, Timeout: 1, RunError: 1, NoCoverage: 1, NotViable: 2, Score: 0.6}
	if r.Totals != want {
		t.Errorf("totals = %+v, want %+v", r.Totals, want)
	}
	r = &Report{Results: []Result{{Status: NotViable}}}
	r.total()
	if r.Totals.Score != 0 || r.Totals.Mutants != 1 {
		t.Errorf("totals of nothing viable = %+v", r.Totals)
	}
}

// The derived timeout follows the durations of the rows reaching the
// mutant, between MinTimeout and the cap; Options.Timeout overrides it.
func TestMutantTimeout(t *testing.T) {
	durations := map[string]int64{"TestFast": 5, "TestSlow": 20_000, "TestMid": 4_000}
	maxTimeout := 60 * time.Second
	o := Options{TimeoutFactor: 3, TimeoutConst: 2 * time.Second}
	for _, tc := range []struct {
		rows []string
		want time.Duration
	}{
		{[]string{"TestFast"}, MinTimeout},
		{[]string{"TestMid"}, 14 * time.Second},
		{[]string{"TestFast", "TestMid"}, 14015 * time.Millisecond},
		{[]string{"TestSlow", "TestMid"}, maxTimeout},
	} {
		if got := o.mutantTimeout(tc.rows, durations, maxTimeout); got != tc.want {
			t.Errorf("mutantTimeout(%v) = %s, want %s", tc.rows, got, tc.want)
		}
	}
	o.Timeout = time.Second
	if got := o.mutantTimeout([]string{"TestSlow"}, durations, maxTimeout); got != time.Second {
		t.Errorf("Options.Timeout must override: got %s", got)
	}
}
