package mutator_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/illumination-k/mutrim/mutator"
)

// counts renders, per ignore reason, how many mutants carry it; kept
// mutants count under "".
func counts(ms []mutator.Mutant) map[string]int {
	out := map[string]int{}
	for _, m := range ms {
		out[m.Ignored]++
	}
	return out
}

// A filter never changes which sites exist, only whether they are
// ignored: the mutant IDs and their order are those of an unfiltered run,
// so counts stay comparable.
func TestFilterIgnoresRatherThanDrops(t *testing.T) {
	pkg := load(t, "control")
	all := mutator.Generate(pkg, mutator.Options{TypeCheck: true})
	filter, err := mutator.FilterSpec{Match: `^Sum$`}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	filtered := mutator.Generate(pkg, mutator.Options{TypeCheck: true, Filter: filter})

	if len(all) != len(filtered) {
		t.Fatalf("mutant count changed: %d -> %d", len(all), len(filtered))
	}
	kept := 0
	for i, m := range filtered {
		if m.ID != all[i].ID {
			t.Errorf("%d: id %s -> %s", i, all[i].ID, m.ID)
		}
		switch {
		case m.Func == "Sum":
			kept++
			if m.Ignored != "" {
				t.Errorf("%s %s: ignored=%q, want kept", m.Func, m.Description, m.Ignored)
			}
		case m.Ignored != "match":
			t.Errorf("%s %s: ignored=%q, want match", m.Func, m.Description, m.Ignored)
		}
	}
	if kept == 0 {
		t.Fatal("-match kept nothing")
	}
}

// Each rule selects what its flag documents, and an ignored mutant is
// never embedded as schemata.
func TestFilterRules(t *testing.T) {
	cases := map[string]struct {
		spec mutator.FilterSpec
		want string // reason the mutants of (*Counter).Inc carry
	}{
		"no filter":            {mutator.FilterSpec{}, ""},
		"match method":         {mutator.FilterSpec{Match: `^\(\*Counter\)\.`}, ""},
		"match other":          {mutator.FilterSpec{Match: `^Sum$`}, "match"},
		"files base name":      {mutator.FilterSpec{Files: "control.go"}, ""},
		"files glob":           {mutator.FilterSpec{Files: "*_test.go, testdata/control/*.go"}, ""},
		"files other":          {mutator.FilterSpec{Files: "other/*.go"}, "files"},
		"exclude files":        {mutator.FilterSpec{ExcludeFiles: "testdata/other,control/control"}, "exclude-files"},
		"exclude other files":  {mutator.FilterSpec{ExcludeFiles: "does-not-exist"}, ""},
		"exclude operator":     {mutator.FilterSpec{ExcludeRE: `incdec: \+\+ -> --`}, "exclude-re"},
		"exclude other mutant": {mutator.FilterSpec{ExcludeRE: `^Sum relational`}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			pkg := load(t, "control") // Lower below rewrites it, so reload per case
			filter, err := tc.spec.Compile()
			if err != nil {
				t.Fatal(err)
			}
			ms := mutator.Generate(pkg, mutator.Options{TypeCheck: true, Filter: filter})
			var inc []mutator.Mutant
			for _, m := range ms {
				if m.Func == "(*Counter).Inc" {
					inc = append(inc, m)
				}
			}
			if len(inc) == 0 {
				t.Fatal("no mutant of (*Counter).Inc")
			}
			for _, m := range inc {
				if m.Ignored != tc.want {
					t.Errorf("%s %q: ignored=%q, want %q", m.Func, m.Description, m.Ignored, tc.want)
				}
			}
			sch, err := mutator.Lower(pkg, ms)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range ms {
				if m.Ignored != "" && sch.Embedded[m.ID] {
					t.Errorf("%s %q: ignored mutants must not be embedded", m.Func, m.Description)
				}
			}
		})
	}
}

// An ignored mutant skips the go/types pre-filter, so it is reported as
// it was found rather than as viable or not.
func TestFilterSkipsTypeCheck(t *testing.T) {
	pkg := load(t, "arith")
	all := mutator.Generate(pkg, mutator.Options{TypeCheck: true})
	notViable := 0
	for _, m := range all {
		if !m.Viable {
			notViable++
		}
	}
	if notViable == 0 {
		t.Fatal("the arith fixture must contain a mutant the type check rejects")
	}
	filter, err := mutator.FilterSpec{Files: "*.go"}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if got := counts(mutator.Generate(pkg, mutator.Options{TypeCheck: true, Filter: filter}))[""]; got != len(all) {
		t.Errorf("a glob matching every file kept %d of %d mutants", got, len(all))
	}
	filter, err = mutator.FilterSpec{Files: "nothing/*.go"}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mutator.Generate(pkg, mutator.Options{TypeCheck: true, Filter: filter}) {
		if !m.Viable {
			t.Errorf("%s %q: an ignored mutant must not be type-checked", m.Func, m.Description)
		}
	}
}

func TestFilterSpecErrors(t *testing.T) {
	cases := map[string]mutator.FilterSpec{
		"match":         {Match: "("},
		"exclude-re":    {ExcludeRE: "*"},
		"exclude-files": {ExcludeFiles: "ok,("},
		"files":         {Files: "ok.go,[-]"},
	}
	for rule, spec := range cases {
		_, err := spec.Compile()
		if err == nil {
			t.Errorf("%s: expected an error", rule)
		} else if !strings.Contains(err.Error(), rule) {
			t.Errorf("%s: error does not name the rule: %v", rule, err)
		}
	}
	if f, err := (mutator.FilterSpec{Files: " , ", ExcludeFiles: ""}).Compile(); err != nil {
		t.Errorf("empty entries: %v", err)
	} else if len(f.Files) != 0 {
		t.Errorf("empty entries became patterns: %v", f.Files)
	}
}

// The default exclude-calls list covers a logging call, the expressions it
// logs, and a method of a logger, and nothing else. The fixture marks the
// lines it must cover with a "// logged" comment.
func TestFilterExcludeCallsDefault(t *testing.T) {
	pkg := load(t, "logging")
	filter, err := mutator.FilterSpec{}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	logged := loggedLines(t)
	ignored, kept := 0, 0
	for _, m := range mutator.Generate(pkg, mutator.Options{TypeCheck: true, Filter: filter}) {
		want := ""
		if logged[m.Line] {
			want = "exclude-calls"
		}
		if m.Ignored != want {
			t.Errorf("%s:%d %s %q: ignored=%q, want %q", filepath.Base(m.File), m.Line, m.Func, m.Description, m.Ignored, want)
		}
		if want == "" {
			kept++
		} else {
			ignored++
		}
	}
	if ignored == 0 || kept == 0 {
		t.Fatalf("the logging fixture must have both kinds of mutant: %d ignored, %d kept", ignored, kept)
	}
}

// loggedLines reads the lines of the logging fixture the default list must
// cover, marked with a trailing "// logged" comment.
func loggedLines(t *testing.T) map[int]bool {
	t.Helper()
	src, err := os.ReadFile(filepath.Clean("testdata/logging/logging.go"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[int]bool{}
	for i, line := range strings.Split(string(src), "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), "// logged") {
			out[i+1] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("the logging fixture marks no line")
	}
	return out
}

// An explicit list replaces the default one, "default" extends it and
// "none" excludes no call at all.
func TestFilterExcludeCallsSpec(t *testing.T) {
	// Functions whose mutants the spec must ignore; the others keep all
	// of theirs.
	cases := map[string]struct {
		spec    string
		ignored []string
	}{
		"default":     {"", []string{"Retry", "Report"}},
		"named":       {"default", []string{"Retry", "Report"}},
		"none":        {"none", nil},
		"other calls": {"fmt.*", []string{"Describe"}},
		"extended":    {"default, fmt.*", []string{"Retry", "Report", "Describe"}},
		"import path": {"log/slog.*", []string{"Retry"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			pkg := load(t, "logging")
			filter, err := mutator.FilterSpec{ExcludeCalls: tc.spec}.Compile()
			if err != nil {
				t.Fatal(err)
			}
			ignored := map[string]int{}
			for _, m := range mutator.Generate(pkg, mutator.Options{TypeCheck: true, Filter: filter}) {
				if m.Ignored == "exclude-calls" {
					ignored[m.Func]++
				} else if m.Ignored != "" {
					t.Errorf("%s %q: ignored=%q, want exclude-calls or kept", m.Func, m.Description, m.Ignored)
				}
			}
			for _, fn := range tc.ignored {
				if ignored[fn] == 0 {
					t.Errorf("%s: no mutant ignored, want some", fn)
				}
				delete(ignored, fn)
			}
			for fn, n := range ignored {
				t.Errorf("%s: %d mutants ignored, want none", fn, n)
			}
		})
	}
}

func TestFilterExcludeCallsErrors(t *testing.T) {
	cases := map[string]string{
		"bad glob":       "log.[-]",
		"none with more": "none,log.*",
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := mutator.FilterSpec{ExcludeCalls: spec}.Compile()
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), "exclude-calls") {
				t.Errorf("error does not name the rule: %v", err)
			}
		})
	}
}
