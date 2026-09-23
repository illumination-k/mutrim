package runner

import (
	"os"
	"path/filepath"
	"testing"
)

// A test's hash covers its own text only: its doc comment and the rest of
// the file can change without invalidating it.
func TestHashTests(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a := write("a_test.go", "package p\n\n// Doc.\nfunc TestA(t *T) { x() }\n\nfunc TestB(t *T) {}\n")
	lib := write("p.go", "package p\n\nfunc TestNotATestFile() {}\n")
	before, err := HashTests([]string{a, lib})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 2 || before["TestA"] == "" || before["TestA"] == before["TestB"] {
		t.Fatalf("hashes = %v, want distinct TestA and TestB only", before)
	}

	write("a_test.go", "package p\n\n// Other doc.\n\n\nfunc TestA(t *T) { x() }\n\nfunc TestB(t *T) { y() }\n")
	after, err := HashTests([]string{a})
	if err != nil {
		t.Fatal(err)
	}
	if after["TestA"] != before["TestA"] {
		t.Error("an edit outside TestA changed its hash")
	}
	if after["TestB"] == before["TestB"] {
		t.Error("an edit to TestB left its hash alone")
	}

	if _, err := HashTests([]string{write("bad_test.go", "package")}); err == nil {
		t.Error("a malformed test file must be an error")
	}
	if _, err := HashTests([]string{filepath.Join(dir, "missing_test.go")}); err == nil {
		t.Error("a missing test file must be an error")
	}
}

// A kill holds while its killers do; a survivor while the tests reaching
// it are the ones it survived.
func TestReusable(t *testing.T) {
	before := map[string]Test{
		"TestA": {Name: "TestA", Hash: "a", Sites: []string{"m"}},
		"TestB": {Name: "TestB", Hash: "b", Sites: []string{"m"}},
	}
	killed := Result{MutantID: "m", Status: Killed, KilledBy: []string{"TestA"}}
	lived := Result{MutantID: "m", Status: Lived}
	for name, tc := range map[string]struct {
		prev     Result
		reaching []string
		now      map[string]Test
		want     bool
	}{
		"kill, unchanged":           {killed, []string{"TestA", "TestB"}, before, true},
		"kill, other test rewrote":  {killed, []string{"TestA", "TestB"}, map[string]Test{"TestA": before["TestA"], "TestB": {Hash: "b2"}}, true},
		"kill, killer rewritten":    {killed, []string{"TestA", "TestB"}, map[string]Test{"TestA": {Hash: "a2"}, "TestB": before["TestB"]}, false},
		"kill, killer deleted":      {killed, []string{"TestB"}, map[string]Test{"TestB": before["TestB"]}, false},
		"kill, killer unreaching":   {killed, []string{"TestB"}, before, false},
		"kill, killer now flaky":    {killed, []string{"TestA", "TestB"}, map[string]Test{"TestA": {Hash: "a", Flaky: true}, "TestB": before["TestB"]}, false},
		"timeout without killers":   {Result{Status: Timeout}, []string{"TestA", "TestB"}, before, true},
		"survivor, unchanged":       {lived, []string{"TestA", "TestB"}, before, true},
		"survivor, test rewritten":  {lived, []string{"TestA", "TestB"}, map[string]Test{"TestA": before["TestA"], "TestB": {Hash: "b2"}}, false},
		"survivor, new test":        {lived, []string{"TestA", "TestB", "TestC"}, before, false},
		"survivor, test deleted":    {lived, []string{"TestA"}, before, false},
		"survivor, test swapped":    {lived, []string{"TestA", "TestC"}, map[string]Test{"TestA": before["TestA"], "TestC": {Hash: "b"}}, false},
		"run error is never reused": {Result{Status: RunError}, []string{"TestA", "TestB"}, before, false},
	} {
		if got := reusable("m", tc.prev, tc.reaching, tc.now, before); got != tc.want {
			t.Errorf("%s: reusable = %v, want %v", name, got, tc.want)
		}
	}
}
