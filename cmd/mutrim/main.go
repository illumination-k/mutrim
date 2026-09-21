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
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/illumination-k/mutrim/mutator"
	"github.com/illumination-k/mutrim/runner"
)

const usage = `usage: mutrim <command> [flags] [packages]

commands:
  gen       list mutants of the packages as JSON; -schemata also writes
            sources with every mutant embedded; -importpath type-checks
            the given files from export data (Bazel mode)
  overlay   write one mutant and print a go build -overlay file for it
  run       execute a schemata test binary once per mutant and report`

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
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}

func runGen(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", "write mutants.json here instead of stdout")
	noCheck := fs.Bool("no-check", false, "skip the go/types pre-filter")
	schemata := fs.String("schemata", "", "write schemata sources under this directory, plus overlay.json for go build")
	importPath := fs.String("importpath", "", "type-check the argument files as this package from export data instead of running go list (Bazel mode)")
	importcfg := fs.String("importcfg", "", "dependencies' export data in go build -importcfg format (with -importpath)")
	stdlib := fs.String("stdlib", "", "directory of compiled standard-library packages, <dir>/<goos_goarch>/<path>.a (with -importpath)")
	tags := fs.String("tags", "", "comma-separated build tags (with -importpath)")
	if err := fs.Parse(args); err != nil {
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
		ms := mutator.Generate(pkg, mutator.Options{TypeCheck: !*noCheck})
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
// viable, so the runner never selects them.
func writeSchemata(dir string, pkg *packages.Package, ms []mutator.Mutant, overlay *mutator.Overlay) error {
	sch, err := mutator.Lower(pkg, ms)
	if err != nil {
		return err
	}
	for i := range ms {
		ms[i].Viable = ms[i].Viable && sch.Embedded[ms[i].ID]
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
	out := fs.String("o", "", "write the overlay JSON here instead of stdout")
	dir := fs.String("dir", "", "directory for the mutated source (default: a temp dir)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("overlay: -id is required")
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
		file, src, err := mutator.Source(pkg, *id)
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
	timeout := fs.Duration("timeout", 0, "per-mutant timeout (default: 3× the baseline run)")
	tests := fs.String("tests", "", "comma-separated top-level tests to run (default: all)")
	dir := fs.String("dir", "", "working directory for the test binary")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *testBin == "" || *mutantsPath == "" {
		return errors.New("run: -test-bin and -mutants are required")
	}

	opts := runner.Options{TestBin: *testBin, Dir: *dir, Args: fs.Args(), Timeout: *timeout, Log: stderr}
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
	var err error
	if opts.Shard, opts.Shards, err = shardEnv(); err != nil {
		return err
	}

	report, err := runner.Run(ctx, opts)
	if err != nil {
		return err
	}
	if *out == "" {
		if d := os.Getenv("TEST_UNDECLARED_OUTPUTS_DIR"); d != "" {
			*out = filepath.Join(d, "report.json")
		}
	}
	return writeJSON(*out, stdout, report)
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
