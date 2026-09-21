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
	if got := childEnv(env, "abc"); !slices.Equal(got, want) {
		t.Errorf("childEnv = %v, want %v", got, want)
	}
	if got := childEnv(nil, ""); !slices.Equal(got, []string{"GOMUTANT_ID="}) {
		t.Errorf("baseline env = %v", got)
	}
}
