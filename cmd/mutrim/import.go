package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/illumination-k/mutrim/ingest"
)

const importUsage = `usage: mutrim import <tool> [flags] <input>

tools:
  stryker        a mutation-testing-report-schema JSON report (StrykerJS
                 with coverageAnalysis "perTest" and disableBail): the
                 sites each test reaches and the mutants it kills
  cargo-mutants  a mutants.out directory (run with
                 --cargo-test-arg=--no-fail-fast): the mutants each test
                 kills, read from the logs, and with nextest the durations
  istanbul       one test's coverage-final.json (vitest, jest; -test):
                 the statements it executes
  llvm-cov       one test's llvm-cov JSON export (cargo llvm-cov --json;
                 -test): the lines (or -regions) it executes
  junit          a JUnit XML report (-runner vitest|nextest): the test
                 durations`

// importFlags are the flags of `mutrim import`; each tool registers
// those it reads.
type importFlags struct {
	test, root, files, excludeFiles, excludeFn, runner string
	regions                                            bool
}

// runImport is `mutrim import`: it reads another language's tool output
// into the observations `mutrim minimize` takes next to report.json.
func runImport(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("import: missing tool\n" + importUsage)
	}
	tool := args[0]
	fs := flag.NewFlagSet("import "+tool, flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", "write the observations here instead of stdout")
	var f importFlags
	switch tool {
	case "istanbul", "llvm-cov":
		fs.StringVar(&f.test, "test", "", "the test the coverage is of, named as the kill importer names it (required)")
		fs.StringVar(&f.root, "root", ".", "the project root: files are reported relative to it, and those outside it left out")
		excludeFiles := ""
		if tool == "llvm-cov" {
			excludeFiles = `(^|/)(tests|benches|examples)/`
			fs.StringVar(&f.excludeFn, "exclude-fn", `(^|::)(tests|test_support|test_utils)(::|$)`, "regexp over the demangled path of the functions to leave out (the test code)")
			fs.BoolVar(&f.regions, "regions", false, "a block per llvm-cov region instead of per line")
		}
		fs.StringVar(&f.files, "files", "", "regexp over the relative path of the files to keep (e.g. the package under test of a workspace); empty keeps every file")
		fs.StringVar(&f.excludeFiles, "exclude-files", excludeFiles, "regexp over the relative path of the files to leave out")
	case "junit":
		fs.StringVar(&f.runner, "runner", "", `the runner that wrote the report, which the row names follow: "vitest" or "nextest" (required)`)
	case "stryker", "cargo-mutants":
	default:
		return fmt.Errorf("import: unknown tool %q\n%s", tool, importUsage)
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("import %s: exactly one input is required", tool)
	}

	var obs *ingest.Observations
	var err error
	if tool == "cargo-mutants" {
		obs, err = ingest.CargoMutants(fs.Arg(0), stderr)
	} else {
		var data []byte
		if data, err = os.ReadFile(filepath.Clean(fs.Arg(0))); err != nil {
			return err
		}
		obs, err = importFile(tool, data, f)
	}
	if err != nil {
		return err
	}
	return writeJSON(*out, stdout, obs)
}

// importFile runs the adapter of a single-file tool.
func importFile(tool string, data []byte, f importFlags) (*ingest.Observations, error) {
	switch tool {
	case "stryker":
		return ingest.Stryker(data)
	case "junit":
		if f.runner == "" {
			return nil, errors.New("import junit: -runner is required")
		}
		return ingest.JUnit(data, f.runner)
	}
	if f.test == "" {
		return nil, fmt.Errorf("import %s: -test is required", tool)
	}
	root, err := filepath.Abs(f.root)
	if err != nil {
		return nil, err
	}
	includeFile, err := optionalRegexp(f.files)
	if err != nil {
		return nil, fmt.Errorf("import %s: -files: %w", tool, err)
	}
	excludeFile, err := optionalRegexp(f.excludeFiles)
	if err != nil {
		return nil, fmt.Errorf("import %s: -exclude-files: %w", tool, err)
	}
	paths := ingest.Paths{Root: root, Include: includeFile, Exclude: excludeFile}
	if tool == "istanbul" {
		return ingest.Istanbul(data, f.test, paths)
	}
	excludeFn, err := optionalRegexp(f.excludeFn)
	if err != nil {
		return nil, fmt.Errorf("import %s: -exclude-fn: %w", tool, err)
	}
	return ingest.LLVMCov(data, f.test, paths, excludeFn, f.regions)
}

// optionalRegexp compiles re into a matcher; empty matches nothing.
func optionalRegexp(re string) (func(string) bool, error) {
	if re == "" {
		return nil, nil
	}
	r, err := regexp.Compile(re)
	if err != nil {
		return nil, err
	}
	return r.MatchString, nil
}
