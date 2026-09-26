package ingest

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestTestResults(t *testing.T) {
	log := `*** cargo test --no-fail-fast
     Running ` + "`rustc --crate-name demo src/lib.rs`" + `
     Running unittests src/lib.rs (target/debug/deps/demo_lib-0123456789abcdef)
test tests::a ... ok
test tests::b - should panic ... FAILED
     Running tests/it.rs (target/debug/deps/it-fedcba9876543210)
test c ... FAILED
   Doc-tests demo-lib
test src/lib.rs - f (line 3) ... FAILED
        PASS [   0.004s] (1/4) demo-lib tests::a
        FAIL [   0.120s] demo-lib::it c
        TIMEOUT [  60.000s] (3/4) demo-lib::bin/tool main_test
        SIGSEGV [   0.010s] (4/4) demo-lib::it d
    test tests::e ... FAILED
`
	path := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(path, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := testResults(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []testResult{
		{name: "demo_lib::tests::a"},
		{name: "demo_lib::tests::b", failed: true},
		{name: "it::c", failed: true},
		{name: "demo_lib::src/lib.rs - f (line 3)", failed: true},
		{name: "demo_lib::tests::a", durationMS: 4},
		{name: "it::c", failed: true, durationMS: 120},
		{name: "tool::main_test", failed: true, durationMS: 60000},
		{name: "it::d", failed: true, durationMS: 10},
	}
	if !slices.Equal(got, want) {
		t.Errorf("testResults =\n%+v\nwant\n%+v", got, want)
	}
}
