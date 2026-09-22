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
	for _, m := range Generate(pkg, Options{Operators: ops}) {
		if m.ID != id {
			continue
		}
		m.site.Apply()
		defer m.site.Undo()

		var buf bytes.Buffer
		if err := format.Node(&buf, pkg.Fset, m.site.file); err != nil {
			return "", nil, fmt.Errorf("mutator: format %s: %w", m.File, err)
		}
		return m.File, buf.Bytes(), nil
	}
	return "", nil, fmt.Errorf("mutator: mutant %s not found in %s", id, pkg.PkgPath)
}
