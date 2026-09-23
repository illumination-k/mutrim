# mutrim

Bazel-native mutation testing for Go that also uses the results to minimize test suites.
Pre-alpha; see [docs/plan.md](docs/plan.md) for the roadmap.

## Usage (non-Bazel dev loop)

```bash
# List type-checked-viable mutants of a package as JSON.
go run ./cmd/mutrim gen ./path/to/pkg > mutants.json

# Select operators: names, "default", and "-name" to remove one. Every operator but
# "call" (a non-void call replaced by its result's zero value) and "concurrency" is in
# the default set.
go run ./cmd/mutrim gen -operators default,-constant ./path/to/pkg
go run ./cmd/mutrim gen -operators default,call ./path/to/pkg

# Narrow the sites: by function name, by file, or by the mutant itself.
go run ./cmd/mutrim gen -match '^\(\*Tree\)\.' -exclude-files '_gen\.go$' \
  -exclude-re 'constant: 0 -> 1' ./path/to/pkg

# Arid code (logging, sleeps, stdout writes, ...) is ignored by default; name more
# callees, or turn the built-in rules off.
go run ./cmd/mutrim gen -arid '(*Metrics).Observe' ./path/to/pkg
go run ./cmd/mutrim gen -no-arid ./path/to/pkg

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

Mutants inside _arid_ nodes are ignored as `"arid"` (Petrović et al., "State of mutation
testing at Google", ICSE-SEIP 2018): a node is arid when a rule says no test observes it,
and a compound node is arid when all of its children are — so emptying
`if err != nil { log.Print(err) }` is ignored while the condition keeps its mutants. The
built-in rules cover calls to `log`, `slog`, `zap`, `zerolog`, `fmt.Print*`, `testing`
helpers, `prometheus` / `metrics` `Inc` / `Add` / `Observe` and `time.Sleep` (the call and
everything in its arguments); `fmt.Fprint*` to `os.Stdout` / `os.Stderr`; the duration or
deadline passed to `context.WithTimeout`, `time.After` and the like; `_ = x` sinks; the
map-cache lookup `if v, ok := m[k]; ok { return v }`; `panic("unreachable")`; and the
bodies of `init` and of `String`, `Error` and `GoString` methods. `-arid <glob,...>` adds
callees, matched as go/types names them, with and without the package path (`slog.Info`
and `log/slog.Info`, `(*slog.Logger).Info`); `-no-arid` turns the built-in rules off,
leaving only the `-arid` callees.

The format argument of a printf-like call (`fmt.Errorf("boom")`, and any function whose
last parameters are `string, ...any`) is ignored as `"printf-format"` by the `string`
operator: lowered into the schemata it would no longer be constant, and vet's `printf`
check, which `go test` and rules_go's `nogo` run, would fail the build.

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
so only new mutants execute. With `-test-srcs` (the package's `_test.go` files;
`-extra-test-srcs pkg=files` for an `-extra-test`) each test in `report.json` carries the
SHA-256 of its function's source, and a result is copied forward only while its tests are
unchanged: a KILLED / TIMEOUT while every test in `killed_by` still exists, reaches the mutant
and has the same hash; any other result while the tests reaching the mutant and their hashes
are the same, so a new or rewritten test gets its chance at a survivor. `mutation_test`
passes its `srcs`.

`run` first runs every top-level test on its own with `GOMUTANT_TRACE` set, which makes the
`mut` runtime record the mutant sites the test reaches. Each mutant then runs only against
the tests reaching it, and `report.json` holds the per-test kill matrix: `tests` (name,
duration, reached sites) and, per mutant, every test that killed it. A mutant no test reaches
is `NO_COVERAGE` and never executed.

`totals` reports the run's scores next to its counts. `score` is the mutation score, (killed +
timeout) / (killed + timeout + lived + no_coverage): how much is untested. `covered_score` is
the score over the covered mutants only, (killed + timeout) / (killed + timeout + lived): how
good the tests that exist are. `coverage` is the fraction of viable mutants a test reaches,
(killed + timeout + lived) / (… + no_coverage). All three are zero when nothing is viable.

A run that dies from outside the tests — the test binary exits without any `--- FAIL:` line
after the runtime hit a `fatal error:`, the process was killed by a signal, or the binary
exited with a code the testing package never uses — is a `RUN_ERROR`, never a kill: no test
failed, so the exit says nothing about the mutant. It counts towards no score (`run -previous`
never copies it forward, so the mutant is executed again), and `minimize` ignores it: it keeps
no test alive and is no weak spot. A `--- FAIL:` or `panic:` line overrides it, since the
failure then came from inside a test: the mutant is at fault and the run is a regular kill.

Equivalent mutants are filtered in two ways. `gen` proves some equivalent statically and marks
them `"equivalent"` in `mutants.json` with the rule that did: `identity-operand` (`x * 1` →
`x / 1`, `x << 0` → `x >> 0`, `x + 0` → `x - 0` on integers, and their compound assignments)
and `non-negative-operand` (a boundary swap against a constant that `len`, `cap` or an unsigned
value cannot tell apart, `len(s) > -1` → `len(s) >= -1`). `run` reports them `EQUIVALENT`
without building or executing them. `run` also traces every mutant run: a survivor that no
test failed against and whose tests reached exactly the sites they reach without it is
`SUSPECT_EQUIVALENT` (it changed neither an outcome nor the path taken, which predicts
equivalence; Schuler & Zeller, STVR 2013). Neither counts towards the score; `run
-count-suspect` (the `count_suspect` attribute of `mutation_test`) counts suspects as survivors
again, in the score, the weak spots and the exported reports.

`run -subtests` makes each subtest a row of that matrix instead of its parent: the baseline
run names them (`=== RUN TestParse/empty_input`), each is traced on its own with
`-test.run '^TestParse$/^empty_input$'`, and per mutant the reaching subtests of one parent
run in a single process, whose `--- FAIL:` lines give the kill per subtest. A test without
subtests stays a row, and each row carries its `parent`. Table-driven suites become
minimizable per case this way, at the cost of one trace process per subtest. Subtest names
must be stable across runs (a name derived from random data is not), and subtests must be
order-independent, as with Stryker's per-test coverage: a case relying on the state a
sibling left behind can fail on its own, which `run` reports as an error.

## Cross-package kills

A package's own tests are not the only ones that exercise it: a mutant that only a
downstream package's tests catch is `LIVED` or `NO_COVERAGE` when only the package's tests
run. `run -extra-test pkg=bin[,dir]` (repeatable) adds the test binary of package `pkg`,
which imports the mutated one, built with the same overlay. Its tests are traced and run
against the mutants like the package's own, and are named `pkg.TestX` in `tests`,
`killed_by` and `suspicious_by`, with `tests[].pkg` set; the package's own tests keep their
bare names.

```bash
go test -c -overlay out/overlay.json -o app.test ./path/to/app
go run ./cmd/mutrim run -test-bin pkg.test -dir ./path/to/pkg \
  -extra-test example.com/app=app.test,./path/to/app -mutants mutants.json -out report.json
```

## Concurrency mutants

The opt-in `concurrency` operator (`-operators default,concurrency`) mimics real Go
concurrency bugs by inverting their fixes (Tu et al., "Understanding real-world concurrency
bugs in Go", ASPLOS 2019): `defer f()` removed or run on the spot, `go f()` run inline, a
send `ch <- v` removed, `make(chan T)` given a buffer of 1 and `make(chan T, n)` losing its
buffer (or, for a size computed at run time, growing by one), a select case whose channel
becomes nil so it never fires, `atomic.AddT(&x, d)` / `atomic.StoreT(&x, v)` made the plain
`x += d` / `x = v`, and `once.Do(f)` made `f()`. Removing a `close`, a `Lock` / `Unlock`, a
WaitGroup `Add` / `Done` / `Wait` or a `cancel()` is a call statement removed, which
`voidcall` already does.

Their kills depend on scheduling. Build the test binary with the race detector
(`go test -c -race`, the `race` attribute of `mutation_test`), so an introduced data race
fails the test that hits it, and confirm kills with `run -confirm-kills N`. A mutant that
deadlocks runs into the per-mutant timeout and counts as killed (`TIMEOUT`).

## Error-path mutants

Error-handling code is the least tested (Lima et al., "Assessing exception handling testing
practices in open-source libraries", 2021). The `errpath` operator, on by default, mutates
it: `fmt.Errorf("...: %w", err)` stops wrapping (`%w` -> `%v`), and with a lone `%w` returns
`err` itself; `errors.Is` / `errors.As` are forced to `true` and `false`; a `panic(x)`
statement is removed unless the function needs it as its terminating statement; and
`recover()` returns `nil` while it still stops the panic. A `return` result replaced by `nil`
(`return x, err` -> `return x, nil`, a nil pointer, map, slice or func) and a forced nil check
(`if err != nil` -> `if true` / `if false`) are the `return` and `condition` operators'
mutants, tagged with the same class.

Every mutant carries a `class` in `mutants.json` and `report.json`: `errpath`,
`concurrency` or `default`. `totals.classes` scores each class on its own, next to the
overall `score`, so "error paths: 40% killed" is visible:

```json
"classes": {
  "default": { "mutants": 120, "killed": 96, "survived": 14, "score": 0.87 },
  "errpath": { "mutants": 25, "killed": 8, "survived": 12, "score": 0.4 }
}
```

`totals.covered_score` is the score over the covered mutants only, `NO_COVERAGE` left out
(PIT's test strength): how well the tests check the code they do reach.

## Thresholds

By default `run` passes whatever the score. `run -threshold 0.8` fails the run when `score` is
below 0.8, and `-threshold-covered 0.9` when `covered_score` is below 0.9 (PIT
`mutationThreshold` / `testStrengthThreshold`, Stryker `thresholds.break`); either fails only
after `report.json` is written, and a run with nothing to score passes. The `threshold` and
`threshold_covered` attributes of `mutation_test` pass them through, turning the target into a
gate, and the other outputs are still written. Under sharding each shard checks its own
mutants; the whole-package score needs the shards' reports merged.

## Flaky tests

A kill read from one run is not always a kill. Shi, Bell and Marinov (ISSTA 2019) measured
mutation scores moving four points between identical reruns, with 9% of mutant × test pairs
unstable. A per-test kill matrix is more exposed than a score: one flaky kill is enough to
call a test essential, or another one redundant. Two flags buy confidence with reruns.

`run -confirm-kills N` reruns the tests that killed a mutant until each has failed N runs.
A failure the reruns do not reproduce is recorded in `suspicious_by` instead of `killed_by`
(mutmut's `SUSPICIOUS`), so it is no kill and no requirement; when every killer of a mutant
turns suspicious the mutant is `LIVED`. Only the killing tests are rerun, so the cost is
proportional to the kills, not to the suite.

`run -confirm-baseline N` runs each test N times while tracing instead of once. A test that
fails in some of those runs and passes in others is marked `"flaky": true` in `tests[]`
rather than failing the run; one that fails every time is still an error, as a failing
baseline always is. A flaky test's failure under a mutant never counts as a kill, and
`minimize` leaves it out of the matrix entirely — never selected, never called redundant,
listed in `flaky_tests` instead, since its observations cannot support either verdict.
`totals.suspicious` counts the mutants with at least one unconfirmed kill, which says how
much of a run to distrust.

```bash
go run ./cmd/mutrim run -test-bin pkg.test -mutants mutants.json \
  -confirm-baseline 3 -confirm-kills 3 -out report.json
```

Each mutant's timeout follows the tests reaching it: `-timeout-factor` (3) × their traced
durations + `-timeout-const` (2s), at least `-min-timeout` (10s) and at most
`-timeout-factor` × the baseline run, so a mutant looping forever in a hot function reached by
one quick test gives up long before the whole suite's worth. Each result records its
`timeout_ms`; `-timeout` sets one timeout for every mutant instead. Every looping mutant waits
out the floor, which dominates a run of fast unit tests: `-min-timeout 1s` ran mutrim's own
`minimize` package in 5s instead of 41s with the same verdicts. Keep the default for tests that
spawn processes or touch the network, whose worst case strays far from their traced time.

Tracing and the mutants' runs execute `-jobs` test processes at once (default GOMAXPROCS);
results keep the order of `mutants.json` whatever order the runs finish in.

`minimize` composes that matrix (reached sites weighted 1, kills weighted 5, per millisecond
of test time; `-w-site` / `-w-kill`) and runs a greedy set cover, then drops every selected
test whose requirements the other selected tests satisfy between them. `selected` lists the
tests kept in order with their gain, `redundant` the rest with the selected tests that subsume
each of them, and `weak_spots` (with `-mutants`) the functions whose mutants survive. Tests
matching `-keep` (default `^TestRegression_`) or tagged `//mutrim:keep` in their doc comment
(`-srcs` names the `_test.go` files or directories to scan) are always kept; `-keep` sees the
full `TestX/case` name of a subtest row, and a tag on the parent keeps every one of its
subtests; with qualified rows both match the name within its package. `-matrix` exports
the composed test × requirement matrix as JSON for an exact solver. The shards of one package
can be passed together, and so can the reports of several packages: each bare test name is
then qualified with its report's package, so a test is one row wherever it appears — the
tests of `app` that ran against `pkg`'s mutants with `-extra-test` and against `app`'s own
mutants carry the sites and kills of both.

## Scoping a run to a diff

`run -in-diff` runs only the mutants a unified diff is relevant to, which is what a
pull request needs:

```bash
git diff --unified=0 --merge-base origin/main > pr.diff
go run ./cmd/mutrim run -test-bin pkg.test -mutants mutants.json -in-diff pr.diff -out report.json
```

The scope is the commit-relevant mutants, found through the per-test trace: the mutants
on the lines the diff adds (`changed: true` in `report.json`), the tests reaching any of
them (they exercise the change), and every mutant those tests reach, in whichever file of
the package. Most commit-relevant mutants lie outside the changed lines (Ojdanić et al.,
TOSEM 2023), so the lines alone miss them; `-diff-expand=false` keeps the lines alone.

Every other mutant is reported `SKIPPED`: it is not executed and counts towards no score,

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
# the changed lines alone
bazel test --test_env=MUTRIM_IN_DIFF="$PWD/pr.diff" --test_arg=-diff-expand=false //...:all
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

# One ::warning per surviving mutant, which GitHub Actions shows on the diff;
# -max-per-line 1 surfaces one mutant per line, as Google's code review does.
mutrim report -format github -max-per-line 1 -mutants mutants.json report.json
```

`KILLED`, `LIVED`, `TIMEOUT` and `NO_COVERAGE` map onto the schema's `Killed`, `Survived`,
`Timeout` and `NoCoverage`; `RUN_ERROR` is a `RuntimeError`, which the schema keeps out of the
score; `NOT_VIABLE` is a `CompileError`, since the mutant never built,
and `IGNORED`, `SKIPPED`, `EQUIVALENT` and `SUSPECT_EQUIVALENT` are `Ignored`, with the
directive or filter that suppressed it, `not in the diff`, or the equivalence, as its
`statusReason` (a suspect is `Survived` under `-count-suspect`). Each mutant carries the tests that reach it (`coveredBy`) and the ones that
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
    embedsrcs = ["testdata/golden.txt"],   # optional; files the tests' //go:embed name
    operators = ["default", "-constant"],  # optional; see `mutrim gen -operators`
    match = "^Compute",                    # optional; the gen filters, per attribute
    exclude_files = ["_gen\\.go$"],
    arid = ["(*Metrics).Observe"],         # optional; extends the built-in arid rules
    subtests = True,                       # optional; `mutrim run -subtests`
    confirm_kills = 3,                     # optional; `mutrim run -confirm-kills`
    confirm_baseline = 3,                  # optional; `mutrim run -confirm-baseline`
    extra_tests = ["//app:app_test"],      # optional; `mutrim run -extra-test`
    threshold = 0.8,                       # optional; `mutrim run -threshold`
    jobs = 4,                              # optional; `mutrim run -jobs`, pair with tags
    min_timeout = "2s",                    # optional; `mutrim run -min-timeout`
    tags = ["cpu:4"],
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
its dependencies, so no `go` command runs inside the sandbox. `extra_tests` names the
`go_test`s of packages importing the library: each is relinked against the schemata library
(`mutant_calc_extra0`, ...), recompiling every package between the test and the library as
`go_test` itself does for external tests, and its tests run against the mutants too; the
target must be visible to the `mutation_test`'s package, and `race` cannot be set with it yet. Passing
`--test_env=MUTRIM_IN_DIFF=$PWD/pr.diff` scopes the run to a diff, as above.

`//go:embed` works on both sides: the library's `embedsrcs` come with `embed` (rules_go
resolves the patterns against the embedded files, wherever the schemata sources live), and
the tests' are passed as `embedsrcs`, as to `go_test`. Not supported yet: cgo packages.
`examples/` dogfoods the macro; run
`bazel test //...` here to see it work.

## Development

```bash
mise install
mise run ci    # fmt/lint/test; see CLAUDE.md
```

Tasks are grouped by `MISE_ENV`: `base` (formatting, GitHub Actions), `golang` (`go test`,
golangci-lint, govulncheck) and `bazel` (gazelle, buildifier, `bazel test //...`). For
example, `MISE_ENV=bazel mise run ci:bazel` runs only the Bazel checks.

`mise run bench` runs every benchmark and summarizes it with
[benchstat](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat); given a git ref it first
runs the same benchmarks at that ref in a temporary worktree and compares the two, which is
how a performance change should be judged:

```bash
mise run bench origin/main                  # working tree vs main, 6 runs each
BENCH=Greedy PKGS=./minimize mise run bench # one benchmark, no comparison
go test ./mutator -run '^$' -bench . -benchpkg go/types    # an extra package to generate for
```

`BENCH` (the `-bench` regexp), `COUNT` (runs per benchmark) and `PKGS` narrow it; the raw
results stay in `.bench/`.

| Package    | Benchmark           | Measures                                                                                     |
| ---------- | ------------------- | -------------------------------------------------------------------------------------------- |
| `mutator`  | `BenchmarkGenerate` | mutant generation with the go/types pre-filter, per mutant                                   |
| `mut`      | `BenchmarkSite`     | one schemata site: identity build, another mutant active, traced, traced from all goroutines |
| `runner`   | `BenchmarkRun`      | a whole run of the schemata fixture without its looping mutants, 1 and GOMAXPROCS jobs       |
| `minimize` | `BenchmarkGreedy`   | the set cover, from 100 tests × 1000 sites to 3000 × 30000                                   |
| `minimize` | `BenchmarkCompose`  | building the matrix the cover runs on, same sizes                                            |

`mise run dogfood` (`tools/dogfood.sh`) runs the schemata pipeline on mutrim's own packages and
writes `report.json` and `minimize.json` per package under `.mutrim/`. It takes a while:
the `runner` tests build and execute test binaries, and every mutant reruns them. Pass package
directories to limit it, for example `tools/dogfood.sh criteria minimize`. Each phase's wall
time (gen, build, run, minimize) lands in `.mutrim/timings.tsv`, so it is also the end-to-end
benchmark.
