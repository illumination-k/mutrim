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
	minimize.Exclusives(m, &res)
	// The greedy order is TestA (6/1), TestB (the only kill left), TestD,
	// then TestAll for its site c (8/100). TestAll then satisfies
	// everything TestA did, so TestA is pruned and the rest replayed.
	if got, want := names(res.Selected), []string{"TestB", "TestD", "TestAll"}; !slices.Equal(got, want) {
		t.Errorf("selected = %v, want %v", got, want)
	}
	if s := res.Selected[0]; s.New != 2 || s.Gain != 6 || s.Protected {
		t.Errorf("first selection = %+v", s)
	}
	if s := res.Selected[2]; s.New != 3 || s.Gain != 0.07 {
		t.Errorf("TestAll selection = %+v", s)
	}
	// TestB is the only killer of mutant b, TestD the only test on site d
	// and TestAll the only one on site c: every selected test is essential,
	// reported with the labels of the requirements no other test satisfies.
	essential := func(i int, name string, labels ...string) {
		if s := res.Selected[i]; s.Name != name || !s.Essential || !slices.Equal(s.Unique, labels) || s.UniqueCount != len(labels) {
			t.Errorf("%s = %+v, want essential with unique %v", name, s, labels)
		}
	}
	essential(0, "TestB", "kill:b")
	essential(1, "TestD", "site:d")
	essential(2, "TestAll", "site:c")
	want := []minimize.Redundancy{
		{Name: "TestA", SubsumedBy: []string{"TestAll"}, SharedWith: 2},
		{Name: "TestEmpty", SubsumedBy: []string{"TestAll", "TestB", "TestD"}, SharedWith: 0},
		{Name: "TestFast", SubsumedBy: []string{"TestAll"}, SharedWith: 3},
	}
	if len(res.Redundant) != len(want) {
		t.Fatalf("redundant = %+v, want %+v", res.Redundant, want)
	}
	for i := range want {
		if res.Redundant[i].Name != want[i].Name || !slices.Equal(res.Redundant[i].SubsumedBy, want[i].SubsumedBy) || res.Redundant[i].SharedWith != want[i].SharedWith {
			t.Errorf("redundant[%d] = %+v, want %+v", i, res.Redundant[i], want[i])
		}
	}
}

// Protected tests come first, and whatever they satisfy no longer counts
// as a gain for the others. The report still sorts them behind the
// essential test (TestB is the only test on site b).
func TestGreedyProtected(t *testing.T) {
	m := criteria.Compose(nil, criteria.Weighted{
		Criterion: criteria.SiteCoverage{"TestA": {"a"}, "TestRegression_A": {"a"}, "TestB": {"b"}},
		Weight:    1,
	})
	keep := regexp.MustCompile(`^TestRegression_`)
	res := minimize.Greedy(m, minimize.Options{Protected: keep.MatchString})
	minimize.Exclusives(m, &res)
	if got, want := names(res.Selected), []string{"TestB", "TestRegression_A"}; !slices.Equal(got, want) {
		t.Errorf("selected = %v, want %v", got, want)
	}
	if res.Selected[0].Protected || !res.Selected[1].Protected {
		t.Errorf("protected flags wrong: %+v", res.Selected)
	}
	if !res.Selected[0].Essential || res.Selected[1].Essential {
		t.Errorf("essential flags wrong: %+v", res.Selected)
	}
	if !slices.Equal(res.Selected[0].Unique, []string{"site:b"}) || res.Selected[1].UniqueCount != 0 {
		t.Errorf("unique = %v, %v", res.Selected[0].Unique, res.Selected[1].Unique)
	}
	if len(res.Redundant) != 1 || res.Redundant[0].Name != "TestA" || !slices.Equal(res.Redundant[0].SubsumedBy, []string{"TestRegression_A"}) || res.Redundant[0].SharedWith != 1 {
		t.Errorf("redundant = %+v", res.Redundant)
	}

	zero := &criteria.Matrix{}
	empty := minimize.Greedy(zero, minimize.Options{})
	minimize.Exclusives(zero, &empty)
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
	minimize.Exclusives(m, &res)
	if got := names(res.Selected); !slices.Equal(got, []string{"TestKnown"}) {
		t.Errorf("selected = %v (tie broken by name)", got)
	}
	// TestKnown satisfies nothing alone: TestUnknown covers the same
	// site, and TestUnknown's single requirement is shared with TestKnown
	// only — one deletion away from essential.
	if res.Selected[0].Essential || res.Selected[0].UniqueCount != 0 {
		t.Errorf("TestKnown = %+v, want no unique requirement", res.Selected[0])
	}
	if res.Redundant[0].SharedWith != 1 {
		t.Errorf("TestUnknown = %+v, want its requirements shared with TestKnown alone", res.Redundant[0])
	}
}

// Of two tests with the same requirements the first selected stays; a
// protected test is never pruned even when another selected test
// satisfies all of its requirements.
func TestGreedyPrunesSubsumedSelections(t *testing.T) {
	sites := criteria.SiteCoverage{"TestFast": {"a"}, "TestBroad": {"a", "b"}, "TestTwin": {"a", "b"}, "TestRegression_A": {"a"}}
	m := criteria.Compose(map[string]int64{"TestFast": 1, "TestBroad": 10, "TestTwin": 10}, criteria.Weighted{Criterion: sites, Weight: 1})
	keep := regexp.MustCompile(`^TestRegression_`)
	res := minimize.Greedy(m, minimize.Options{Protected: keep.MatchString})
	minimize.Exclusives(m, &res)
	// TestFast (1/1) is picked before TestBroad (1/10 for b), then pruned.
	if got, want := names(res.Selected), []string{"TestRegression_A", "TestBroad"}; !slices.Equal(got, want) {
		t.Errorf("selected = %v, want %v", got, want)
	}
	if s := res.Selected[1]; s.New != 1 || s.Gain != 0.1 {
		t.Errorf("replayed selection = %+v", s)
	}
	if len(res.Redundant) != 2 || res.Redundant[0].Name != "TestFast" || res.Redundant[1].Name != "TestTwin" {
		t.Errorf("redundant = %+v", res.Redundant)
	}
	if !slices.Equal(res.Redundant[0].SubsumedBy, []string{"TestBroad", "TestRegression_A"}) || !slices.Equal(res.Redundant[1].SubsumedBy, []string{"TestBroad"}) {
		t.Errorf("subsumed_by = %+v", res.Redundant)
	}
	// No selected test is essential here: TestRegression_A and TestBroad
	// share every requirement with TestFast or TestTwin. Each redundant
	// test's requirements are shared with the selected tests and the other
	// redundant one.
	if res.Selected[0].Essential || res.Selected[1].Essential {
		t.Errorf("essential flags wrong: %+v", res.Selected)
	}
	if res.Redundant[0].SharedWith != 3 || res.Redundant[1].SharedWith != 3 {
		t.Errorf("shared_with = %d, %d; want 3 and 3 (the selected tests and each other)", res.Redundant[0].SharedWith, res.Redundant[1].SharedWith)
	}
}

// Adjacent requirement bits are all counted: the constant mutant of the
// bit iteration (NextSet(i + 2)) surfaced that no test pinned this.
func TestGreedyGainCountsEveryRequirement(t *testing.T) {
	m := criteria.Compose(map[string]int64{"TestAll": 1}, criteria.Weighted{Criterion: criteria.SiteCoverage{"TestAll": {"a", "b", "c"}}, Weight: 2})
	res := minimize.Greedy(m, minimize.Options{})
	minimize.Exclusives(m, &res)
	if len(res.Selected) != 1 || res.Selected[0].New != 3 || res.Selected[0].Gain != 6 {
		t.Errorf("selected = %+v, want TestAll with 3 new requirements and gain 6", res.Selected)
	}
	if !res.Selected[0].Essential || !slices.Equal(res.Selected[0].Unique, []string{"site:a", "site:b", "site:c"}) {
		t.Errorf("TestAll = %+v, want essential on three unique requirements", res.Selected[0])
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

// TestDocumented has a doc comment, but not the tag.
func TestDocumented(t *testing.T) {}

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
