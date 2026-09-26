---
title: Other languages
description: "Minimize a TypeScript or Rust test suite from Stryker, cargo-mutants and per-test coverage."
---

The minimizer (`mutrim minimize`) only needs the rows of a test × requirement matrix: which
code each test reaches and which mutants it kills. `mutrim import` reads those rows from
other languages' tools into an **observations** file, and `minimize` takes observations
files next to (or instead of) Go `report.json` files. mutrim runs none of those tools.

| Tool                                   | `mutrim import` | Rows it gives                          |
| -------------------------------------- | --------------- | -------------------------------------- |
| StrykerJS (mutation-testing-elements)  | `stryker`       | sites reached, mutants killed          |
| cargo-mutants (`mutants.out`)          | `cargo-mutants` | mutants killed, durations with nextest |
| vitest / jest coverage (Istanbul JSON) | `istanbul`      | statements one test executes           |
| `cargo llvm-cov --json`                | `llvm-cov`      | code regions one test executes         |
| JUnit XML (vitest, nextest)            | `junit`         | durations                              |

The files of one suite merge by test name, so each adapter names a test the same way:

- TypeScript: `<test file>#<describe blocks and test, joined by spaces>`, as StrykerJS
  names it (`src/calc.test.ts#clamp low`).
- Rust: `<crate>::<test path>`, the crate being the test binary's: the library's for a
  unit test, the file's for an integration test (`rsdemo::tests::low`, `sign::pos`).

Requirements are labeled by source range (`src/calc.ts:2:7-2:13`) or by mutant
(`src/lib.rs:2:10: replace < with == in clamp`), so the rows of two files that share a
label share a requirement.

## TypeScript: vitest + StrykerJS

Stryker must record every test reaching and killing each mutant: per-test coverage, and
no bail after the first kill (by default only one killer is recorded, which makes every
test look essential).

```json
{
  "testRunner": "vitest",
  "coverageAnalysis": "perTest",
  "disableBail": true,
  "reporters": ["json"]
}
```

```sh
npx stryker run
mkdir -p obs
mutrim import stryker -o obs/stryker.json reports/mutation/mutation.json

# Durations (optional): vitest's JUnit report.
npx vitest run --reporter=junit --outputFile=junit.xml
mutrim import junit -runner vitest -o obs/junit.json junit.xml
```

That is already enough to minimize: Stryker's `coveredBy` is the site coverage. Block
coverage, which also sees code without a mutant, takes one coverage run per test:

```sh
jq -r '.tests[].name' obs/stryker.json | while IFS= read -r t; do
  file=${t%%#*} name=${t#*#}
  pattern="^$(printf '%s' "$name" | sed 's/[][\.*^$(){}?+|/]/\\&/g')\$"
  npx vitest run --coverage.enabled --coverage.reporter=json -t "$pattern" "$file"
  mutrim import istanbul -test "$t" -o "obs/cov-$(printf '%s' "$t" | md5sum | cut -c1-8).json" \
    coverage/coverage-final.json
done

mutrim minimize obs/*.json
```

`-root` (default: the current directory) makes the coverage paths relative, and leaves out
files outside it; `-exclude-files` drops more by regexp. vitest leaves the test files out
of coverage itself.

Stryker's vitest runner selects a test by the space-joined name; with a vitest version that
matches names differently, Stryker reports every test of a `describe` as reaching the
mutants but killing none. Check the report's `killedBy` before minimizing.

## Rust: cargo-mutants + cargo llvm-cov

cargo-mutants runs the whole suite against each mutant and only records whether it failed;
`import cargo-mutants` reads the failing tests back from each mutant's log. A failing test
binary stops `cargo test` from running the next ones, so pass `--no-fail-fast`:

```sh
cargo mutants --cargo-test-arg=--no-fail-fast
# or, with durations: cargo mutants --test-tool=nextest --cargo-test-arg=--no-fail-fast
mkdir -p obs
mutrim import cargo-mutants -o obs/mutants.json mutants.out
```

A caught mutant whose log names no failing test (a crash, a doctest harness failing) is
reported on stderr and kills nothing; timeouts and unviable mutants are left out; missed
ones count as survivors.

Coverage per test, with the library crate named in `lib`:

```sh
lib=rsdemo
jq -r '.tests[].name' obs/mutants.json | while IFS= read -r t; do
  crate=${t%%::*} path=${t#*::}
  if [ "$crate" = "$lib" ]; then target=--lib; else target="--test $crate"; fi
  cargo llvm-cov --json --output-path cov.json $target -- --exact "$path"
  mutrim import llvm-cov -test "$t" -o "obs/cov-$t.json" cov.json
done

mutrim minimize obs/*.json
```

A test's own body is executed by that test alone and would make every test essential, so
`import llvm-cov` leaves out the test code: files under `tests/`, `benches/` and `examples/`
(`-exclude-files`) and functions whose demangled path has a `tests` module
(`-exclude-fn '(^|::)tests(::|$)'`). Adjust both to the project's layout.

## What minimize reports

The selection, the redundant tests with the tests subsuming them, the essential tests and
the dominator totals are computed exactly as for Go. `-keep` (a regexp over the row name)
protects tests; `-tag`, `-mutants` (weak spots) and `-blocks` (uncovered functions) read Go
sources and artifacts, and have nothing to read here. The weights (`-w-site`, `-w-block`,
`-w-kill`) apply to the imported sites, blocks and kills.
