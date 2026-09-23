// Command mutrim generates and runs mutants for Go packages.
//
// Output on stdout is JSON only; diagnostics go to stderr.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"golang.org/x/tools/go/packages"

	"github.com/illumination-k/mutrim/criteria"
	"github.com/illumination-k/mutrim/minimize"
	"github.com/illumination-k/mutrim/mutator"
	"github.com/illumination-k/mutrim/report"
	"github.com/illumination-k/mutrim/runner"
)

// operatorsUsage documents the -operators flag of gen and overlay.
const operatorsUsage = `comma-separated operators to apply: names, "default", ` +
	`and "-name" to remove one (default: the default set, which leaves out the opt-in ones)`

// aridUsage documents the -arid flag of gen.
const aridUsage = `comma-separated globs over the callee ("(*Metrics).Observe", ` +
	`"(*slog.Logger).Info") extending the built-in arid rules: a matching call ` +
	`and its arguments are arid, and their mutants ignored`

const usage = `usage: mutrim <command> [flags] [packages]

commands:
  gen       list mutants of the packages as JSON; -operators selects the
            operators and -match / -files / -exclude-files / -exclude-re /
            -arid narrow the sites (a filtered mutant is reported, and
            ignored); mutants inside arid nodes (logging, sleeps, ...) are
            ignored unless -no-arid;
            -schemata also writes sources with every mutant embedded;
            -importpath type-checks the given files from export data
            (Bazel mode)
  overlay   write one mutant and print a go build -overlay file for it;
            -operators must match the gen run the mutant comes from
  run       execute a schemata test binary once per mutant, against the
            tests that reach it, and report the per-test kill matrix;
            -subtests makes each subtest a row of it; -in-diff scopes the
            run to the mutants a unified diff's added lines and their
            tests reach (-diff-expand=false: the lines alone); -confirm-kills and
            -confirm-baseline rerun to keep flaky tests out of the matrix;
            -extra-test adds the tests of a package importing it; a
            survivor that left every test's trace unchanged is reported
            SUSPECT_EQUIVALENT and scored only with -count-suspect;
            -threshold / -threshold-covered fail the run, after writing
            the report, when its score is below them
  minimize  from report.json files (the shards of a package, or several
            packages), list the tests a greedy set cover finds redundant,
            the functions whose mutants survive, and the tests found flaky
  report    render report.json in an interchange format: the Stryker
            mutation-testing-elements JSON, its single-file HTML viewer,
            or GitHub Actions annotations
  bazel-test
            the test executable of the mutation_test macro: run, then
            minimize and report into TEST_UNDECLARED_OUTPUTS_DIR, with
            every path a runfiles path`

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "mutrim:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("missing command\n" + usage)
	}
	switch args[0] {
	case "gen":
		return runGen(args[1:], stdout, stderr)
	case "overlay":
		return runOverlay(args[1:], stdout, stderr)
	case "run":
		return runRun(ctx, args[1:], stdout, stderr)
	case "minimize":
		return runMinimize(args[1:], stdout, stderr)
	case "report":
		return runReport(args[1:], stdout, stderr)
	case "bazel-test":
		return runBazelTest(ctx, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}

func runGen(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", "write mutants.json here instead of stdout")
	noCheck := fs.Bool("no-check", false, "skip the go/types pre-filter")
	operators := fs.String("operators", "", operatorsUsage)
	match := fs.String("match", "", "keep only the mutants of functions whose name matches this regexp, in the \"(*T).Name\" form")
	files := fs.String("files", "", "keep only the mutants in files matching one of these comma-separated globs")
	excludeFiles := fs.String("exclude-files", "", "drop the mutants in files matching one of these comma-separated regexps")
	excludeRE := fs.String("exclude-re", "", "drop the mutants whose \"func operator: description\" matches this regexp")
	arid := fs.String("arid", "", aridUsage)
	noArid := fs.Bool("no-arid", false, "turn the built-in arid rules off (-arid still applies)")
	schemata := fs.String("schemata", "", "write schemata sources under this directory, plus overlay.json for go build")
	importPath := fs.String("importpath", "", "type-check the argument files as this package from export data instead of running go list (Bazel mode)")
	importcfg := fs.String("importcfg", "", "dependencies' export data in go build -importcfg format (with -importpath)")
	stdlib := fs.String("stdlib", "", "directory of compiled standard-library packages, <dir>/<goos_goarch>/<path>.a (with -importpath)")
	tags := fs.String("tags", "", "comma-separated build tags (with -importpath)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ops, err := mutator.Operators(*operators)
	if err != nil {
		return err
	}
	filter, err := mutator.FilterSpec{
		Match:        *match,
		Files:        *files,
		ExcludeFiles: *excludeFiles,
		ExcludeRE:    *excludeRE,
		Arid:         *arid,
		NoArid:       *noArid,
	}.Compile()
	if err != nil {
		return err
	}

	var pkgs []*packages.Package
	if *importPath != "" {
		cfg := mutator.FilesConfig{ImportPath: *importPath, Files: fs.Args(), Importcfg: *importcfg, Stdlib: *stdlib}
		if *tags != "" {
			cfg.Tags = strings.Split(*tags, ",")
		}
		pkg, err := mutator.LoadFiles(cfg)
		if err != nil {
			return err
		}
		pkgs = []*packages.Package{pkg}
	} else {
		var err error
		if pkgs, err = mutator.Load(".", patterns(fs)...); err != nil {
			return err
		}
	}
	mutants := []mutator.Mutant{}
	overlay := mutator.Overlay{Replace: map[string]string{}}
	for _, pkg := range pkgs {
		ms := mutator.Generate(pkg, mutator.Options{Operators: ops, TypeCheck: !*noCheck, Filter: filter})
		if *schemata != "" {
			if err := writeSchemata(*schemata, pkg, ms, &overlay); err != nil {
				return err
			}
		}
		mutants = append(mutants, ms...)
	}
	if *schemata != "" {
		if err := writeJSON(filepath.Join(*schemata, "overlay.json"), nil, overlay); err != nil {
			return err
		}
	}
	return writeJSON(*out, stdout, mutants)
}

// writeSchemata writes the complete package under dir/<import path>/:
// files with an embedded mutant are lowered, the rest (including files
// excluded by build constraints) are copied as they are, so the directory
// can replace the package. Mutants the lowering declined are marked not
// viable, so the runner never selects them; ignored and equivalent
// mutants are not embedded either, but keep their status.
func writeSchemata(dir string, pkg *packages.Package, ms []mutator.Mutant, overlay *mutator.Overlay) error {
	sch, err := mutator.Lower(pkg, ms)
	if err != nil {
		return err
	}
	for i := range ms {
		if !ms[i].Excluded() {
			ms[i].Viable = ms[i].Viable && sch.Embedded[ms[i].ID]
		}
	}
	pkgDir := filepath.Join(dir, filepath.FromSlash(pkg.PkgPath))
	if err := os.MkdirAll(pkgDir, 0o750); err != nil {
		return err
	}
	for _, orig := range slices.Concat(pkg.GoFiles, pkg.IgnoredFiles) {
		src, err := schemataSource(sch, orig)
		if err != nil {
			return err
		}
		mutated := filepath.Clean(filepath.Join(pkgDir, filepath.Base(orig)))
		if err := os.WriteFile(mutated, src, 0o600); err != nil {
			return err
		}
		overlay.Replace[orig] = mutated
	}
	return nil
}

// schemataSource is the lowered source of orig, or the original when no
// mutant is embedded in it.
func schemataSource(sch *mutator.Schemata, orig string) ([]byte, error) {
	if src, ok := sch.Files[orig]; ok {
		return src, nil
	}
	return os.ReadFile(filepath.Clean(orig))
}

func runOverlay(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("overlay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "mutant ID to apply (required)")
	operators := fs.String("operators", "", operatorsUsage)
	out := fs.String("o", "", "write the overlay JSON here instead of stdout")
	dir := fs.String("dir", "", "directory for the mutated source (default: a temp dir)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("overlay: -id is required")
	}
	ops, err := mutator.Operators(*operators)
	if err != nil {
		return err
	}

	pkgs, err := mutator.Load(".", patterns(fs)...)
	if err != nil {
		return err
	}
	if *dir == "" {
		if *dir, err = os.MkdirTemp("", "mutrim-"+*id+"-"); err != nil {
			return err
		}
	}

	var errs []error
	for _, pkg := range pkgs {
		file, src, err := mutator.Source(pkg, *id, ops)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		mutated := filepath.Join(*dir, filepath.Base(file))
		if err := os.WriteFile(mutated, src, 0o600); err != nil {
			return err
		}
		return writeJSON(*out, stdout, mutator.Overlay{Replace: map[string]string{file: mutated}})
	}
	return errors.Join(errs...)
}

// runRun is `mutrim run`. Under Bazel it reads TEST_SHARD_INDEX,
// TEST_TOTAL_SHARDS and TEST_UNDECLARED_OUTPUTS_DIR; arguments after "--"
// go to the test binary.
func runRun(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	testBin := fs.String("test-bin", "", "test binary built from schemata sources (required)")
	mutantsPath := fs.String("mutants", "", "mutants.json from gen -schemata (required)")
	out := fs.String("out", "", "write report.json here (default: $TEST_UNDECLARED_OUTPUTS_DIR/report.json, else stdout)")
	previous := fs.String("previous", "", "report.json of an earlier run; a result is copied forward while the tests it was observed with are unchanged (see -test-srcs)")
	testSrcs := fs.String("test-srcs", "", "comma-separated _test.go files of the package; each test's hash in report.json is read from them, so -previous re-executes the mutants whose killing or reaching tests changed")
	inDiff := fs.String("in-diff", "", "unified diff (`git diff --merge-base main > pr.diff`); only the mutants on its added lines (see -diff-expand) run, the rest are SKIPPED")
	diffExpand := fs.Bool("diff-expand", true, "with -in-diff, also run the commit-relevant mutants: every mutant a test reaching the diff's added lines reaches, wherever it lies")
	timeout := fs.Duration("timeout", 0, "per-mutant timeout, overriding the derived one (default: -timeout-factor × the durations of the tests reaching the mutant + -timeout-const, between -min-timeout and -timeout-factor × the baseline run)")
	minTimeout := fs.Duration("min-timeout", runner.DefaultMinTimeout, "floor of the derived per-mutant timeout, which every looping mutant waits out; lower it for fast, self-contained tests")
	timeoutFactor := fs.Float64("timeout-factor", runner.DefaultTimeoutFactor, "multiplier of the derived per-mutant timeout")
	timeoutConst := fs.Duration("timeout-const", 2*time.Second, "added to the derived per-mutant timeout for process startup")
	tests := fs.String("tests", "", "comma-separated top-level tests to run (default: all)")
	subtests := fs.Bool("subtests", false, "make each subtest (TestX/case) a row of the kill matrix: traced on its own and named in killed_by; subtest names must be stable across runs")
	confirmKills := fs.Int("confirm-kills", 1, "rerun a mutant's killing tests until each has failed this many runs; a kill that does not reproduce is recorded in suspicious_by instead of killed_by")
	confirmBaseline := fs.Int("confirm-baseline", 1, "run each test this many times while tracing; one that fails in some runs and passes in others is marked flaky, and its failures are never kills")
	countSuspect := fs.Bool("count-suspect", false, "count SUSPECT_EQUIVALENT mutants (survivors whose tests reached the same sites as without them) as survivors in the score and the reports")
	threshold := fs.Float64("threshold", 0, "fail after writing the report when the score is below this (0..1); 0 is off")
	thresholdCovered := fs.Float64("threshold-covered", 0, "fail after writing the report when the score over the covered mutants (covered_score) is below this (0..1); 0 is off")
	dir := fs.String("dir", "", "working directory for the test binary")
	jobs := fs.Int("jobs", 0, "test processes run at once, while tracing and while running the mutants (default: GOMAXPROCS)")
	var extra []runner.Binary
	fs.Func("extra-test", "`pkg=bin[,dir]`: the test binary of package pkg, which imports the mutated one, built against the same schemata sources; its tests run against the mutants too, named pkg.TestX (repeatable)", func(v string) error {
		pkg, rest, ok := strings.Cut(v, "=")
		if !ok || pkg == "" || rest == "" {
			return fmt.Errorf("want pkg=bin[,dir], got %q", v)
		}
		bin, binDir, _ := strings.Cut(rest, ",")
		extra = append(extra, runner.Binary{Path: bin, Pkg: pkg, Dir: binDir})
		return nil
	})
	extraSrcs := map[string][]string{}
	fs.Func("extra-test-srcs", "`pkg=a_test.go,...`: the _test.go files of the -extra-test package pkg, as -test-srcs for its tests (repeatable)", func(v string) error {
		pkg, files, ok := strings.Cut(v, "=")
		if !ok || pkg == "" || files == "" {
			return fmt.Errorf("want pkg=files, got %q", v)
		}
		extraSrcs[pkg] = append(extraSrcs[pkg], strings.Split(files, ",")...)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *testBin == "" || *mutantsPath == "" {
		return errors.New("run: -test-bin and -mutants are required")
	}
	for pkg, files := range extraSrcs {
		i := slices.IndexFunc(extra, func(b runner.Binary) bool { return b.Pkg == pkg })
		if i < 0 {
			return fmt.Errorf("run: -extra-test-srcs %s: no -extra-test of that package", pkg)
		}
		extra[i].Srcs = files
	}
	th := runner.Threshold{Score: *threshold, Covered: *thresholdCovered}
	if th.Score < 0 || th.Score > 1 || th.Covered < 0 || th.Covered > 1 {
		return errors.New("run: -threshold and -threshold-covered must be between 0 and 1")
	}

	opts := runner.Options{
		TestBin: *testBin, ExtraTests: extra, Dir: *dir, Args: fs.Args(), Subtests: *subtests,
		ConfirmKills: *confirmKills, ConfirmBaseline: *confirmBaseline, CountSuspect: *countSuspect,
		Timeout: *timeout, TimeoutFactor: *timeoutFactor, TimeoutConst: *timeoutConst, MinTimeout: *minTimeout, Jobs: *jobs, Log: stderr,
	}
	if err := readJSON(*mutantsPath, &opts.Mutants); err != nil {
		return err
	}
	if *tests != "" {
		opts.Tests = strings.Split(*tests, ",")
	}
	if *testSrcs != "" {
		opts.TestSrcs = strings.Split(*testSrcs, ",")
	}
	if *previous != "" {
		var err error
		if opts.Previous, err = runner.ReadReport(*previous); err != nil {
			return err
		}
	}
	if *inDiff != "" {
		var err error
		if opts.InDiff, err = runner.ReadDiff(*inDiff); err != nil {
			return err
		}
		opts.DiffExpand = *diffExpand
	}
	var err error
	if opts.Shard, opts.Shards, err = shardEnv(); err != nil {
		return err
	}

	rep, err := runner.Run(ctx, opts)
	if err != nil {
		return err
	}
	if *out == "" {
		if d := os.Getenv("TEST_UNDECLARED_OUTPUTS_DIR"); d != "" {
			*out = filepath.Join(d, "report.json")
		}
	}
	if err := writeJSON(*out, stdout, rep); err != nil {
		return err
	}
	if err := th.Check(rep); err != nil {
		return thresholdError{err}
	}
	return nil
}

// thresholdError is a run that completed, report written, but whose score
// is below `run -threshold` or `-threshold-covered`.
type thresholdError struct{ error }

// minimizeOutput is the JSON of `mutrim minimize`.
type minimizeOutput struct {
	minimize.Result
	WeakSpots []runner.Spot `json:"weak_spots"`
	// Flaky lists the tests `run -confirm-baseline` found unreliable.
	// They take no part in the cover, so they are neither selected nor
	// called redundant; deciding about them needs the flakiness fixed first.
	Flaky []string `json:"flaky_tests"`
}

// runMinimize is `mutrim minimize`: it composes the site-coverage and
// kill matrices of the given reports, runs the weighted greedy set cover,
// and reports. Nothing is deleted. A row is one test wherever it appears:
// the shards of a package share their rows, and so do the reports of
// several packages once each row is qualified by its package (see
// rowNames), so a test of package b that `run -extra-test` ran against
// package a's mutants is one row with the sites and kills of both.
func runMinimize(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("minimize", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", "write the result here instead of stdout")
	mutantsPath := fs.String("mutants", "", "mutants.json of the reports; enables the weak_spots listing")
	keep := fs.String("keep", `^TestRegression_`, "regexp of test names (TestX/case for a subtest, without the package) that are always kept")
	tag := fs.String("tag", "mutrim:keep", "tests whose doc comment contains this are always kept, with their subtests (needs -srcs)")
	srcs := fs.String("srcs", "", "comma-separated _test.go files or directories to scan for -tag")
	wSite := fs.Float64("w-site", 1, "weight of a reached mutant site")
	wKill := fs.Float64("w-kill", 5, "weight of a killed mutant")
	matrixPath := fs.String("matrix", "", "also write the composed test × requirement matrix here, for an exact solver")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("minimize: at least one report.json is required")
	}
	keepRE, err := regexp.Compile(*keep)
	if err != nil {
		return fmt.Errorf("minimize: -keep: %w", err)
	}

	var reports []*runner.Report
	pkgs := map[string]bool{}
	for _, path := range fs.Args() {
		r, err := runner.ReadReport(path)
		if err != nil {
			return err
		}
		reports = append(reports, r)
		if r.Pkg != "" {
			pkgs[r.Pkg] = true
		}
	}
	names := rowNames(reports, len(pkgs) > 1)
	row := func(i int, test string) rowName {
		if n, ok := names[i][test]; ok {
			return n
		}
		return rowName{name: test, local: test}
	}
	// A flaky test's observations are not trustworthy, so flakyOf and
	// inputsOf leave it out of the matrix entirely; see their comments.
	flaky := flakyOf(reports, row)
	durations, reached, local, kills := inputsOf(reports, row, flaky)
	sites := criteria.SiteCoverage{}
	for name, ids := range reached {
		sites[name] = slices.Sorted(maps.Keys(ids))
	}
	matrix := criteria.Compose(durations,
		criteria.Weighted{Criterion: sites, Weight: *wSite},
		criteria.Weighted{Criterion: kills, Weight: *wKill},
	)
	if *matrixPath != "" {
		if err := writeJSON(*matrixPath, nil, matrix); err != nil {
			return err
		}
	}

	tagged := map[string]bool{}
	if *srcs != "" {
		names, err := minimize.Tagged(strings.Split(*srcs, ","), *tag)
		if err != nil {
			return err
		}
		for _, n := range names {
			tagged[n] = true
		}
	}
	// A tag sits on the top-level test and protects its subtests; the
	// regexp sees the full name within its package, so ^TestRegression_
	// matches those too.
	protected := func(name string) bool {
		name = local[name]
		top, _, _ := strings.Cut(name, "/")
		return tagged[top] || keepRE.MatchString(name)
	}
	res := minimize.Greedy(matrix, minimize.Options{Protected: protected})
	minimize.Exclusives(matrix, &res)
	result := minimizeOutput{
		Result:    res,
		WeakSpots: []runner.Spot{},
		Flaky:     slices.Sorted(maps.Keys(flaky)),
	}
	if *mutantsPath != "" {
		var mutants []mutator.Mutant
		if err := readJSON(*mutantsPath, &mutants); err != nil {
			return err
		}
		result.WeakSpots = runner.WeakSpots(mutants, reports...)
	}
	return writeJSON(*out, stdout, result)
}

// rowName is a row of the minimize matrix: its name there, and its name
// within its package, which the protection rules match.
type rowName struct{ name, local string }

// rowNames maps, per report, the test names the report uses (in tests and
// killed_by) to the rows of the matrix. A report names the mutated
// package's own tests bare and another package's as pkg.TestX; with
// qualify (the reports span several packages) the bare names are
// qualified with the report's package too, so the same test gets the same
// row in every report and two packages' TestX never collide.
func rowNames(reports []*runner.Report, qualify bool) []map[string]rowName {
	out := make([]map[string]rowName, len(reports))
	for i, r := range reports {
		out[i] = map[string]rowName{}
		for _, t := range r.Tests {
			n := rowName{name: t.Name, local: t.Name}
			switch {
			case t.Pkg != "":
				n.local = strings.TrimPrefix(t.Name, t.Pkg+".")
			case qualify && r.Pkg != "":
				n.name = r.Pkg + "." + t.Name
			}
			out[i][t.Name] = n
		}
	}
	return out
}

// flakyOf returns the set of matrix rows that are flaky in any report.
// A flaky test's observations are not trustworthy, so it is left out of
// the matrix entirely: it covers nothing, is never selected and is never
// called redundant, and is reported on its own instead. Its suspicious
// pairs are out already, since the runner keeps them out of killed_by.
func flakyOf(reports []*runner.Report, row func(i int, test string) rowName) map[string]bool {
	flaky := map[string]bool{}
	for i, r := range reports {
		for _, t := range r.Tests {
			if t.Flaky {
				flaky[row(i, t.Name).name] = true
			}
		}
	}
	return flaky
}

// inputsOf collects the inputs of the minimize matrix from the reports,
// skipping every flaky row: the running time and reached sites per test,
// the name the protection rules match, and the mutants each test kills.
// A RUN_ERROR mutant is no requirement either: no test failed, so it
// keeps no test alive, and its function is a weak spot only through its
// other mutants.
func inputsOf(reports []*runner.Report, row func(i int, test string) rowName, flaky map[string]bool) (durations map[string]int64, reached map[string]map[string]bool, local map[string]string, kills criteria.Mutation) {
	durations = map[string]int64{}
	reached = map[string]map[string]bool{}
	local = map[string]string{}
	kills = criteria.Mutation{}
	for i, r := range reports {
		for _, t := range r.Tests {
			n := row(i, t.Name)
			if flaky[n.name] {
				continue
			}
			local[n.name] = n.local
			durations[n.name] = max(durations[n.name], t.DurationMS)
			if reached[n.name] == nil {
				reached[n.name] = map[string]bool{}
			}
			for _, id := range t.Sites {
				reached[n.name][id] = true
			}
		}
		for _, res := range r.Results {
			for _, t := range res.KilledBy {
				if n := row(i, t).name; !flaky[n] {
					kills[n] = append(kills[n], res.MutantID)
				}
			}
		}
	}
	return durations, reached, local, kills
}

// runReport is `mutrim report`: it renders the reports of a run (the
// shards of a package, or several packages) in a format other tools read.
// Only -format stryker is JSON; html and github are written as they are,
// since GitHub reads its annotations from the step's stdout.
func runReport(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "stryker", "output format: \"stryker\" (mutation-testing-elements JSON), \"html\" (that JSON in its single-file viewer) or \"github\" (GitHub Actions annotations)")
	mutantsPath := fs.String("mutants", "", "mutants.json of the reports (required)")
	srcs := fs.String("srcs", "", "comma-separated files or directories holding the mutated sources, for the source text of the HTML view")
	out := fs.String("o", "", "write the report here instead of stdout")
	maxPerLine := fs.Int("max-per-line", 0, "annotate at most this many mutants per source line (with -format github); 0 is no cap")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *mutantsPath == "" {
		return errors.New("report: -mutants is required")
	}
	if fs.NArg() == 0 {
		return errors.New("report: at least one report.json is required")
	}
	var mutants []mutator.Mutant
	if err := readJSON(*mutantsPath, &mutants); err != nil {
		return err
	}
	var reports []*runner.Report
	for _, path := range fs.Args() {
		r, err := runner.ReadReport(path)
		if err != nil {
			return err
		}
		reports = append(reports, r)
	}
	var sources *report.Sources
	if *srcs != "" {
		var err error
		if sources, err = report.NewSources(strings.Split(*srcs, ",")); err != nil {
			return err
		}
	}

	switch *format {
	case "stryker":
		return writeJSON(*out, stdout, report.ToStryker(mutants, reports, sources))
	case "html":
		return writeTo(*out, stdout, func(w io.Writer) error {
			return report.WriteHTML(w, report.ToStryker(mutants, reports, sources))
		})
	case "github":
		return writeTo(*out, stdout, func(w io.Writer) error {
			return report.WriteAnnotations(w, mutants, reports, *maxPerLine)
		})
	default:
		return fmt.Errorf("report: unknown -format %q", *format)
	}
}

// runBazelTest is `mutrim bazel-test`, the test executable of the
// mutation_test macro. Its flags and arguments are runfiles paths: the
// arguments are the library sources, which the reports quote; flags after
// "--" go to `mutrim run`. It runs the mutants, then writes report.json,
// minimize.json and mutation-report.json / .html to
// TEST_UNDECLARED_OUTPUTS_DIR. MUTRIM_IN_DIFF, when set, is the diff the
// run is scoped to (`run -in-diff`).
func runBazelTest(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("bazel-test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	testBin := fs.String("test-bin", "", "runfiles path of the schemata test binary (required)")
	mutantsPath := fs.String("mutants", "", "runfiles path of mutants.json (required)")
	var testSrcs []string
	fs.Func("test-src", "runfiles path of a test source, scanned for the mutrim:keep tag and hashed for report.json (repeatable)", func(s string) error {
		testSrcs = append(testSrcs, s)
		return nil
	})
	var extraTests []string
	fs.Func("extra-test", "runfiles path of a mutrim_relink manifest: another package's test binary and test sources (repeatable)", func(s string) error {
		extraTests = append(extraTests, s)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *testBin == "" || *mutantsPath == "" {
		return errors.New("bazel-test: -test-bin and -mutants are required")
	}
	out := os.Getenv("TEST_UNDECLARED_OUTPUTS_DIR")
	if out == "" {
		return errors.New("bazel-test: TEST_UNDECLARED_OUTPUTS_DIR is not set; run it under bazel test")
	}
	libSrcs, runFlags := fs.Args(), []string(nil)
	if i := slices.Index(libSrcs, "--"); i >= 0 {
		libSrcs, runFlags = libSrcs[:i], libSrcs[i+1:]
	}

	rf, err := runfiles.New()
	if err != nil {
		return err
	}
	files, err := rlocations(rf, []string{*testBin, *mutantsPath})
	if err != nil {
		return err
	}
	bin, mutants := files[0], files[1]
	if testSrcs, err = rlocations(rf, testSrcs); err != nil {
		return err
	}
	allTestSrcs := testSrcs
	if libSrcs, err = rlocations(rf, libSrcs); err != nil {
		return err
	}
	var extraFlags []string
	for _, path := range extraTests {
		extra, srcs, err := readRelink(rf, path)
		if err != nil {
			return err
		}
		extraFlags = append(extraFlags, "-extra-test", extra)
		if len(srcs) > 0 {
			pkg, _, _ := strings.Cut(extra, "=")
			extraFlags = append(extraFlags, "-extra-test-srcs", pkg+"="+strings.Join(srcs, ","))
		}
		allTestSrcs = append(allTestSrcs, srcs...)
	}

	rep := filepath.Join(out, "report.json")
	// The run, minimize and report handlers are called directly, not
	// through run: bazel-test is itself dispatched by run, and a call back
	// into it would recurse.
	runArgs := slices.Concat(runFlags, extraFlags, []string{"-test-bin", bin, "-mutants", mutants, "-out", rep})
	if len(testSrcs) > 0 {
		runArgs = append(runArgs, "-test-srcs", strings.Join(testSrcs, ","))
	}
	if diff := os.Getenv("MUTRIM_IN_DIFF"); diff != "" {
		runArgs = append(runArgs, "-in-diff", diff)
	}
	// A score below the threshold still wrote report.json, so the other
	// outputs are written before the test fails.
	below := runRun(ctx, runArgs, stdout, stderr)
	if below != nil && !errors.As(below, new(thresholdError)) {
		return below
	}
	// Each output reads only report.json, so one failing leaves the others
	// written.
	srcs := strings.Join(libSrcs, ",")
	return errors.Join(
		below,
		runMinimize([]string{"-mutants", mutants, "-srcs", strings.Join(allTestSrcs, ","), "-o", filepath.Join(out, "minimize.json"), rep}, stdout, stderr),
		runReport([]string{"-format", "stryker", "-mutants", mutants, "-srcs", srcs, "-o", filepath.Join(out, "mutation-report.json"), rep}, stdout, stderr),
		runReport([]string{"-format", "html", "-mutants", mutants, "-srcs", srcs, "-o", filepath.Join(out, "mutation-report.html"), rep}, stdout, stderr),
	)
}

// relink is the manifest mutrim_relink writes for an extra test: the
// package its binary tests, and the binary and the package's test sources
// as runfiles paths.
type relink struct {
	Pkg  string   `json:"pkg"`
	Bin  string   `json:"bin"`
	Srcs []string `json:"srcs"`
}

// readRelink reads the manifest at runfiles path path and returns the
// `run -extra-test` value of its binary and its resolved test sources.
func readRelink(rf *runfiles.Runfiles, path string) (extraTest string, srcs []string, err error) {
	files, err := rlocations(rf, []string{path})
	if err != nil {
		return "", nil, err
	}
	var m relink
	if err = readJSON(files[0], &m); err != nil {
		return "", nil, err
	}
	if m.Pkg == "" || m.Bin == "" {
		return "", nil, fmt.Errorf("bazel-test: %s: pkg and bin are required", path)
	}
	resolved, err := rlocations(rf, append([]string{m.Bin}, m.Srcs...))
	if err != nil {
		return "", nil, err
	}
	return m.Pkg + "=" + resolved[0], resolved[1:], nil
}

// rlocations resolves runfiles paths to paths on disk.
func rlocations(rf *runfiles.Runfiles, paths []string) ([]string, error) {
	resolved := make([]string, len(paths))
	for i, p := range paths {
		var err error
		if resolved[i], err = rf.Rlocation(p); err != nil {
			return nil, err
		}
	}
	return resolved, nil
}

// shardEnv reads Bazel's sharding protocol and acknowledges it by touching
// TEST_SHARD_STATUS_FILE.
func shardEnv() (index, total int, err error) {
	if s := os.Getenv("TEST_TOTAL_SHARDS"); s != "" {
		if total, err = strconv.Atoi(s); err != nil {
			return 0, 0, fmt.Errorf("TEST_TOTAL_SHARDS: %w", err)
		}
		if index, err = strconv.Atoi(os.Getenv("TEST_SHARD_INDEX")); err != nil {
			return 0, 0, fmt.Errorf("TEST_SHARD_INDEX: %w", err)
		}
	}
	if f := os.Getenv("TEST_SHARD_STATUS_FILE"); f != "" {
		if err := os.WriteFile(filepath.Clean(f), nil, 0o600); err != nil {
			return 0, 0, err
		}
	}
	return index, total, nil
}

func patterns(fs *flag.FlagSet) []string {
	if fs.NArg() == 0 {
		return []string{"."}
	}
	return fs.Args()
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// writeTo runs write against path, or stdout when path is empty.
func writeTo(path string, stdout io.Writer, write func(io.Writer) error) error {
	var buf bytes.Buffer
	if err := write(&buf); err != nil {
		return err
	}
	if path != "" {
		return os.WriteFile(filepath.Clean(path), buf.Bytes(), 0o600)
	}
	_, err := stdout.Write(buf.Bytes())
	return err
}

func writeJSON(path string, stdout io.Writer, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	if path != "" {
		return os.WriteFile(filepath.Clean(path), buf.Bytes(), 0o600)
	}
	_, err := stdout.Write(buf.Bytes())
	return err
}
