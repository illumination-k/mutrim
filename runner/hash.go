package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
)

// HashTests returns the SHA-256 of the source text of every top-level
// function in the given _test.go files (other files are skipped), by name.
// The text runs from `func` to the closing brace, so an edit to the doc
// comment or elsewhere in the file leaves the hash alone; an edit to a
// helper the test calls does too, which a full run corrects.
func HashTests(files []string) (map[string]string, error) {
	hashes := map[string]string{}
	fset := token.NewFileSet()
	for _, f := range files {
		if !strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f) //nolint:gosec // the test sources the caller names
		if err != nil {
			return nil, err
		}
		file, err := parser.ParseFile(fset, f, src, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("runner: %w", err)
		}
		tf := fset.File(file.Pos())
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
				sum := sha256.Sum256(src[tf.Offset(fn.Pos()):tf.Offset(fn.End())])
				hashes[fn.Name.Name] = hex.EncodeToString(sum[:])
			}
		}
	}
	return hashes, nil
}

// reusable reports whether prev, the result of mutant id in the previous
// report, still holds for the tests now reaching it. A kill holds while
// every killer is still a trustworthy row reaching the mutant, with the
// source it had then (PIT's incremental analysis); any other result, or a
// kill naming no killer, holds while the rows reaching the mutant and
// their sources are the ones it was observed with, so a new or rewritten
// test gets its chance at a survivor. Without test sources every hash is
// empty and only the rows themselves are compared.
func reusable(id string, prev Result, reaching []string, now, before map[string]Test) bool {
	if !prev.Status.Executed() {
		return false
	}
	if (prev.Status == Killed || prev.Status == Timeout) && len(prev.KilledBy) > 0 {
		for _, k := range prev.KilledBy {
			t, ok := now[k]
			b, was := before[k]
			if !ok || !was || t.Flaky || t.Hash != b.Hash || !slices.Contains(reaching, k) {
				return false
			}
		}
		return true
	}
	then := map[string]string{}
	for name, t := range before {
		if slices.Contains(t.Sites, id) {
			then[name] = t.Hash
		}
	}
	if len(then) != len(reaching) {
		return false
	}
	for _, name := range reaching {
		if h, ok := then[name]; !ok || h != now[name].Hash {
			return false
		}
	}
	return true
}
