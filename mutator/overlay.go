package mutator

import (
	"bytes"
	"fmt"
	"go/format"

	"golang.org/x/tools/go/packages"
)

// Overlay is the JSON shape accepted by `go build -overlay`.
type Overlay struct {
	Replace map[string]string `json:"Replace"`
}

// Source applies mutant id to pkg and returns the rewritten file's path and
// formatted contents. The AST is restored before returning. ops must be the
// operators the mutant was generated with; nil means DefaultOperators.
func Source(pkg *packages.Package, id string, ops []Operator) (file string, src []byte, err error) {
	srcs, err := Sources(pkg, []string{id}, ops)
	if err != nil {
		return "", nil, err
	}
	return srcs[id].File, srcs[id].Src, nil
}

// Mutated is a file with one mutant applied.
type Mutated struct {
	File string
	Src  []byte
}

// Sources is Source for several mutants, generated once: it maps each of
// ids to its rewritten file.
func Sources(pkg *packages.Package, ids []string, ops []Operator) (map[string]Mutated, error) {
	byID := map[string]Mutant{}
	for _, m := range Generate(pkg, Options{Operators: ops}) {
		byID[m.ID] = m
	}
	out := make(map[string]Mutated, len(ids))
	for _, id := range ids {
		m, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("mutator: mutant %s not found in %s", id, pkg.PkgPath)
		}
		src, err := applied(pkg, m)
		if err != nil {
			return nil, err
		}
		out[id] = Mutated{File: m.File, Src: src}
	}
	return out, nil
}

// applied formats m's file with m applied, and restores the AST.
func applied(pkg *packages.Package, m Mutant) ([]byte, error) {
	m.site.Apply()
	defer m.site.Undo()
	var buf bytes.Buffer
	if err := format.Node(&buf, pkg.Fset, m.site.file); err != nil {
		return nil, fmt.Errorf("mutator: format %s: %w", m.File, err)
	}
	return buf.Bytes(), nil
}
