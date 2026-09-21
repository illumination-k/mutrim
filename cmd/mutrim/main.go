// Command mutrim generates and runs mutants for Go packages.
//
// Output on stdout is JSON only; diagnostics go to stderr.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/illumination-k/mutrim/mutator"
)

const usage = `usage: mutrim <command> [flags] [packages]

commands:
  gen       list mutants of the packages as JSON
  overlay   write one mutant and print a go build -overlay file for it`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "mutrim:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("missing command\n" + usage)
	}
	switch args[0] {
	case "gen":
		return runGen(args[1:], stdout, stderr)
	case "overlay":
		return runOverlay(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}

func runGen(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", "write mutants.json here instead of stdout")
	noCheck := fs.Bool("no-check", false, "skip the go/types pre-filter")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pkgs, err := mutator.Load(".", patterns(fs)...)
	if err != nil {
		return err
	}
	var mutants []mutator.Mutant
	for _, pkg := range pkgs {
		mutants = append(mutants, mutator.Generate(pkg, mutator.Options{TypeCheck: !*noCheck})...)
	}
	if mutants == nil {
		mutants = []mutator.Mutant{}
	}
	return writeJSON(*out, stdout, mutants)
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

func patterns(fs *flag.FlagSet) []string {
	if fs.NArg() == 0 {
		return []string{"."}
	}
	return fs.Args()
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
