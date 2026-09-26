package report_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/illumination-k/mutrim/mutator"
	"github.com/illumination-k/mutrim/report"
	"github.com/illumination-k/mutrim/runner"
)

// writeFiles writes name -> content under a temp directory and returns it.
func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// ExportSurvivors lists the unkilled mutants with the source of their function
// and of each test reaching them, the own package's test preferred over a
// same-named one elsewhere, and a subtest row quoting its parent.
func TestExportSurvivors(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"p/a.go":      "package p\n\n// F doubles x.\nfunc F(x int) int {\n\treturn x * 2\n}\n\ntype T struct{}\n\nfunc (*T) M() {}\n",
		"p/a_test.go": "package p\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) { F(1) }\n\nfunc TestM(t *testing.T) {\n\tt.Run(\"a\", func(t *testing.T) {})\n}\n",
		"q/q_test.go": "package q\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) { /* q */ }\n",
	})
	sources, err := report.NewSources([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	ms := []mutator.Mutant{
		{ID: "killed", Pkg: "p", File: "p/a.go", Func: "F"},
		{ID: "lived", Pkg: "p", File: "p/a.go", Line: 5, Func: "F", Operator: "arithmetic", Description: "* -> /", Diff: "-x * 2\n+x / 2\n"},
		{ID: "method", Pkg: "p", File: "p/a.go", Line: 10, Func: "(*T).M", Operator: "extra"},
		{ID: "uncovered", Pkg: "p", File: "p/a.go", Line: 11, Func: "F"},
		{ID: "ignored", Pkg: "p", File: "p/a.go", Func: "F"},
	}
	rep := &runner.Report{
		Pkg: "p",
		Tests: []runner.Test{
			{Name: "TestF", Sites: []string{"killed", "lived"}},
			{Name: "TestM/a", Parent: "TestM", Sites: []string{"method"}},
			{Name: "example.com/q.TestF", Pkg: "example.com/q", Sites: []string{"lived"}},
		},
		Results: []runner.Result{
			{MutantID: "killed", Status: runner.Killed},
			{MutantID: "lived", Status: runner.Lived},
			{MutantID: "method", Status: runner.SuspectEquivalent},
			{MutantID: "uncovered", Status: runner.NoCoverage},
			{MutantID: "ignored", Status: runner.Ignored},
		},
	}
	got := report.ExportSurvivors(ms, []*runner.Report{rep}, sources)
	if len(got) != 3 || got[0].ID != "lived" || got[1].ID != "method" || got[2].ID != "uncovered" {
		t.Fatalf("got %+v", got)
	}

	lived := got[0]
	if lived.Status != runner.Lived || lived.Diff != ms[1].Diff || lived.FuncSource != "// F doubles x.\nfunc F(x int) int {\n\treturn x * 2\n}" {
		t.Errorf("lived = %+v", lived)
	}
	want := []report.ExportedTest{
		{Name: "TestF", Source: "func TestF(t *testing.T) { F(1) }"},
		{Name: "example.com/q.TestF", Pkg: "example.com/q", Source: "func TestF(t *testing.T) { /* q */ }"},
	}
	if len(lived.Tests) != 2 || lived.Tests[0] != want[0] || lived.Tests[1] != want[1] {
		t.Errorf("lived tests = %+v, want %+v", lived.Tests, want)
	}

	method := got[1]
	if method.FuncSource != "func (*T) M() {}" || len(method.Tests) != 1 || method.Tests[0].Source != "func TestM(t *testing.T) {\n\tt.Run(\"a\", func(t *testing.T) {})\n}" {
		t.Errorf("method = %+v", method)
	}
	if u := got[2]; u.Status != runner.NoCoverage || len(u.Tests) != 0 {
		t.Errorf("uncovered = %+v", u)
	}
}
