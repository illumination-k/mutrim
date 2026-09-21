package mutator_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/illumination-k/mutrim/mutator"
)

// exportData asks the go command for the export data of pkg and its
// dependencies, the way a Bazel build would have compiled them. Standard
// library packages are copied into a rules_go-style tree,
// <dir>/<goos_goarch>/<path>.a, and the rest is written to an importcfg.
func exportData(t *testing.T, dir, pattern string) (importcfg, stdlib string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", "list", "-export", "-deps", "-f", "{{.ImportPath}}={{.Export}}={{.Standard}}", pattern) //nolint:gosec // test-controlled args
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -export: %v", err)
	}
	stdlib = filepath.Join(dir, "stdlib")
	var cfg strings.Builder
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, "=")
		path, export, standard := parts[0], parts[1], parts[2]
		if export == "" {
			continue // the package under test itself, or unsafe
		}
		if standard != "true" {
			fmt.Fprintf(&cfg, "packagefile %s=%s\n", path, export)
			continue
		}
		dst := filepath.Clean(filepath.Join(stdlib, "linux_amd64", filepath.FromSlash(path)+".a"))
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Clean(export))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil { //nolint:gosec // paths come from go list
			t.Fatal(err)
		}
	}
	importcfg = filepath.Join(dir, "importcfg")
	if err := os.WriteFile(importcfg, []byte(cfg.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return importcfg, stdlib
}

// LoadFiles must see the same package as go/packages: the same mutants,
// with the same IDs and viability, since the type-check pre-filter runs
// on the export-data types.
func TestLoadFilesMatchesLoad(t *testing.T) {
	ref := load(t, "schemata")
	want := mutator.Generate(ref, mutator.Options{TypeCheck: true})

	importcfg, stdlib := exportData(t, t.TempDir(), "./testdata/schemata")
	pkg, err := mutator.LoadFiles(mutator.FilesConfig{
		ImportPath: ref.PkgPath,
		Files:      ref.GoFiles,
		Importcfg:  importcfg,
		Stdlib:     stdlib,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Name != ref.Name || pkg.PkgPath != ref.PkgPath || len(pkg.Syntax) != len(ref.Syntax) {
		t.Errorf("LoadFiles = %s %s (%d files), want %s %s (%d files)", pkg.Name, pkg.PkgPath, len(pkg.Syntax), ref.Name, ref.PkgPath, len(ref.Syntax))
	}
	got := mutator.Generate(pkg, mutator.Options{TypeCheck: true})
	if len(got) != len(want) {
		t.Fatalf("got %d mutants, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i].ID != want[i].ID || got[i].Viable != want[i].Viable || got[i].Description != want[i].Description {
			t.Errorf("mutant %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	if len(pkg.IgnoredFiles) != 0 {
		t.Errorf("no file is constrained, but %v were ignored", pkg.IgnoredFiles)
	}
}

// Files whose build constraints exclude them are ignored rather than
// type-checked, and files that import a package with no export data fail.
func TestLoadFilesConstraintsAndErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	main := write("lib.go", "package lib\n\nfunc Less(a, b int) bool { return a < b }\n")
	other := write("lib_other.go", "//go:build never\n\npackage lib\n\nimport \"nonexistent/pkg\"\n\nvar _ = pkg.X\n")
	tagged := write("lib_tagged.go", "//go:build mutrimtag\n\npackage lib\n\nfunc Tagged() {}\n")
	external := write("ext.go", "package lib\n\nimport \"example.com/missing\"\n\nvar _ = missing.X\n")

	pkg, err := mutator.LoadFiles(mutator.FilesConfig{ImportPath: "example.com/lib", Files: []string{main, other, tagged}})
	if err != nil {
		t.Fatal(err)
	}
	if len(pkg.GoFiles) != 1 || len(pkg.IgnoredFiles) != 2 {
		t.Errorf("GoFiles = %v, IgnoredFiles = %v", pkg.GoFiles, pkg.IgnoredFiles)
	}
	if ms := mutator.Generate(pkg, mutator.Options{TypeCheck: true}); len(ms) == 0 {
		t.Error("no mutants generated for Less")
	}

	pkg, err = mutator.LoadFiles(mutator.FilesConfig{ImportPath: "example.com/lib", Files: []string{main, tagged}, Tags: []string{"mutrimtag"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(pkg.GoFiles) != 2 {
		t.Errorf("-tags did not include the tagged file: %v", pkg.GoFiles)
	}

	cases := map[string]mutator.FilesConfig{
		"no files":             {ImportPath: "example.com/lib"},
		"no import path":       {Files: []string{main}},
		"all files excluded":   {ImportPath: "example.com/lib", Files: []string{other}},
		"missing export data":  {ImportPath: "example.com/lib", Files: []string{main, external}},
		"missing importcfg":    {ImportPath: "example.com/lib", Files: []string{main}, Importcfg: filepath.Join(dir, "missing")},
		"malformed importcfg":  {ImportPath: "example.com/lib", Files: []string{main}, Importcfg: write("bad.cfg", "packagefile nope\n")},
		"unknown directive":    {ImportPath: "example.com/lib", Files: []string{main}, Importcfg: write("bad2.cfg", "bogus a=b\n")},
		"stdlib without match": {ImportPath: "example.com/lib", Files: []string{main, write("fmt.go", "package lib\n\nimport \"fmt\"\n\nvar _ = fmt.Sprint\n")}, Stdlib: dir},
		"unparseable file":     {ImportPath: "example.com/lib", Files: []string{write("broken.go", "package lib\n\nfunc {\n")}},
	}
	for name, cfg := range cases {
		if _, err := mutator.LoadFiles(cfg); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
