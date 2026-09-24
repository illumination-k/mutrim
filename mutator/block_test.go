package mutator_test

import (
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/illumination-k/mutrim/mutator"
)

// Blocks lists the function bodies, the bodies of if, else, for and range,
// the case and select clauses, and a function literal's body, but neither
// a switch or select body (it holds clauses) nor a bare block. The IDs are
// distinct, stable and never a mutant's.
func TestBlocks(t *testing.T) {
	pkg := load(t, "blocks")
	blocks := mutator.Blocks(pkg)
	got := make([]string, 0, len(blocks))
	ids := map[string]bool{}
	for _, b := range blocks {
		got = append(got, fmt.Sprintf("%s %d:%d-%d:%d", b.Func, b.Line, b.Col, b.EndLine, b.EndCol))
		ids[b.ID] = true
	}
	want := []string{
		"Straight 4:31-7:2",
		"Every 9:36-32:2",
		"Every 10:11-12:3",  // if
		"Every 12:9-14:3",   // else
		"Every 15:25-16:3",  // for
		"Every 17:14-18:3",  // range
		"Every 20:2-20:9",   // case 1
		"Every 21:2-21:10",  // default
		"Every 24:2-24:12",  // case <-ch
		"Every 25:2-25:10",  // default
		"Every 27:18-27:30", // func literal
	}
	if !slices.Equal(got, want) {
		t.Errorf("blocks =\n%v\nwant\n%v", got, want)
	}
	if len(ids) != len(blocks) {
		t.Errorf("block IDs are not distinct: %v", blocks)
	}
	for _, m := range mutator.Generate(pkg, mutator.Options{}) {
		if ids[m.ID] {
			t.Errorf("block ID %s is also mutant %s's", m.ID, m.Description)
		}
	}
	for i, b := range mutator.Blocks(load(t, "blocks")) {
		if b.ID != blocks[i].ID {
			t.Errorf("block %d: ID %s, then %s", i, blocks[i].ID, b.ID)
		}
	}
	if excluded := mutator.Blocks(load(t, "excluded")); len(excluded) == 0 || slices.ContainsFunc(excluded, func(b mutator.Block) bool {
		return filepath.Base(b.File) != "normal.go"
	}) {
		t.Errorf("blocks must come from the mutated files only: %v", excluded)
	}
}
