package minimize

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// Tagged returns the top-level tests whose doc comment contains tag, in
// the given _test.go files and directories (other files are skipped).
// It is the comment-tag protection rule: a test the tool must never call
// redundant, whatever the matrix says.
func Tagged(paths []string, tag string) ([]string, error) {
	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			files = append(files, p)
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			files = append(files, filepath.Join(p, e.Name()))
		}
	}

	fset := token.NewFileSet()
	var names []string
	for _, f := range files {
		if !strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("minimize: %w", err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") && hasTag(fn.Doc, tag) {
				names = append(names, fn.Name.Name)
			}
		}
	}
	return names, nil
}

// hasTag looks at the raw comment lines: CommentGroup.Text drops
// directive-shaped lines such as //mutrim:keep.
func hasTag(doc *ast.CommentGroup, tag string) bool {
	if doc == nil {
		return false
	}
	for _, c := range doc.List {
		if strings.Contains(c.Text, tag) {
			return true
		}
	}
	return false
}
