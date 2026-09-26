package mutator_test

import (
	"strings"
	"testing"

	"github.com/illumination-k/mutrim/mutator"
)

// extras are proposals against testdata/extra, keyed by what each checks.
var extras = map[string]mutator.Extra{
	"expr":        {File: "extra.go", Func: "Clamp", Line: 11, Original: "hi", Replacement: "hi - 1"},
	"stmt":        {File: "extra.go", Func: "Words", Original: "n++", Replacement: "n += 2", Description: "double count"},
	"duplicate":   {File: "extra.go", Func: "Clamp", Original: "x  >  hi", Replacement: "x >= hi"},
	"identical":   {File: "extra.go", Func: "Clamp", Original: "x < lo", Replacement: "x<lo"},
	"type":        {File: "testdata/extra/extra.go", Func: "Clamp", Line: 8, Original: "lo", Replacement: `"lo"`},
	"undefined":   {File: "extra.go", Func: "Clamp", Line: 13, Original: "x", Replacement: "y"},
	"unused":      {File: "extra.go", Func: "Words", Original: "n++", Replacement: "m := 1"},
	"declaring":   {File: "extra.go", Func: "Words", Original: "n := 0", Replacement: "n := 1"},
	"notfound":    {File: "extra.go", Original: "y + 1", Replacement: "y"},
	"ambiguous":   {File: "extra.go", Func: "Clamp", Original: "x", Replacement: "lo"},
	"nofile":      {File: "other.go", Original: "x", Replacement: "y"},
	"unparseable": {File: "extra.go", Original: "if {", Replacement: "x"},
}

func TestGenerateExtra(t *testing.T) {
	names := []string{"expr", "stmt", "duplicate", "identical", "type", "undefined", "unused", "declaring"}
	pkg := load(t, "extra")
	generated := mutator.Generate(pkg, mutator.Options{TypeCheck: true, Diff: mutator.DiffStmt})
	in := make([]mutator.Extra, 0, len(names))
	for _, name := range names {
		in = append(in, extras[name])
	}
	ms, errs := mutator.GenerateExtra(pkg, generated, in, mutator.Options{TypeCheck: true, Diff: mutator.DiffStmt})
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(ms) != len(names) {
		t.Fatalf("got %d mutants, want %d: %+v", len(ms), len(names), ms)
	}
	got := map[string]mutator.Mutant{}
	for i, name := range names {
		got[name] = ms[i] // one mutant per entry, in entry order
	}

	for name, m := range got {
		if m.Operator != mutator.ExtraOperator || m.Class != mutator.ClassDefault {
			t.Errorf("%s: operator %q class %q", name, m.Operator, m.Class)
		}
	}
	if m := got["expr"]; !m.Viable || m.Excluded() || m.Description != "hi -> hi-1" || m.Line != 11 || !strings.Contains(m.Diff, "+\t\treturn hi - 1") {
		t.Errorf("expr: %+v", m)
	}
	if m := got["stmt"]; !m.Viable || m.Excluded() || m.Description != "double count" || !strings.Contains(m.Diff, "+\t\tn += 2") {
		t.Errorf("stmt: %+v", m)
	}
	var relational string
	for _, g := range generated {
		if g.Func == "Clamp" && g.Description == "> -> >=" {
			relational = g.ID
		}
	}
	if m := got["duplicate"]; m.Ignored != "duplicate" || m.Reason != "of "+relational || m.Diff != "" {
		t.Errorf("duplicate: want ignored as duplicate of %s, got %+v", relational, m)
	}
	if m := got["identical"]; m.Equivalent != "identical" {
		t.Errorf("identical: %+v", m)
	}
	for name, want := range map[string]string{
		"type":      "replacement has type untyped string, want int",
		"undefined": "undefined: y",
		"unused":    "declared and not used: m",
		"declaring": "a declaring statement cannot be replaced",
	} {
		if m := got[name]; m.Viable || !strings.Contains(m.Reason, want) {
			t.Errorf("%s: want not viable with %q, got viable=%v reason=%q", name, want, m.Viable, m.Reason)
		}
	}
	if got["expr"].ID == got["type"].ID {
		t.Error("two rewrites share an ID")
	}
}

func TestGenerateExtraErrors(t *testing.T) {
	pkg := load(t, "extra")
	for name, want := range map[string]string{
		"notfound":    "original not found",
		"ambiguous":   "original matches 3 sites",
		"nofile":      "no file other.go",
		"unparseable": "original:",
	} {
		t.Run(name, func(t *testing.T) {
			ms, errs := mutator.GenerateExtra(pkg, nil, []mutator.Extra{extras[name]}, mutator.Options{TypeCheck: true})
			if len(ms) != 0 || len(errs) != 1 || !strings.Contains(errs[0].Error(), want) {
				t.Errorf("want one error with %q, got %v %v", want, ms, errs)
			}
		})
	}
}

func TestPackageExtras(t *testing.T) {
	pkg := load(t, "extra")
	mine, rest := mutator.PackageExtras(pkg, []mutator.Extra{extras["expr"], extras["type"], extras["nofile"]})
	if len(mine) != 2 || len(rest) != 1 || rest[0].File != "other.go" {
		t.Errorf("mine=%v rest=%v", mine, rest)
	}
}

// TestSchemataExtra embeds extra mutants next to the generated ones: the
// source must behave like the original, and each extra mutant must fail
// the fixture's tests when selected.
func TestSchemataExtra(t *testing.T) {
	pkg := load(t, "extra")
	generated := mutator.Generate(pkg, mutator.Options{TypeCheck: true})
	ms, errs := mutator.GenerateExtra(pkg, generated, []mutator.Extra{extras["expr"], extras["stmt"]}, mutator.Options{TypeCheck: true})
	if len(errs) > 0 || len(ms) != 2 {
		t.Fatal(ms, errs)
	}
	sch, err := mutator.Lower(pkg, append(generated, ms...), mutator.Blocks(pkg), mutator.RuntimePath)
	if err != nil {
		t.Fatal(err)
	}
	overlayPath := overlayFor(t, sch)
	if out, err := goTestOverlay(t, "extra", overlayPath, ""); err != nil {
		t.Fatalf("schemata source must pass the fixture tests: %v\n%s", err, out)
	}
	for _, m := range ms {
		if !sch.Embedded[m.ID] {
			t.Errorf("%s: not embedded", m.Description)
			continue
		}
		if out, err := goTestOverlay(t, "extra", overlayPath, m.ID); err == nil {
			t.Errorf("%s should have been killed\n%s", m.Description, out)
		}
	}
}
