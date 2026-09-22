# mutrim

Bazel-native mutation testing for Go that also uses the results to minimize test suites.
Pre-alpha; see [docs/plan.md](docs/plan.md) for the roadmap.

## Usage (non-Bazel dev loop)

```bash
# List type-checked-viable mutants of a package as JSON.
go run ./cmd/mutrim gen ./path/to/pkg > mutants.json

# Select operators: names, "default", and "-name" to remove one.
go run ./cmd/mutrim gen -operators default,-constant ./path/to/pkg

# Narrow the sites: by function name, by file, or by the mutant itself.
go run ./cmd/mutrim gen -match '^\(\*Tree\)\.' -exclude-files '_gen\.go$' \
  -exclude-re 'constant: 0 -> 1' ./path/to/pkg

# Apply one mutant through go build -overlay and run the package's tests.
go run ./cmd/mutrim overlay -id <mutant id> -o overlay.json ./path/to/pkg
go test -overlay overlay.json ./path/to/pkg
```

A failing `go test` means the mutant was killed.

`-match <re>` keeps only the functions whose name matches, in the `(*T).Name` form of the
`func` field; `-files <glob,...>` keeps only the files matching one of the globs and
`-exclude-files <re,...>` drops the files matching one of the regexps (both are tried
against the path as reported, its path relative to the working directory, and its base
name); `-exclude-re <re>` drops the mutants whose `func operator: description` matches, so
a rewrite can be excluded wherever it occurs. A filtered mutant is still listed, with
`"ignored"` naming the rule that rejected it, and `run` reports it `IGNORED` without
building or executing it — the counts of a filtered run stay comparable with an unfiltered
one, and `IGNORED` mutants count towards no score.

## Usage (schemata: one build, all mutants)

```bash
# Write sources with every mutant embedded, plus overlay.json and mutants.json.
go run ./cmd/mutrim gen -schemata out -o mutants.json ./path/to/pkg

# Build the test binary once from the schemata sources.
go test -c -overlay out/overlay.json -o pkg.test ./path/to/pkg

# Re-execute it once per mutant and write report.json.
go run ./cmd/mutrim run -test-bin pkg.test -mutants mutants.json -dir ./path/to/pkg -out report.json

# Report redundant tests and functions whose mutants survive. Never deletes anything.
go run ./cmd/mutrim minimize -mutants mutants.json -srcs ./path/to/pkg report.json
```

The schemata sources import `github.com/illumination-k/mutrim/mut`, so the target module
needs mutrim as a dependency. `run` honors `TEST_SHARD_INDEX` / `TEST_TOTAL_SHARDS` and
`TEST_UNDECLARED_OUTPUTS_DIR`, and `-previous report.json` copies earlier results forward
so only new mutants execute.

`run` first runs every top-level test on its own with `GOMUTANT_TRACE` set, which makes the
`mut` runtime record the mutant sites the test reaches. Each mutant then runs only against
the tests reaching it, and `report.json` holds the per-test kill matrix: `tests` (name,
duration, reached sites) and, per mutant, every test that killed it. A mutant no test reaches
is `NO_COVERAGE` and never executed.

`minimize` composes that matrix (reached sites weighted 1, kills weighted 5, per millisecond
of test time; `-w-site` / `-w-kill`) and runs a greedy set cover, then drops every selected
test whose requirements the other selected tests satisfy between them. `selected` lists the
tests kept in order with their gain, `redundant` the rest with the selected tests that subsume
each of them, and `weak_spots` (with `-mutants`) the functions whose mutants survive. Tests
matching `-keep` (default `^TestRegression_`) or tagged `//mutrim:keep` in their doc comment
(`-srcs` names the `_test.go` files or directories to scan) are always kept. `-matrix` exports
the composed test × requirement matrix as JSON for an exact solver. The shards of one package
can be passed together; reports of different packages are refused, since their test names would
collide and no test of one package covers a requirement of another.

## Usage (Bazel)

```starlark
# MODULE.bazel
bazel_dep(name = "mutrim", version = "0.1.0")
local_path_override(module_name = "mutrim", path = "../mutrim")  # until it is published
```

```starlark
# BUILD.bazel: mirror the package's go_test
load("@mutrim//:defs.bzl", "mutation_test")

mutation_test(
    name = "mutant_calc",
    srcs = ["calc_test.go"],
    embed = [":calc"],
    operators = ["default", "-constant"],  # optional; see `mutrim gen -operators`
    match = "^Compute",                    # optional; the gen filters, per attribute
    exclude_files = ["_gen\\.go$"],
    shard_count = 4,
)
```

`bazel test //pkg:mutant_calc` builds the package once with every mutant embedded, runs the
tests against it (`mutant_calc_schemata_test`, which must pass like the original tests), then
re-executes that binary once per mutant against the tests reaching it and writes
`report.json` and `minimize.json` to the test's undeclared outputs
(`bazel-testlogs/pkg/mutant_calc/test.outputs/`, one directory per shard; pass the shards'
reports together to `mutrim minimize` for a whole-package verdict). Mutants are generated
hermetically: the rule type-checks the library against the export data rules_go compiled for
its dependencies, so no `go` command runs inside the sandbox.

Not supported yet: cgo packages and `//go:embed` directives in the library under test (the
schemata sources live in a generated directory). `examples/` dogfoods the macro; run
`bazel test //...` here to see it work.

## Development

```bash
mise install
mise run ci    # fmt/lint/test; see CLAUDE.md
```

Tasks are grouped by `MISE_ENV`: `base` (formatting, GitHub Actions), `golang` (`go test`,
golangci-lint, govulncheck) and `bazel` (gazelle, buildifier, `bazel test //...`). For
example, `MISE_ENV=bazel mise run ci:bazel` runs only the Bazel checks.

Benchmark mutant generation, including the go/types pre-filter, with:

```bash
go test ./mutator -run '^$' -bench . -benchtime 3x         # fixture + go/parser
go test ./mutator -run '^$' -bench . -benchpkg go/types    # one extra package
```

`BenchmarkGenerate` reports `ms/mutant` for the full pipeline.

`mise run dogfood` (`tools/dogfood.sh`) runs the schemata pipeline on mutrim's own packages and
writes `report.json` and `minimize.json` per package under `.mutrim/`. It takes a while:
the `runner` tests build and execute test binaries, and every mutant reruns them. Pass package
directories to limit it, for example `tools/dogfood.sh criteria minimize`.
