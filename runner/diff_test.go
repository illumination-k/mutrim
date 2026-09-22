package runner_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/illumination-k/mutrim/runner"
)

// sample is a git-style diff of two files plus a deletion, with the noise
// a real `git diff` carries: index lines, a rename, a binary file and a
// context line that looks like a header.
const sample = `diff --git a/pkg/calc.go b/pkg/calc.go
index 1111111..2222222 100644
--- a/pkg/calc.go
+++ b/pkg/calc.go
@@ -3,3 +3,7 @@ package pkg
 func Add(a, b int) int {
-	return a - b
+	return a + b
+}
+
+func Sub(a, b int) int {
+	return a - b
 }
@@ -40,2 +42,3 @@ func Unrelated() {
 	println("+++ b/not-a-header.go")
+	println("tail")
 }
diff --git a/pkg/calc_test.go b/pkg/calc_test.go
--- a/pkg/calc_test.go
+++ b/pkg/calc_test.go
@@ -1,1 +1,2 @@
 package pkg
+// a test file holds no mutant
diff --git a/pkg/gone.go b/pkg/gone.go
--- a/pkg/gone.go
+++ /dev/null
@@ -1,3 +0,0 @@
-package pkg
-
-func Gone() {}
diff --git a/pkg/logo.png b/pkg/logo.png
Binary files a/pkg/logo.png and b/pkg/logo.png differ
`

func TestParseDiff(t *testing.T) {
	d, err := runner.ParseDiff(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	// The hunk starts at new line 3: the context line is 3, the removed
	// line advances nothing, so the added lines are 4-8, and 43 in the
	// second hunk.
	for _, line := range []int{4, 5, 6, 7, 8, 43} {
		if !d.Touches("pkg/calc.go", line, 0) {
			t.Errorf("line %d is added, but the diff does not touch it", line)
		}
	}
	for _, line := range []int{1, 2, 3, 9, 42, 44} {
		if d.Touches("pkg/calc.go", line, 0) {
			t.Errorf("line %d is context or outside a hunk, but the diff touches it", line)
		}
	}
	// A span overlapping an added line is touched, one ending before it is not.
	if !d.Touches("pkg/calc.go", 2, 4) || d.Touches("pkg/calc.go", 1, 3) {
		t.Error("a mutant span must be matched by overlap with an added line")
	}
	// Test files, deleted files and files outside the diff hold no mutants.
	for _, file := range []string{"pkg/calc_test.go", "pkg/gone.go", "pkg/other.go"} {
		if d.Touches(file, 1, 100) {
			t.Errorf("%s must not be touched", file)
		}
	}
	// A nil diff scopes nothing.
	var none *runner.Diff
	if !none.Touches("pkg/calc.go", 1, 0) {
		t.Error("a nil diff must touch everything")
	}
}

// The diff is relative to the repository root, while a mutant's file is
// absolute under `go list` and execroot-relative under Bazel.
func TestDiffPathMatching(t *testing.T) {
	d, err := runner.ParseDiff(strings.NewReader(
		"--- a/deep/pkg/calc.go\n+++ b/deep/pkg/calc.go\n@@ -1 +1,2 @@\n package pkg\n+var x = 1\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{
		"deep/pkg/calc.go",
		"/home/user/repo/deep/pkg/calc.go",
		"./deep/pkg/calc.go",
		filepath.FromSlash("/repo/deep/pkg/calc.go"),
		"pkg/calc.go", // a shorter path the diff's path ends with
		"calc.go",
	} {
		if !d.Touches(file, 2, 2) {
			t.Errorf("%s must match the diff's deep/pkg/calc.go", file)
		}
	}
	for _, file := range []string{"other/pkg/calc.go", "deep/pkg/calcs.go", "deep/pkg/sub/calc.go"} {
		if d.Touches(file, 2, 2) {
			t.Errorf("%s must not match the diff's deep/pkg/calc.go", file)
		}
	}
}

func TestReadDiff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pr.diff")
	if err := os.WriteFile(path, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := runner.ReadDiff(path)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Touches("pkg/calc.go", 4, 4) {
		t.Error("ReadDiff lost the added lines")
	}
	if _, err := runner.ReadDiff(filepath.Join(t.TempDir(), "missing.diff")); err == nil {
		t.Error("a missing diff must be an error")
	}
}

// An empty diff (nothing changed) selects nothing, rather than everything.
func TestParseEmptyDiff(t *testing.T) {
	d, err := runner.ParseDiff(strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if d.Touches("pkg/calc.go", 1, 10) {
		t.Error("an empty diff must touch nothing")
	}
}
