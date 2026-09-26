package ingest

import (
	"encoding/json/v2"
	"fmt"
)

// llvmExport is the part of an llvm-cov JSON export (`llvm-cov export
// -format=text`, `cargo llvm-cov --json`) the adapter reads.
type llvmExport struct {
	Type string `json:"type"`
	Data []struct {
		Functions []struct {
			Name      string   `json:"name"`
			Filenames []string `json:"filenames"`
			// Regions are [lineStart, colStart, lineEnd, colEnd,
			// executionCount, fileID, expandedFileID, kind].
			Regions [][]int64 `json:"regions"`
		} `json:"functions"`
	} `json:"data"`
}

// llvmCodeRegion is the kind of a region counting executed code, as
// opposed to an expansion, a skipped, a gap or a branch region.
const llvmCodeRegion = 0

// LLVMCov reads the llvm-cov JSON export of one test's run into the row
// of test: every code region executed is a block it reaches. Regions of
// files outside paths are left out, and those of a function exclude
// matches by its demangled Rust path (legacy or v0 mangling; a name that
// does not demangle is matched as it is): a test function's own body is
// reached by that test alone, and would make every test essential.
// Per-test coverage takes one coverage run per test. The same source
// region compiled into several binaries is one block.
//
// Unless regions is set, a block is a line ("<file>:<line>"), each region
// counted on its first line: llvm-cov splits one line into a region per
// operand of a short-circuit or a call, which would weigh a line by its
// operators and list a dozen regions for one line an essential test alone
// reaches. The kills already see what one operand of a line decides.
func LLVMCov(data []byte, test string, paths Paths, exclude func(fn string) bool, regions bool) (*Observations, error) {
	var e llvmExport
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("llvm-cov: %w", err)
	}
	if e.Type != "llvm.coverage.json.export" {
		return nil, fmt.Errorf("llvm-cov: type %q: not an llvm-cov JSON export", e.Type)
	}
	t := Test{Name: test}
	for _, d := range e.Data {
		for _, fn := range d.Functions {
			if exclude != nil && exclude(Demangle(fn.Name)) {
				continue
			}
			for _, r := range fn.Regions {
				if len(r) < 8 || r[4] == 0 || r[7] != llvmCodeRegion || r[5] < 0 || int(r[5]) >= len(fn.Filenames) {
					continue
				}
				rel, ok := paths.rel(fn.Filenames[r[5]])
				if !ok {
					continue
				}
				if regions {
					t.Blocks = append(t.Blocks, span(rel, int(r[0]), int(r[1]), int(r[2]), int(r[3])))
				} else {
					t.Blocks = append(t.Blocks, fmt.Sprintf("%s:%d", rel, r[0]))
				}
			}
		}
	}
	t.Blocks = dedup(t.Blocks)
	return &Observations{Source: "llvm-cov", Tests: []Test{t}}, nil
}
