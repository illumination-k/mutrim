package ingest

import (
	"encoding/json/v2"
	"fmt"
	"maps"
	"slices"
)

// istanbulFile is one file of an Istanbul coverage-final.json.
type istanbulFile struct {
	Path         string `json:"path"`
	StatementMap map[string]struct {
		Start istanbulPos `json:"start"`
		End   istanbulPos `json:"end"`
	} `json:"statementMap"`
	S map[string]int64 `json:"s"`
}

// istanbulPos is 1-based in lines and 0-based in columns; the v8
// provider writes a null column for a statement ending its line, read
// as 0.
type istanbulPos struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

// Istanbul reads the coverage-final.json of one test's run (vitest's json
// coverage reporter, with either provider, or jest's) into the row of
// test: every statement executed is a block it reaches. Per-test coverage
// takes one coverage run per test; files outside paths are left out.
func Istanbul(data []byte, test string, paths Paths) (*Observations, error) {
	var files map[string]istanbulFile
	if err := json.Unmarshal(data, &files); err != nil {
		return nil, fmt.Errorf("istanbul: %w", err)
	}
	t := Test{Name: test}
	for _, key := range slices.Sorted(maps.Keys(files)) {
		f := files[key]
		path := f.Path
		if path == "" {
			path = key
		}
		rel, ok := paths.rel(path)
		if !ok {
			continue
		}
		for id, count := range f.S {
			s, ok := f.StatementMap[id]
			if count > 0 && ok {
				t.Blocks = append(t.Blocks, span(rel, s.Start.Line, s.Start.Column, s.End.Line, s.End.Column))
			}
		}
	}
	t.Blocks = dedup(t.Blocks)
	return &Observations{Source: "istanbul", Tests: []Test{t}}, nil
}
