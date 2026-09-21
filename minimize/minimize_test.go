package minimize_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/illumination-k/mutrim/criteria"
	"github.com/illumination-k/mutrim/minimize"
)

func names(sel []minimize.Selection) []string {
	out := make([]string, len(sel))
	for i, s := range sel {
		out[i] = s.Name
	}
	return out
}

// The cover keeps the tests that satisfy something new, prefers weight per
// millisecond, and explains each redundant test by its subsumers.
func TestGreedy(t *testing.T) {
	sites := criteria.SiteCoverage{
		"TestAll":   {"a", "b", "c"},
		"TestA":     {"a"},
		"TestB":     {"b"},
		"TestD":     {"d"},
		"TestFast":  {"a", "b"},
		"TestEmpty": nil,
	}
	kills := criteria.Mutation{"TestAll": {"a"}, "TestA": {"a"}, "TestB": {"b"}}
	durations := map[string]int64{"TestAll": 100, "TestA": 1, "TestB": 1, "TestD": 1, "TestFast": 1}
	m := criteria.Compose(durations,
		criteria.Weighted{Criterion: sites, Weight: 1},
		criteria.Weighted{Criterion: kills, Weight: 5},
	)

	res := minimize.Greedy(m, minimize.Options{})
	// TestA (6/1) beats TestFast (2/1) and TestAll (8/100); TestB is then
	// the only test with a kill left; TestAll's site c is still unique but
	// slow, so TestD comes before it.
	if got, want := names(res.Selected), []string{"TestA", "TestB", "TestD", "TestAll"}; !slices.Equal(got, want) {
		t.Errorf("selected = %v, want %v", got, want)
	}
	if s := res.Selected[0]; s.New != 2 || s.Gain != 6 || s.Protected {
		t.Errorf("first selection = %+v", s)
	}
	if s := res.Selected[3]; s.New != 1 || s.Gain != 0.01 {
		t.Errorf("TestAll selection = %+v", s)
	}
	want := []minimize.Redundancy{
		{Name: "TestEmpty", SubsumedBy: []string{"TestA", "TestAll", "TestB", "TestD"}},
		{Name: "TestFast", SubsumedBy: []string{"TestAll"}},
	}
	if len(res.Redundant) != len(want) {
		t.Fatalf("redundant = %+v, want %+v", res.Redundant, want)
	}
	for i := range want {
		if res.Redundant[i].Name != want[i].Name || !slices.Equal(res.Redundant[i].SubsumedBy, want[i].SubsumedBy) {
			t.Errorf("redundant[%d] = %+v, want %+v", i, res.Redundant[i], want[i])
		}
	}
}

// Protected tests come first, and whatever they satisfy no longer counts
// as a gain for the others.
func TestGreedyProtected(t *testing.T) {
	m := criteria.Compose(nil, criteria.Weighted{
		Criterion: criteria.SiteCoverage{"TestA": {"a"}, "TestRegression_A": {"a"}, "TestB": {"b"}},
		Weight:    1,
	})
	keep := regexp.MustCompile(`^TestRegression_`)
	res := minimize.Greedy(m, minimize.Options{Protected: keep.MatchString})
	if got, want := names(res.Selected), []string{"TestRegression_A", "TestB"}; !slices.Equal(got, want) {
		t.Errorf("selected = %v, want %v", got, want)
	}
	if !res.Selected[0].Protected || res.Selected[1].Protected {
		t.Errorf("protected flags wrong: %+v", res.Selected)
	}
	if len(res.Redundant) != 1 || res.Redundant[0].Name != "TestA" || !slices.Equal(res.Redundant[0].SubsumedBy, []string{"TestRegression_A"}) {
		t.Errorf("redundant = %+v", res.Redundant)
	}

	empty := minimize.Greedy(&criteria.Matrix{}, minimize.Options{})
	if empty.Selected == nil || empty.Redundant == nil || len(empty.Selected)+len(empty.Redundant) != 0 {
		t.Errorf("empty matrix: %+v", empty)
	}
}

// A test with a zero duration is not infinitely cheap: it counts as 1ms,
// so a faster real test with the same coverage still wins.
func TestGreedyUnknownDuration(t *testing.T) {
	m := criteria.Compose(map[string]int64{"TestKnown": 1}, criteria.Weighted{
		Criterion: criteria.SiteCoverage{"TestKnown": {"a"}, "TestUnknown": {"a"}},
		Weight:    1,
	})
	res := minimize.Greedy(m, minimize.Options{})
	if got := names(res.Selected); !slices.Equal(got, []string{"TestKnown"}) {
		t.Errorf("selected = %v (tie broken by name)", got)
	}
}

func TestTagged(t *testing.T) {
	dir := t.TempDir()
	src := `package p

import "testing"

// TestKept exercises the parser.
//
//mutrim:keep
func TestKept(t *testing.T) {}

func TestPlain(t *testing.T) {}

// mutrim:keep on a helper, not a test.
func helper() {}

type T struct{}

// mutrim:keep on a method, not a test.
func (T) TestMethod(t *testing.T) {}
`
	if err := os.WriteFile(filepath.Join(dir, "p_test.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p.go"), []byte("package p\n\n// mutrim:keep\nfunc TestNotATestFile() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := minimize.Tagged([]string{dir}, "mutrim:keep")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"TestKept"}) {
		t.Errorf("Tagged = %v, want [TestKept]", got)
	}
	if got, err = minimize.Tagged([]string{filepath.Join(dir, "p_test.go")}, "mutrim:keep"); err != nil || !slices.Equal(got, []string{"TestKept"}) {
		t.Errorf("Tagged(file) = %v, %v", got, err)
	}
	if _, err := minimize.Tagged([]string{filepath.Join(dir, "missing")}, "x"); err == nil {
		t.Error("missing path: expected an error")
	}
	if err := os.WriteFile(filepath.Join(dir, "bad_test.go"), []byte("package {"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := minimize.Tagged([]string{dir}, "x"); err == nil {
		t.Error("unparsable file: expected an error")
	}
}
