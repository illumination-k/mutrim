package ingest

import (
	"bufio"
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// cargoOutcomes is the part of cargo-mutants' outcomes.json the adapter
// reads. A scenario is the string "Baseline" or {"Mutant": {...}}.
type cargoOutcomes struct {
	Outcomes []struct {
		Scenario jsontext.Value `json:"scenario"`
		Summary  string         `json:"summary"`
		LogPath  string         `json:"log_path"`
	} `json:"outcomes"`
}

// cargoMutant is the part of an outcome's mutant the adapter reads.
type cargoMutant struct {
	Name     string `json:"name"`
	File     string `json:"file"`
	Function *struct {
		Name string `json:"function_name"`
	} `json:"function"`
	Span struct {
		Start struct {
			Line int `json:"line"`
		} `json:"start"`
	} `json:"span"`
}

// place is where m sits; a mutant outside any function (a constant, a
// static) belongs to its file.
func (m cargoMutant) place() Mutant {
	p := Mutant{ID: m.Name, File: m.File, Line: m.Span.Start.Line}
	if m.Function != nil {
		p.Func = m.Function.Name
	}
	return p
}

// CargoMutants reads the mutants.out directory of a cargo-mutants run.
// cargo-mutants runs the whole suite against every mutant and records
// only whether it failed, so the killing tests are read back from each
// mutant's log: the libtest or cargo-nextest lines of a failing test,
// qualified with the crate of the test binary that ran it. The baseline
// log names every test, and with nextest their durations. A test binary
// failing stops cargo test from running the next ones, so the run must
// pass --no-fail-fast to cargo test (`--cargo-test-arg=--no-fail-fast`)
// for the kill matrix to be complete; a caught mutant whose log names no
// failing test (a crash, a doctest harness failing) is reported through
// warn and kills nothing. A missed mutant is a survivor; a timeout or an
// unviable mutant is left out. The caught and missed mutants are placed
// in their functions (Observations.Mutants).
func CargoMutants(dir string, warn io.Writer) (*Observations, error) {
	data, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "outcomes.json")))
	if err != nil {
		return nil, fmt.Errorf("cargo-mutants: %w", err)
	}
	var outcomes cargoOutcomes
	if err := json.Unmarshal(data, &outcomes); err != nil {
		return nil, fmt.Errorf("cargo-mutants: outcomes.json: %w", err)
	}
	rows := map[string]*Test{}
	add := func(name string) *Test {
		if rows[name] == nil {
			rows[name] = &Test{Name: name}
		}
		return rows[name]
	}
	o := &Observations{Source: "cargo-mutants"}
	for _, out := range outcomes.Outcomes {
		var mutant struct {
			Mutant cargoMutant
		}
		if json.Unmarshal(out.Scenario, &mutant) != nil {
			// The baseline: every test that ran is a row.
			results, err := testResults(filepath.Join(dir, out.LogPath))
			if err != nil {
				return nil, err
			}
			for _, r := range results {
				t := add(r.name)
				t.DurationMS = max(t.DurationMS, r.durationMS)
			}
			continue
		}
		name := mutant.Mutant.Name
		switch out.Summary {
		case "CaughtMutant":
			results, err := testResults(filepath.Join(dir, out.LogPath))
			if err != nil {
				return nil, err
			}
			killed := false
			for _, r := range results {
				if r.failed {
					t := add(r.name)
					t.Kills = append(t.Kills, name)
					killed = true
				}
			}
			if !killed {
				_, _ = fmt.Fprintf(warn, "cargo-mutants: %s: caught, but its log names no failing test; left out\n", name)
			}
		case "MissedMutant":
			o.Survived = append(o.Survived, name)
		}
		if out.Summary == "CaughtMutant" || out.Summary == "MissedMutant" {
			o.Mutants = append(o.Mutants, mutant.Mutant.place())
		}
	}
	o.Tests = sortedRows(rows)
	return o, nil
}

// testResult is one test a log reports.
type testResult struct {
	name       string
	failed     bool
	durationMS int64
}

var (
	// libtest: "     Running `target/debug/deps/crate-0123456789abcdef`"
	// (cargo --verbose), "     Running unittests src/lib.rs
	// (target/debug/deps/crate-0123456789abcdef)" or "     Running
	// tests/it.rs (...)" otherwise.
	libtestBinary = regexp.MustCompile("^\\s+Running (?:`([^` ]+)`|(?:\\S+ )+\\(([^)]+)\\))$")
	libtestDoc    = regexp.MustCompile(`^\s+Doc-tests (\S+)$`)
	libtestResult = regexp.MustCompile(`^test (.+?)(?: - should panic)? \.\.\. (ok|FAILED)$`)
	// nextest: "        PASS [   0.004s] (1/6) crate::binary tests::name";
	// the counter only in recent versions.
	nextestResult = regexp.MustCompile(`^\s*(PASS|FAIL|TIMEOUT|SIGSEGV|SIGABRT|ABORT|LEAK-FAIL) \[\s*([0-9.]+)s\] (?:\(\s*\d+/\d+\) )?(\S+) (.+)$`)
	binaryHash    = regexp.MustCompile(`-[0-9a-f]{16}(\.exe)?$`)
)

// testResults reads the test results a cargo test or cargo nextest log
// reports, each named "<crate>::<path>".
func testResults(path string) ([]testResult, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("cargo-mutants: %w", err)
	}
	var out []testResult
	crate := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, 1<<24)
	for sc.Scan() {
		line := sc.Text()
		if m := libtestBinary.FindStringSubmatch(line); m != nil {
			bin := filepath.Base(m[1] + m[2])
			crate = binaryHash.ReplaceAllString(bin, "")
			continue
		}
		if m := libtestDoc.FindStringSubmatch(line); m != nil {
			crate = strings.ReplaceAll(m[1], "-", "_")
			continue
		}
		if m := libtestResult.FindStringSubmatch(line); m != nil {
			out = append(out, testResult{name: crate + "::" + m[1], failed: m[2] == "FAILED"})
			continue
		}
		if m := nextestResult.FindStringSubmatch(line); m != nil {
			secs, _ := strconv.ParseFloat(m[2], 64)
			out = append(out, testResult{
				name:       nextestCrate(m[3]) + "::" + m[4],
				failed:     m[1] != "PASS",
				durationMS: int64(secs * 1000),
			})
		}
	}
	return out, sc.Err()
}

// nextestCrate is the crate of a nextest binary ID: "pkg" for the
// library's unit tests, "pkg::name" for an integration test or
// "pkg::bin/name" for a binary's, named after the target.
func nextestCrate(id string) string {
	pkg, target, ok := strings.Cut(id, "::")
	if !ok {
		target = pkg
	}
	if _, name, ok := strings.Cut(target, "/"); ok {
		target = name
	}
	return strings.ReplaceAll(target, "-", "_")
}
