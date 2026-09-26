package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/tools/go/packages"

	"github.com/illumination-k/mutrim/mut"
	"github.com/illumination-k/mutrim/mutator"
	"github.com/illumination-k/mutrim/runner"
)

// injectedRuntime is where `mutrim test` puts its copy of the mut runtime,
// relative to the target module: internal, so nothing outside the module
// can import it, and only ever present in the build overlay.
const injectedRuntime = "internal/mutrimrt"

// reportFiles names the file `mutrim test -report` writes per format.
var reportFiles = map[string]string{"stryker": "mutation-report.json", "html": "mutation-report.html", "github": "annotations.txt"}

// testOutput is the JSON of `mutrim test`.
type testOutput struct {
	Packages []testPackage `json:"packages"`
	// Totals scores the mutants of every package together.
	Totals runner.Totals `json:"totals"`
}

// testPackage is one package of a `mutrim test` run.
type testPackage struct {
	Pkg    string        `json:"pkg"`
	Report string        `json:"report"`
	Totals runner.Totals `json:"totals"`
}

// testUnit is a package with tests, generated and ready to build and run.
type testUnit struct {
	pkg      *packages.Package
	dir      string // its output directory
	mutants  []mutator.Mutant
	testSrcs []string
}

// runTest is `mutrim test`, the one-shot driver for go test users: per
// package it runs gen -schemata with the mut runtime injected through the
// overlay (so the target module never depends on mutrim), builds the test
// binary and runs it, writing <out>/<pkg>/report.json, which the next run
// copies results forward from. It prints the combined totals; -minimize and
// -report chain the minimize and report commands over every package.
func runTest(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", ".mutrim", "directory of the outputs, <out>/<pkg>/report.json per package")
	previous := fs.String("previous", ".mutrim", "directory of an earlier run's outputs whose results are copied forward while their tests are unchanged; empty runs every mutant")
	operators := fs.String("operators", "", operatorsUsage)
	inDiff := fs.String("in-diff", "", "git ref; only the mutants `git diff --merge-base <ref>` adds lines to, and those their tests reach, run")
	threshold := fs.Float64("threshold", 0, "fail after writing the outputs when the combined score is below this (0..1); 0 is off")
	thresholdCovered := fs.Float64("threshold-covered", 0, "fail after writing the outputs when the combined covered_score is below this (0..1); 0 is off")
	parallel := fs.Int("p", runtime.GOMAXPROCS(0), "packages built and run at once")
	jobs := fs.Int("jobs", 0, "test processes per package run at once (default: GOMAXPROCS / -p)")
	skipFailing := fs.Bool("skip-failing", false, "run the mutants without the tests that fail on their own instead of stopping (see run -skip-failing)")
	doMinimize := fs.Bool("minimize", false, "also write <out>/minimize.json over every package")
	extraPath := fs.String("extra", "", extraUsage)
	formats := fs.String("report", "", `comma-separated report formats to write to <out>: "stryker" (mutation-report.json), "html" (mutation-report.html), "github" (annotations.txt)`)
	if err := fs.Parse(args); err != nil {
		return err
	}
	th := runner.Threshold{Score: *threshold, Covered: *thresholdCovered}
	if th.Score < 0 || th.Score > 1 || th.Covered < 0 || th.Covered > 1 {
		return errors.New("test: -threshold and -threshold-covered must be between 0 and 1")
	}
	for format := range strings.SplitSeq(*formats, ",") {
		if _, ok := reportFiles[format]; !ok && format != "" {
			return fmt.Errorf("test: unknown -report format %q", format)
		}
	}
	ops, err := mutator.Operators(*operators)
	if err != nil {
		return err
	}
	// The runner starts the test binaries from their package directories.
	if *out, err = filepath.Abs(*out); err != nil {
		return err
	}
	var diff *runner.Diff
	if *inDiff != "" {
		if diff, err = gitDiff(ctx, *inDiff); err != nil {
			return err
		}
	}

	extras, err := readExtras(*extraPath)
	if err != nil {
		return err
	}
	pkgs, err := mutator.Load(".", patterns(fs)...)
	if err != nil {
		return err
	}
	var units []*testUnit
	for _, pkg := range pkgs {
		var mine []mutator.Extra
		mine, extras = mutator.PackageExtras(pkg, extras)
		u, err := genUnit(pkg, *out, ops, mine, stderr)
		if err != nil {
			return err
		}
		if u == nil {
			_, _ = fmt.Fprintf(stderr, "mutrim: %s: no test files, skipped\n", pkg.PkgPath)
			continue
		}
		units = append(units, u)
	}
	for _, e := range extras {
		_, _ = fmt.Fprintf(stderr, "mutrim: test -extra: %s: no loaded package has this file\n", e.File)
	}
	if len(units) == 0 {
		return errors.New("test: no package with tests")
	}
	p := max(1, min(*parallel, len(units)))
	if *jobs == 0 {
		*jobs = max(1, runtime.GOMAXPROCS(0)/p)
	}

	log := &syncWriter{w: stderr}
	reports := make([]*runner.Report, len(units))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(p)
	for i, u := range units {
		g.Go(func() error {
			opts := runner.Options{
				Mutants: u.mutants, Dir: u.pkg.Dir, TestSrcs: u.testSrcs, InDiff: diff, DiffExpand: true,
				TimeoutConst: 2 * time.Second, SkipFailing: *skipFailing, Jobs: *jobs, Log: log,
			}
			var err error
			if opts.TestBin, err = buildUnit(gctx, u); err != nil {
				return err
			}
			if opts.Previous, err = previousReport(*previous, u.pkg.PkgPath); err != nil {
				return err
			}
			if reports[i], err = runner.Run(gctx, opts); err != nil {
				return fmt.Errorf("%s: %w", u.pkg.PkgPath, err)
			}
			return writeJSON(filepath.Join(u.dir, "report.json"), nil, reports[i])
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	if err := chainOutputs(units, *out, *doMinimize, *formats, stdout, stderr); err != nil {
		return err
	}
	merged := runner.Merge(reports...)
	result := testOutput{Packages: make([]testPackage, len(units)), Totals: merged.Totals}
	for i, u := range units {
		result.Packages[i] = testPackage{Pkg: u.pkg.PkgPath, Report: filepath.Join(u.dir, "report.json"), Totals: reports[i].Totals}
	}
	if err := writeJSON("", stdout, result); err != nil {
		return err
	}
	if err := th.Check(merged); err != nil {
		return thresholdError{err}
	}
	return nil
}

// genUnit generates the mutants of pkg and writes its schemata sources, the
// injected runtime, overlay.json, mutants.json and blocks.json under
// <out>/<pkg>, extras (gen -extra) included. A package without _test.go
// files returns nil: it has no test binary to run.
func genUnit(pkg *packages.Package, out string, ops []mutator.Operator, extras []mutator.Extra, stderr io.Writer) (*testUnit, error) {
	testSrcs, err := filepath.Glob(filepath.Join(pkg.Dir, "*_test.go"))
	if err != nil || len(testSrcs) == 0 {
		return nil, err
	}
	if pkg.Module == nil {
		return nil, fmt.Errorf("test: %s is not in a module", pkg.PkgPath)
	}
	u := &testUnit{pkg: pkg, dir: filepath.Join(out, filepath.FromSlash(pkg.PkgPath)), testSrcs: testSrcs}
	if err := os.RemoveAll(filepath.Join(u.dir, "schemata")); err != nil {
		return nil, err
	}
	opts := mutator.Options{Operators: ops, TypeCheck: true, Diff: mutator.DiffStmt}
	u.mutants = mutator.Generate(pkg, opts)
	u.mutants = append(u.mutants, generateExtra(pkg, u.mutants, extras, opts, stderr)...)
	blocks := mutator.Blocks(pkg)
	schemata := filepath.Join(u.dir, "schemata")
	overlay := mutator.Overlay{Replace: map[string]string{}}
	runtimePath := pkg.Module.Path + "/" + injectedRuntime
	if err := writeSchemata(schemata, pkg, u.mutants, blocks, runtimePath, &overlay); err != nil {
		return nil, err
	}
	rt := filepath.Join(schemata, "mutrimrt.go")
	if err := os.WriteFile(rt, mut.Source, 0o600); err != nil {
		return nil, err
	}
	overlay.Replace[filepath.Join(pkg.Module.Dir, filepath.FromSlash(injectedRuntime), "mut.go")] = rt
	for path, v := range map[string]any{"overlay.json": overlay, "mutants.json": u.mutants, "blocks.json": blocks} {
		if err := writeJSON(filepath.Join(u.dir, path), nil, v); err != nil {
			return nil, err
		}
	}
	return u, nil
}

// previousReport reads <dir>/<pkg>/report.json, or returns nil when dir is
// empty or holds no report of pkg.
func previousReport(dir, pkg string) (*runner.Report, error) {
	if dir == "" {
		return nil, nil
	}
	path := filepath.Join(dir, filepath.FromSlash(pkg), "report.json")
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return runner.ReadReport(path)
}

// buildUnit builds the schemata test binary of u and returns its path.
// Vet is off: it cannot run on the injected runtime, whose directory exists
// only in the overlay.
func buildUnit(ctx context.Context, u *testUnit) (string, error) {
	bin := filepath.Join(u.dir, "pkg.test")
	cmd := exec.CommandContext(ctx, "go", "test", "-c", "-vet=off", //nolint:gosec // the package path comes from go list
		"-overlay", filepath.Join(u.dir, "overlay.json"), "-o", bin, u.pkg.PkgPath)
	cmd.Dir = u.pkg.Module.Dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%s: build the schemata test binary: %w\n%s", u.pkg.PkgPath, err, out)
	}
	return bin, nil
}

// chainOutputs writes the combined mutants.json and blocks.json of the
// run to out, then minimize.json and the requested reports from them.
func chainOutputs(units []*testUnit, out string, doMinimize bool, formats string, stdout, stderr io.Writer) error {
	if !doMinimize && formats == "" {
		return nil
	}
	var mutants []mutator.Mutant
	var blocks []mutator.Block
	var reports, testSrcs, srcs []string
	for _, u := range units {
		var bs []mutator.Block
		if err := readJSON(filepath.Join(u.dir, "blocks.json"), &bs); err != nil {
			return err
		}
		mutants = append(mutants, u.mutants...)
		blocks = append(blocks, bs...)
		reports = append(reports, filepath.Join(u.dir, "report.json"))
		testSrcs = append(testSrcs, u.testSrcs...)
		srcs = append(srcs, u.pkg.Dir)
	}
	mutantsPath, blocksPath := filepath.Join(out, "mutants.json"), filepath.Join(out, "blocks.json")
	if err := writeJSON(mutantsPath, nil, mutants); err != nil {
		return err
	}
	if err := writeJSON(blocksPath, nil, blocks); err != nil {
		return err
	}
	var errs []error
	if doMinimize {
		args := []string{"-mutants", mutantsPath, "-blocks", blocksPath, "-srcs", strings.Join(testSrcs, ","), "-o", filepath.Join(out, "minimize.json")}
		errs = append(errs, runMinimize(slices.Concat(args, reports), stdout, stderr))
	}
	for format := range strings.SplitSeq(formats, ",") {
		if format == "" {
			continue
		}
		args := []string{"-format", format, "-mutants", mutantsPath, "-srcs", strings.Join(srcs, ","), "-o", filepath.Join(out, reportFiles[format])}
		errs = append(errs, runReport(slices.Concat(args, reports), stdout, stderr))
	}
	return errors.Join(errs...)
}

// gitDiff is `git diff --merge-base ref`: the lines the working tree adds
// since it left ref.
func gitDiff(ctx context.Context, ref string) (*runner.Diff, error) {
	out, err := exec.CommandContext(ctx, "git", "diff", "--merge-base", ref).Output() //nolint:gosec // the ref is the user's
	if err != nil {
		return nil, fmt.Errorf("test: git diff --merge-base %s: %w", ref, err)
	}
	return runner.ParseDiff(bytes.NewReader(out))
}

// syncWriter serializes the logs of the packages run at once.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
