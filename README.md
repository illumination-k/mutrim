# mutrim

Bazel-native mutation testing for Go that also uses the results to minimize test suites.
Pre-alpha; see [docs/plan.md](docs/plan.md) for the roadmap.

## Usage (non-Bazel dev loop)

```bash
# List type-checked-viable mutants of a package as JSON.
go run ./cmd/mutrim gen ./path/to/pkg > mutants.json

# Select operators: names, "default", and "-name" to remove one. Every operator but
# "call" (a non-void call replaced by its result's zero value) is in the default set.
go run ./cmd/mutrim gen -operators default,-constant ./path/to/pkg
go run ./cmd/mutrim gen -operators default,call ./path/to/pkg

# Narrow the sites: by function name, by file, or by the mutant itself.
go run ./cmd/mutrim gen -match '^\(\*Tree\)\.' -exclude-files '_gen\.go$' \
  -exclude-re 'constant: 0 -> 1' ./path/to/pkg

# Calls to log and slog are excluded by default; name others, or turn the rule off.
go run ./cmd/mutrim gen -exclude-calls 'default,(*Metrics).Observe' ./path/to/pkg
go run ./cmd/mutrim gen -exclude-calls none ./path/to/pkg

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
a rewrite can be excluded wherever it occurs.

`-exclude-calls <glob,...>` drops the mutants of a call whose callee matches — the call
itself and everything in its arguments — because no test asserts on what a logging call
does: removing `log.Printf("n=%d", n+1)`, or mutating the `n+1` it logs, produces a mutant
nothing can kill. The callee is matched as go/types names it, with and without the package
path (`slog.Info` and `log/slog.Info`, `(*slog.Logger).Info`). The default list is
`log.*`, `(*log.Logger).*`, `slog.*`, `(*slog.Logger).*`; an explicit list replaces it, the
entry `default` keeps it (`default,(*Metrics).Observe`), and `none` excludes no call.

A filtered mutant is still listed, with
`"ignored"` naming the rule that rejected it, and `run` reports it `IGNORED` without
building or executing it — the counts of a filtered run stay comparable with an unfiltered
one, and `IGNORED` mutants count towards no score.

## Inline directives

The `gen` flags above are the run-wide switches; comments are the local one. A site a
directive suppresses is reported exactly like a filtered one: still listed in
`mutants.json`, with `"ignored"` naming the directive and `"reason"` keeping its text, and
`run` reports it `IGNORED` without building or executing it.

```go
//mutrim:disable [op,...] [reason]            // until //mutrim:enable
//mutrim:disable-next-line [op,...] [reason]  // the line below
//mutrim:disable-func [op,...] [reason]       // in a function's doc comment
//mutrim:enable                               // closes every open disable
```

```go
func Retry(attempts int) error {
	//mutrim:disable-next-line relational,constant the bound is arbitrary
	for i := 0; i < 3; i++ {
	}
	return nil
}
```

The operator list is optional and defaults to every operator, which `all` spells explicitly.
A first word that does not name operators starts the reason instead, so a reason never needs
quoting. A `//mutrim:disable` that nothing closes runs to the end of the file. A directive
wins over the `gen` filters, which are only consulted for a site no directive covers.

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

# Render the run for another tool: Stryker JSON, its HTML viewer, CI annotations.
go run ./cmd/mutrim report -mutants mutants.json -srcs ./path/to/pkg report.json
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

## Scoping a run to a diff

`run -in-diff` runs only the mutants on the lines a unified diff adds, which is what a
pull request needs:

```bash
git diff --unified=0 --merge-base origin/main > pr.diff
go run ./cmd/mutrim run -test-bin pkg.test -mutants mutants.json -in-diff pr.diff -out report.json
```

Every other mutant is reported `SKIPPED`: it is not executed and counts towards no score,
so `score` is the mutation score of the diff. The filter is applied before everything
else, `-previous` included, so a scoped run executes nothing outside the diff.

Only the new side of the diff is read. A removed line holds no mutant, and a context line
is code the diff did not change, so a mutant is kept when its span overlaps an added line;
`_test.go` files are skipped, since mutants never live there. The diff's paths are matched
against a mutant's file by path suffix, so a repository-relative diff matches both the
absolute paths of a `go list` run and the execroot-relative ones of a Bazel run.

`gen` is unchanged: mutant IDs are content hashes, so `mutants.json` is the same either
way and a scoped run stays comparable with a full one. The diff must be taken against the
sources the test binary was built from; mutrim does not verify that, and a diff of another
tree simply selects the wrong lines.

Under Bazel the diff is passed by path, which keeps the runner hermetic:

```bash
git diff --unified=0 --merge-base origin/main > "$PWD/pr.diff"
bazel test --test_env=MUTRIM_IN_DIFF="$PWD/pr.diff" //...:all
```

## Reporting

`report` renders `report.json` in the formats other tools already read. `-mutants` is
required (it carries the source locations), the shards of a run are passed together, and
`-srcs` names the files or directories holding the mutated sources.

```bash
# Stryker mutation-testing-report-schema v2, which the Stryker dashboard accepts.
mutrim report -mutants mutants.json -srcs ./pkg report.json > mutation-report.json

# The same JSON inside the single-file mutation-test-report-app viewer.
mutrim report -format html -mutants mutants.json -srcs ./pkg -o mutation-report.html report.json

# One ::warning per surviving mutant, which GitHub Actions shows on the diff.
mutrim report -format github -mutants mutants.json report.json
```

`KILLED`, `LIVED`, `TIMEOUT` and `NO_COVERAGE` map onto the schema's `Killed`, `Survived`,
`Timeout` and `NoCoverage`; `NOT_VIABLE` is a `CompileError`, since the mutant never built,
and `IGNORED` and `SKIPPED` are `Ignored`, with the directive or filter that suppressed it,
or `not in the diff`, as its `statusReason`. Each mutant carries the tests that reach it (`coveredBy`) and the ones that
killed it (`killedBy`), so the viewer shows the kill matrix per mutant. A mutant no report
mentions is left out, so one shard's report renders that shard.

`-format html` and `-format github` write their own text rather than JSON: GitHub reads its
annotations from the step's stdout, and the HTML view loads the viewer from unpkg, so it
needs a network at display time but is a single file to publish.

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
    exclude_calls = ["default", "(*Metrics).Observe"],  # logging calls are excluded anyway
    shard_count = 4,
)
```

`bazel test //pkg:mutant_calc` builds the package once with every mutant embedded, runs the
tests against it (`mutant_calc_schemata_test`, which must pass like the original tests), then
re-executes that binary once per mutant against the tests reaching it and writes
`report.json`, `minimize.json` and the Stryker `mutation-report.json` / `mutation-report.html`
to the test's undeclared outputs (`bazel-testlogs/pkg/mutant_calc/test.outputs/`, one
directory per shard; pass the shards' reports together to `mutrim minimize` or
`mutrim report` for a whole-package verdict). Mutants are generated
hermetically: the rule type-checks the library against the export data rules_go compiled for
its dependencies, so no `go` command runs inside the sandbox. Passing
`--test_env=MUTRIM_IN_DIFF=$PWD/pr.diff` scopes the run to a diff, as above.

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
