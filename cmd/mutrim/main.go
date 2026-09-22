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
            run to the lines a unified diff adds; -confirm-kills and
            -confirm-baseline rerun to keep flaky tests out of the matrix
  minimize  from the report.json of one package (all of its shards),
            list the tests a greedy set cover finds redundant, the
            functions whose mutants survive, and the tests found flaky
  report    render report.json in an interchange format: the Stryker
            mutation-testing-elements JSON, its single-file HTML viewer,
            or GitHub Actions annotations`

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
// viable, so the runner never selects them; ignored mutants are not
// embedded either, but keep their status.
func writeSchemata(dir string, pkg *packages.Package, ms []mutator.Mutant, overlay *mutator.Overlay) error {
	sch, err := mutator.Lower(pkg, ms)
	if err != nil {
		return err
	}
	for i := range ms {
		if ms[i].Ignored == "" {
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
	previous := fs.String("previous", "", "report.json of an earlier run; its results are copied forward")
	inDiff := fs.String("in-diff", "", "unified diff (`git diff --merge-base main > pr.diff`); only the mutants on its added lines run, the rest are SKIPPED")
	timeout := fs.Duration("timeout", 0, "per-mutant timeout (default: 3× the baseline run)")
	tests := fs.String("tests", "", "comma-separated top-level tests to run (default: all)")
	subtests := fs.Bool("subtests", false, "make each subtest (TestX/case) a row of the kill matrix: traced on its own and named in killed_by; subtest names must be stable across runs")
	confirmKills := fs.Int("confirm-kills", 1, "rerun a mutant's killing tests until each has failed this many runs; a kill that does not reproduce is recorded in suspicious_by instead of killed_by")
	confirmBaseline := fs.Int("confirm-baseline", 1, "run each test this many times while tracing; one that fails in some runs and passes in others is marked flaky, and its failures are never kills")
	dir := fs.String("dir", "", "working directory for the test binary")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *testBin == "" || *mutantsPath == "" {
		return errors.New("run: -test-bin and -mutants are required")
	}

	opts := runner.Options{
		TestBin: *testBin, Dir: *dir, Args: fs.Args(), Subtests: *subtests,
		ConfirmKills: *confirmKills, ConfirmBaseline: *confirmBaseline,
		Timeout: *timeout, Log: stderr,
	}
	if err := readJSON(*mutantsPath, &opts.Mutants); err != nil {
		return err
	}
	if *tests != "" {
		opts.Tests = strings.Split(*tests, ",")
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
	return writeJSON(*out, stdout, rep)
}

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
// kill matrices of the given reports (the shards of one package), runs
// the weighted greedy set cover, and reports. Nothing is deleted. Reports
// of several packages are refused: their test names would collide, and
// no test of one package can cover a requirement of another, so nothing
// is gained by minimizing them together.
func runMinimize(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("minimize", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", "write the result here instead of stdout")
	mutantsPath := fs.String("mutants", "", "mutants.json of the reports; enables the weak_spots listing")
	keep := fs.String("keep", `^TestRegression_`, "regexp of test names (TestX/case for a subtest) that are always kept")
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
	if len(pkgs) > 1 {
		return fmt.Errorf("minimize: the reports span several packages (%s); pass one package's shards at a time", strings.Join(slices.Sorted(maps.Keys(pkgs)), ", "))
	}
	// A flaky test's observations are not trustworthy, so it is left out
	// of the matrix entirely: it covers nothing, is never selected and is
	// never called redundant, and is reported on its own instead. Its
	// suspicious pairs are out already, since the runner keeps them out of
	// killed_by. A RUN_ERROR mutant is no requirement either: no test
	// failed, so it keeps no test alive, and its function is a weak spot
	// only through its other mutants.
	flaky := map[string]bool{}
	for _, r := range reports {
		for _, t := range r.Tests {
			if t.Flaky {
				flaky[t.Name] = true
			}
		}
	}
	durations := map[string]int64{}
	sites, kills := criteria.SiteCoverage{}, criteria.Mutation{}
	for _, r := range reports {
		for _, t := range r.Tests {
			if flaky[t.Name] {
				continue
			}
			durations[t.Name] = t.DurationMS
			sites[t.Name] = t.Sites // identical across the shards of one package
		}
		for _, res := range r.Results {
			for _, t := range res.KilledBy {
				if !flaky[t] {
					kills[t] = append(kills[t], res.MutantID)
				}
			}
		}
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
	// regexp sees the full name, so ^TestRegression_ matches those too.
	protected := func(name string) bool {
		top, _, _ := strings.Cut(name, "/")
		return tagged[top] || keepRE.MatchString(name)
	}
	result := minimizeOutput{
		Result:    minimize.Greedy(matrix, minimize.Options{Protected: protected}),
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
