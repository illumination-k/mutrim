package ingest

import (
	"encoding/json/v2"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// strykerReport is the part of a mutation-testing-report-schema report
// the adapter reads.
type strykerReport struct {
	Files     map[string]strykerFile `json:"files"`
	TestFiles map[string]struct {
		Tests []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"tests"`
	} `json:"testFiles"`
}

type strykerFile struct {
	Mutants []struct {
		ID          string   `json:"id"`
		MutatorName string   `json:"mutatorName"`
		Replacement string   `json:"replacement"`
		Status      string   `json:"status"`
		CoveredBy   []string `json:"coveredBy"`
		KilledBy    []string `json:"killedBy"`
		Location    struct {
			Start strykerPos `json:"start"`
			End   strykerPos `json:"end"`
		} `json:"location"`
	} `json:"mutants"`
}

type strykerPos struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

// Stryker reads a mutation-testing-report-schema JSON report (StrykerJS's
// json reporter, Stryker.NET's, ...). A mutant's location is the site its
// coveredBy tests reach, and it is killed by its killedBy tests: the
// report needs per-test coverage (coverageAnalysis "perTest") for the
// first, and every killing test rather than the first (disableBail) for
// the second, or the kill matrix holds one test per mutant. A Survived or
// NoCoverage mutant is a survivor; a Timeout names no test and is left out.
func Stryker(data []byte) (*Observations, error) {
	var r strykerReport
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("stryker: %w", err)
	}
	if r.TestFiles == nil {
		return nil, fmt.Errorf("stryker: no testFiles: the report lists no tests")
	}
	names := map[string]string{}
	for file, tf := range r.TestFiles {
		for _, t := range tf.Tests {
			names[t.ID] = file + "#" + t.Name
		}
	}
	// Every test is a row, so one reaching no mutant is still reported
	// redundant.
	rows := map[string]*Test{}
	for _, name := range names {
		rows[name] = &Test{Name: name}
	}
	row := func(id string) (*Test, error) {
		name, ok := names[id]
		if !ok {
			return nil, fmt.Errorf("stryker: test %q is in no testFiles entry", id)
		}
		return rows[name], nil
	}
	o := &Observations{Source: "stryker"}
	for _, file := range slices.Sorted(maps.Keys(r.Files)) {
		for _, m := range r.Files[file].Mutants {
			start, end := m.Location.Start, m.Location.End
			site := span(file, start.Line, start.Column, end.Line, end.Column)
			label := fmt.Sprintf("%s:%d:%d: %s %s", file, start.Line, start.Column, m.MutatorName, oneLine(m.Replacement))
			for _, id := range m.CoveredBy {
				t, err := row(id)
				if err != nil {
					return nil, err
				}
				t.Sites = append(t.Sites, site)
			}
			switch m.Status {
			case "Killed":
				for _, id := range m.KilledBy {
					t, err := row(id)
					if err != nil {
						return nil, err
					}
					t.Kills = append(t.Kills, label)
				}
			case "Survived", "NoCoverage":
				o.Survived = append(o.Survived, label)
			}
		}
	}
	o.Tests = sortedRows(rows)
	return o, nil
}

// oneLine collapses the whitespace of a replacement, which spans lines
// for a block.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// sortedRows lists the rows by name, each label once and sorted.
func sortedRows(rows map[string]*Test) []Test {
	out := make([]Test, 0, len(rows))
	for _, name := range slices.Sorted(maps.Keys(rows)) {
		t := rows[name]
		t.Sites = dedup(t.Sites)
		t.Blocks = dedup(t.Blocks)
		t.Kills = dedup(t.Kills)
		out = append(out, *t)
	}
	return out
}

func dedup(s []string) []string {
	slices.Sort(s)
	return slices.Compact(s)
}
