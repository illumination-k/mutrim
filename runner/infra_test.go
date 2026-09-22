package runner

import "testing"

// classifyExit splits the non-zero exit of the test binary into a kill
// and a RUN_ERROR. A --- FAIL: or panic: line means the failure came
// from inside a test, so the tests' verdict stands whatever else the
// run printed (the gomutants rule); anything else that says the process
// died from outside the tests is infrastructure the mutant cannot be
// blamed for.
func TestClassifyExit(t *testing.T) {
	cases := []struct {
		name     string
		out      string
		exitCode int
		want     Status
	}{
		{"test failure", "=== RUN   TestX\n--- FAIL: TestX (0.00s)\n", 1, Killed},
		{"panic", "panic: runtime error: index out of range [5] with length 3\n", 2, Killed},
		{"a FAIL: line overrides a fatal error", "--- FAIL: TestX (0.00s)\nfatal error: out of memory\n", 2, Killed},
		{"a panic overrides a signal death", "--- FAIL: TestX (0.00s)\npanic: runtime error: invalid memory address or nil pointer dereference\n", -1, Killed},
		{"fatal error", "fatal error: out of memory\n", 2, RunError},
		{"deadlock", "fatal error: all goroutines are asleep - deadlock!\n", 2, RunError},
		{"signal death", "", -1, RunError},
		{"exit status 2 with a runtime stack", "goroutine 1 [running]:\nmain.main()\n", 2, RunError},
		{"exit code the testing package never uses", "", 3, RunError},
		{"bare exit 1, no test failed", "", 1, Killed},
		{"bare exit 2, no stack", "", 2, Killed},
	}
	for _, tc := range cases {
		if got := classifyExit([]byte(tc.out), tc.exitCode); got != tc.want {
			t.Errorf("%s: classifyExit(exit %d) = %s, want %s", tc.name, tc.exitCode, got, tc.want)
		}
	}
}
