package criteria_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/illumination-k/mutrim/criteria"
)

func labels(m *criteria.Matrix) []string {
	out := make([]string, len(m.Requirements))
	for i, r := range m.Requirements {
		out[i] = r.Label
	}
	return out
}

func covers(m *criteria.Matrix, t criteria.Test) string {
	var b strings.Builder
	for i, ok := t.Covers.NextSet(0); ok; i, ok = t.Covers.NextSet(i + 1) {
		b.WriteString(m.Requirements[i].Label + " ")
	}
	return strings.TrimSpace(b.String())
}

func TestCompose(t *testing.T) {
	sites := criteria.SiteCoverage{"TestB": {"m2", "m1"}, "TestA": {"m1"}}
	kills := criteria.Mutation{"TestB": {"m2"}}
	m := criteria.Compose(map[string]int64{"TestA": 3, "TestB": 0, "TestC": 7},
		criteria.Weighted{Criterion: sites, Weight: 1},
		criteria.Weighted{Criterion: kills, Weight: 5},
	)

	if got, want := strings.Join(labels(m), " "), "kill:m2 site:m1 site:m2"; got != want {
		t.Errorf("requirements = %q, want %q", got, want)
	}
	if m.Requirements[0].Weight != 5 || m.Requirements[1].Weight != 1 {
		t.Errorf("weights not taken from the criterion: %+v", m.Requirements)
	}
	want := map[string]string{"TestA": "site:m1", "TestB": "kill:m2 site:m1 site:m2", "TestC": ""}
	if len(m.Tests) != 3 {
		t.Fatalf("tests = %+v", m.Tests)
	}
	for i, name := range []string{"TestA", "TestB", "TestC"} {
		if m.Tests[i].Name != name {
			t.Errorf("tests[%d] = %s, want %s (sorted by name)", i, m.Tests[i].Name, name)
		}
		if got := covers(m, m.Tests[i]); got != want[name] {
			t.Errorf("%s covers %q, want %q", name, got, want[name])
		}
	}
	if m.Tests[0].DurationMS != 3 || m.Tests[2].DurationMS != 7 {
		t.Errorf("durations lost: %+v", m.Tests)
	}
}

// The JSON form lists requirement indices per test and round-trips.
func TestMatrixJSON(t *testing.T) {
	m := criteria.Compose(nil, criteria.Weighted{Criterion: criteria.Mutation{"TestA": {"x", "y"}, "TestB": {}}, Weight: 2})
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"requirements":[{"label":"kill:x","weight":2},{"label":"kill:y","weight":2}],"tests":[{"name":"TestA","duration_ms":0,"covers":[0,1]},{"name":"TestB","duration_ms":0,"covers":[]}]}`
	if string(data) != want {
		t.Errorf("json = %s\nwant   %s", data, want)
	}
	var back criteria.Matrix
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if got := covers(&back, back.Tests[0]); got != "kill:x kill:y" {
		t.Errorf("round trip lost bits: %q", got)
	}
	if !back.Tests[1].Covers.None() {
		t.Error("round trip set bits for an empty row")
	}
}
