package report_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/illumination-k/mutrim/mutator"
	"github.com/illumination-k/mutrim/report"
	"github.com/illumination-k/mutrim/runner"
)

func mutants() []mutator.Mutant {
	return []mutator.Mutant{
		{ID: "1", Pkg: "p", File: "p/a.go", Line: 4, Col: 11, EndLine: 4, EndCol: 12, Func: "F", Operator: "relational", Description: "> -> >=", Viable: true},
		{ID: "2", Pkg: "p", File: "p/a.go", Line: 7, Col: 2, EndLine: 7, EndCol: 14, Func: "F", Operator: "return", Description: "return -> zero values", Viable: true},
		{ID: "3", Pkg: "p", File: "p/b.go", Line: 9, Col: 5, EndLine: 9, EndCol: 6, Func: "G", Operator: "arith", Description: "+ -> -", Viable: true},
		{ID: "4", Pkg: "p", File: "p/b.go", Line: 12, Col: 5, EndLine: 12, EndCol: 6, Func: "G", Operator: "arith", Description: "* -> /", Ignored: "disable-func", Reason: "legacy"},
		{ID: "5", Pkg: "p", File: "p/b.go", Line: 15, Col: 5, EndLine: 15, EndCol: 6, Func: "G", Operator: "constant", Description: "1 -> 2"},
		{ID: "absent", Pkg: "p", File: "p/c.go", Line: 1, Col: 1, EndLine: 1, EndCol: 2, Func: "H", Operator: "arith", Description: "+ -> -", Viable: true},
	}
}

func report1() *runner.Report {
	return &runner.Report{
		Pkg:   "p",
		Tests: []runner.Test{{Name: "TestF", DurationMS: 3, Sites: []string{"1", "2"}}, {Name: "TestG", DurationMS: 5}},
		Results: []runner.Result{
			{MutantID: "1", Status: runner.Killed, TestsRun: 1, KilledBy: []string{"TestF"}, DurationMS: 7},
			{MutantID: "2", Status: runner.Lived, TestsRun: 1, DurationMS: 8},
			{MutantID: "3", Status: runner.NoCoverage},
			{MutantID: "4", Status: runner.Ignored},
			{MutantID: "5", Status: runner.NotViable},
		},
	}
}

// Every field the schema wants is filled from mutants.json and the
// reports: the status vocabulary, the per-mutant location, the tests that
// reach and kill it, and the reason an ignored mutant gives.
func TestToStryker(t *testing.T) {
	s := report.ToStryker(mutants(), []*runner.Report{report1()}, nil)

	if s.SchemaVersion != "2" || s.Schema == "" {
		t.Errorf("schema = %q, version = %q", s.Schema, s.SchemaVersion)
	}
	if len(s.Files) != 2 {
		t.Fatalf("files = %v, want a.go and b.go only (the mutant no report mentions is dropped)", keys(s.Files))
	}
	a, ok := s.Files["p/a.go"]
	if !ok || a.Language != "go" || len(a.Mutants) != 2 {
		t.Fatalf("p/a.go = %+v", a)
	}
	first := a.Mutants[0]
	want := report.Mutant{
		ID:             "1",
		MutatorName:    "relational",
		Description:    "F > -> >=",
		Replacement:    ">=",
		Location:       report.Location{Start: report.Position{Line: 4, Column: 11}, End: report.Position{Line: 4, Column: 12}},
		Status:         report.Killed,
		CoveredBy:      []string{"TestF"},
		KilledBy:       []string{"TestF"},
		TestsCompleted: ptr(1),
		Duration:       7,
	}
	if !equalMutant(first, want) {
		t.Errorf("mutant 1 = %+v, want %+v", first, want)
	}
	if got := a.Mutants[1]; got.Status != report.Survived || len(got.KilledBy) != 0 || got.TestsCompleted == nil {
		t.Errorf("mutant 2 = %+v, want Survived with no killer but a test count", got)
	}

	b := s.Files["p/b.go"]
	if len(b.Mutants) != 3 {
		t.Fatalf("p/b.go = %+v", b)
	}
	if got := b.Mutants[0]; got.Status != report.NoCoverage || len(got.CoveredBy) != 0 || got.TestsCompleted != nil {
		t.Errorf("mutant 3 = %+v, want NoCoverage, uncovered and never run", got)
	}
	if got := b.Mutants[1]; got.Status != report.Ignored || got.StatusReason != "disable-func legacy" {
		t.Errorf("mutant 4 = %+v, want Ignored with its directive and reason", got)
	}
	if got := b.Mutants[2]; got.Status != report.CompileError {
		t.Errorf("mutant 5 = %+v, want CompileError for a mutant that never built", got)
	}

	tests, ok := s.TestFiles[""]
	if !ok || len(tests.Tests) != 2 {
		t.Fatalf("testFiles = %+v", s.TestFiles)
	}
	if tests.Tests[0] != (report.Test{ID: "TestF", Name: "TestF"}) {
		t.Errorf("tests[0] = %+v", tests.Tests[0])
	}
}

// A mutant a diff left out is Ignored in the schema, the status for a
// mutant deliberately kept out of the score, and says why. It is not a
// survivor either, so it is not annotated.
func TestToStrykerSkipped(t *testing.T) {
	ms := mutants()
	r := &runner.Report{Pkg: "p", Results: []runner.Result{{MutantID: "1", Status: runner.Skipped}}}
	s := report.ToStryker(ms, []*runner.Report{r}, nil)
	got := s.Files["p/a.go"].Mutants[0]
	if got.Status != report.Ignored || got.StatusReason != "not in the diff" || got.TestsCompleted != nil {
		t.Errorf("skipped mutant = %+v, want Ignored, never run, with its reason", got)
	}
	var buf bytes.Buffer
	if err := report.WriteAnnotations(&buf, ms, []*runner.Report{r}); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("a skipped mutant must not be annotated: %s", buf.String())
	}
}

// A RUN_ERROR is a RuntimeError in the schema, which keeps it out of the
// score. It is no survivor — the run died from infrastructure — so it
// is not annotated either.
func TestToStrykerRunError(t *testing.T) {
	ms := mutants()
	r := &runner.Report{Pkg: "p", Results: []runner.Result{{MutantID: "1", Status: runner.RunError, DurationMS: 4}}}
	s := report.ToStryker(ms, []*runner.Report{r}, nil)
	got := s.Files["p/a.go"].Mutants[0]
	if got.Status != report.RuntimeError || got.TestsCompleted != nil || got.Duration != 4 {
		t.Errorf("run error mutant = %+v, want RuntimeError, never run", got)
	}
	var buf bytes.Buffer
	if err := report.WriteAnnotations(&buf, ms, []*runner.Report{r}); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("a run error mutant must not be annotated: %s", buf.String())
	}
}

// Shard reports of one package are merged: their results add up and the
// test list, which every shard repeats, is not duplicated.
func TestToStrykerMergesShards(t *testing.T) {
	shard0 := &runner.Report{
		Tests:   []runner.Test{{Name: "TestF", Sites: []string{"1"}}},
		Results: []runner.Result{{MutantID: "1", Status: runner.Killed, KilledBy: []string{"TestF"}}},
	}
	shard1 := &runner.Report{
		Tests:   []runner.Test{{Name: "TestF", Sites: []string{"1"}}},
		Results: []runner.Result{{MutantID: "3", Status: runner.Lived}},
	}
	s := report.ToStryker(mutants(), []*runner.Report{shard0, shard1}, nil)
	if n := len(s.Files["p/a.go"].Mutants) + len(s.Files["p/b.go"].Mutants); n != 2 {
		t.Errorf("mutants = %d, want the two the shards report", n)
	}
	if got := s.TestFiles[""].Tests; len(got) != 1 {
		t.Errorf("tests = %+v, want TestF once", got)
	}
	if got := s.Files["p/a.go"].Mutants[0].CoveredBy; len(got) != 1 {
		t.Errorf("coveredBy = %v, want TestF once", got)
	}
}

// The source of a mutated file is read through -srcs even when the path
// in mutants.json does not resolve from here, which is the normal case
// under Bazel and for an absolute path from another machine.
func TestSourcesResolveBySuffix(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "p"), 0o750); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "p", "a.go"), "package p // the one\n")
	write(t, filepath.Join(dir, "q.go"), "package q\n")

	srcs, err := report.NewSources([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	s := report.ToStryker(mutants(), []*runner.Report{report1()}, srcs)
	if got := s.Files["p/a.go"].Source; got != "package p // the one\n" {
		t.Errorf("source = %q", got)
	}
	if got := s.Files["p/b.go"].Source; got != "" {
		t.Errorf("unfound source = %q, want empty", got)
	}
}

// A file named directly is indexed too, and a name shared by two files
// resolves to the one whose path matches further back.
func TestSourcesPrefersTheLongerSharedSuffix(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"p", "other"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o750); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, p, "a.go"), "package "+p+"\n")
	}
	srcs, err := report.NewSources([]string{filepath.Join(dir, "other", "a.go"), filepath.Join(dir, "p", "a.go")})
	if err != nil {
		t.Fatal(err)
	}
	if got := srcs.Read("elsewhere/p/a.go"); got != "package p\n" {
		t.Errorf("Read = %q, want the file under p/", got)
	}
}

func TestSourcesRejectsAMissingPath(t *testing.T) {
	if _, err := report.NewSources([]string{filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Error("want an error for a path that does not exist")
	}
}

// The HTML view embeds the report where the viewer reads it, with "<"
// escaped so that source text can never end the script block.
func TestWriteHTML(t *testing.T) {
	s := report.ToStryker(mutants(), []*runner.Report{report1()}, nil)
	s.Files["p/a.go"] = report.File{Language: "go", Source: "// </script><script>alert(1)</script>\n", Mutants: s.Files["p/a.go"].Mutants}

	var buf bytes.Buffer
	if err := report.WriteHTML(&buf, s); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "<mutation-test-report-app") || !strings.Contains(out, "unpkg.com/mutation-testing-elements") {
		t.Error("the viewer is not embedded")
	}
	embedded, ok := cut(out, `<script type="application/json" id="mutation-report">`, "</script>")
	if !ok {
		t.Fatalf("no JSON block:\n%s", out)
	}
	var back report.Stryker
	if err := json.Unmarshal([]byte(embedded), &back); err != nil {
		t.Fatalf("embedded JSON does not parse: %v", err)
	}
	if got := back.Files["p/a.go"].Source; got != s.Files["p/a.go"].Source {
		t.Errorf("source round trip = %q", got)
	}
}

// Annotations name the surviving mutants only, in source order, with the
// characters that would end a workflow command escaped.
func TestWriteAnnotations(t *testing.T) {
	ms := mutants()
	ms[2].Description = "a, b -> a: b" // the characters a property may not hold
	var buf bytes.Buffer
	if err := report.WriteAnnotations(&buf, ms, []*runner.Report{report1()}); err != nil {
		t.Fatal(err)
	}
	want := "::warning file=p/a.go,line=7,col=2,endLine=7,endColumn=14::LIVED: F return: return -> zero values\n" +
		"::warning file=p/b.go,line=9,col=5,endLine=9,endColumn=6::NO_COVERAGE: G arith: a, b -> a: b\n"
	if got := buf.String(); got != want {
		t.Errorf("annotations =\n%s\nwant\n%s", got, want)
	}
}

// A mutants.json written before end positions existed still yields a
// valid location: the viewer needs an end, so the start stands in.
func TestLocationWithoutAnEnd(t *testing.T) {
	ms := []mutator.Mutant{{ID: "1", File: "p/a.go", Line: 4, Col: 11, Operator: "relational", Description: "> -> >="}}
	s := report.ToStryker(ms, []*runner.Report{{Results: []runner.Result{{MutantID: "1", Status: runner.Killed}}}}, nil)
	if got := s.Files["p/a.go"].Mutants[0].Location; got.End != got.Start {
		t.Errorf("location = %+v, want the end at the start", got)
	}
}

func ptr(n int) *int { return &n }

func equalMutant(got, want report.Mutant) bool {
	if got.TestsCompleted == nil || want.TestsCompleted == nil {
		return false
	}
	a, b := got, want
	a.TestsCompleted, b.TestsCompleted = nil, nil
	return jsonEqual(a, b) && *got.TestsCompleted == *want.TestsCompleted
}

func jsonEqual(a, b report.Mutant) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func keys(m map[string]report.File) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// cut returns the text between the first open and the next end.
func cut(s, open, end string) (string, bool) {
	_, rest, ok := strings.Cut(s, open)
	if !ok {
		return "", false
	}
	inner, _, ok := strings.Cut(rest, end)
	return inner, ok
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
