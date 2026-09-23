package runner_test

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/illumination-k/mutrim/mutator"
	"github.com/illumination-k/mutrim/runner"
)

// TestRunFallback runs the mutants the schemata fixture cannot embed
// through builds of their own: each is traced by its probe, run only
// against the tests reaching it, and killed like an embedded one; a
// fallback that does not compile is NOT_VIABLE.
func TestRunFallback(t *testing.T) {
	bin, all, _, probed := buildPkgWith(t, fixtureDir, mutator.LowerFallback)
	pkgs, err := mutator.Load(".", fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	srcs, err := mutator.Sources(pkgs[0], slices.Sorted(maps.Keys(probed)), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	writeOverlay := func(id, orig string, src []byte) string {
		t.Helper()
		mutated := filepath.Join(out, id+".go")
		if werr := os.WriteFile(mutated, src, 0o600); werr != nil {
			t.Fatal(werr)
		}
		data, merr := json.Marshal(mutator.Overlay{Replace: map[string]string{orig: mutated}})
		if merr != nil {
			t.Fatal(merr)
		}
		path := filepath.Join(out, id+".json")
		if werr := os.WriteFile(path, data, 0o600); werr != nil {
			t.Fatal(werr)
		}
		return path
	}

	want := map[string]runner.Status{
		"Classify case body -> empty": runner.Killed,  // a case body ending in return
		"SkipConst * -> /":            runner.Killed,  // a constant expression
		"Drain default body -> empty": runner.Timeout, // the loop never ends
		"SkipMapPost ++ -> --":        runner.NotViable,
	}
	var mutants []mutator.Mutant
	for _, m := range all {
		key := m.Func + " " + m.Description
		if _, ok := want[key]; !ok {
			continue
		}
		if !probed[m.ID] {
			t.Fatalf("%s: not probed", key)
		}
		src := srcs[m.ID]
		if key == "SkipMapPost ++ -> --" {
			src.Src = []byte("package schemata\n\nfunc broken() int { return \"\" }\n")
		}
		m.Fallback = writeOverlay(m.ID, src.File, src.Src)
		mutants = append(mutants, m)
	}

	report, err := runner.Run(t.Context(), runner.Options{
		TestBin: bin,
		Mutants: mutants,
		Dir:     fixtureDir,
		Tests:   []string{"TestStatements", "TestSkipped", "TestBranches"},
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	killers := map[string][]string{
		"Classify case body -> empty": {"TestStatements"},
		"SkipConst * -> /":            {"TestSkipped"},
		"Drain default body -> empty": {"TestBranches"},
	}
	for i, r := range report.Results {
		key := mutants[i].Func + " " + mutants[i].Description
		if r.Status != want[key] {
			t.Errorf("%s: %s, want %s", key, r.Status, want[key])
		}
		if !slices.Equal(r.KilledBy, killers[key]) {
			t.Errorf("%s: killed by %v, want %v", key, r.KilledBy, killers[key])
		}
	}
}
