# Implementation plan

This is the initial plan for building mutrim from the current scaffold. It orders the work so
that every phase leaves `mise run ci` green and produces something testable on its own.
Architecture and conventions are fixed in [CLAUDE.md](../CLAUDE.md); this document only
decides _what to build in which order_ and what "done" means for each step.

## Scope of the first release

- **In:** mutation testing for Go packages, first without Bazel (`go test -overlay`), then
  under Bazel via `mutation_test(...)` with schemata + sharding. Done when
  `bazel test //...:mutant_*` works on the `examples/` fixtures.
- **Out (later releases):** per-test coverage matrix, weighted greedy set cover, subsumption
  reports. The package skeletons exist from day one so the API can grow into them, but no
  minimizer work starts before the Bazel milestone ships.

## Repository layout

```
cmd/mutrim/          CLI: `gen`, `overlay`, `run`, later `minimize` (thin; JSON on stdout)
mutator/             AST rewriting, operators (ops_*.go), type-check pre-filter, mutant IDs,
                     mutants.json, overlay source, schemata lowering (schemata.go)
mutator/testdata/    fixture packages + golden files for the mutator tests
mut/                 runtime package imported by schemata sources; reads GOMUTANT_ID
runner/              re-exec test binary per mutant, sharding, report.json, incremental
criteria/            Criterion interface, SiteCoverage, Mutation, matrix composition
minimize/            greedy set cover, subsumption, protection rules
examples/            Bazel fixtures calling mutation_test on themselves (Phase 3)
defs.bzl, MODULE.bazel
```

Packages are created when their phase starts, not as empty placeholders.

## Phase 0 — Foundation (done)

Goal: real module layout, dependencies pinned, fixtures in place.

- Add `golang.org/x/tools` (`go/packages`) to `go.mod`; delete root placeholders.
- `mutator/testdata/` fixtures: one package per operator family with golden expectations,
  plus an "excluded files" package (`_test.go`, `.pb.go`, `mock_*.go`, generated header) and a
  `killable` package with a test for the end-to-end overlay check.
- `cmd/mutrim` uses plain `flag` subcommands; JSON to stdout, logs to stderr.

## Phase 1 — Mutator (Bazel-independent, done)

Goal: `mutrim gen ./pkg` writes a `mutants.json` listing only type-checked-viable mutants,
and `mutrim overlay` lets `go test -overlay` run a single mutant.

1. **Loading.** `go/packages` once per package with
   `NeedName|NeedFiles|NeedSyntax|NeedTypes|NeedTypesInfo|NeedDeps|NeedImports`.
   Exclusion filter applied to file names and build constraints before any rewriting.
2. **Operator interface.**
   ```go
   type Operator interface {
       Name() string                          // stable, used in mutant IDs
       Sites(ctx *Context, n ast.Node) []Site // rewrites this operator can perform on n
   }
   type Site struct {
       Node        ast.Node
       Description string   // "< -> <=" for mutants.json
       Apply, Undo func()   // in-place AST rewrite and its exact inverse
   }
   ```
   Families: one table-driven `BinaryOp` for the operator swaps plus a type per statement
   family; CLAUDE.md lists every operator mutrim ships. Only function bodies are walked.
3. **Mutant ID.** `sha256(pkgPath, enclosing func name, AST path from func root, operator
   name, description)` truncated to 16 hex chars. Position-independent by construction; a test
   asserts that inserting lines above a site does not change its ID.
4. **Type-check pre-filter.** For each binary-operator site: `Apply`, run `types.CheckExpr`
   on the rewritten expression in its original scope, record `viable=false` on error, `Undo`.
   Statement mutants are well-typed by construction and have no check. Context-dependent
   failures (constant overflow, unused import after a return replacement) are left to the
   build, which the runner counts as NOT VIABLE.
5. **`mutants.json`.** One entry per candidate:
   `{id, pkg, file, line, col, func, operator, description, viable, ignored?, reason?}`.
   Non-viable entries are kept in the file (so the runner can report NOT_VIABLE counts) but
   never executed; so are the entries a `gen` filter or an inline `//mutrim:disable`
   directive suppressed, which the runner reports IGNORED and no score counts.
6. **Overlay mode.** `mutrim overlay -id <id>` prints a `go build -overlay` JSON pointing at
   a temp file with that single mutant applied (`go/format`). This is the fast dev loop and
   the non-Bazel user path.

Done: golden tests cover every operator, the ID stability test passes, and a test runs
`go test -overlay` with a known-killable mutant on a fixture and asserts the test fails.

## Phase 2 — Schemata and runner (done)

Goal: one build per package; the test binary re-executed per mutant.

1. **Schemata lowering** (`mutator.Lower`, driven by each operator's `Site.Schemata`
   callback). Every viable mutant of a package is embedded into rewritten sources that import
   the `mut` runtime package. Helpers take the operands eagerly wherever Go evaluates them
   eagerly, so evaluation order and `recover()` semantics are untouched and the operand types
   are inferred by generics instead of being spelled out:

   | Original         | Lowered                                                     |
   | ---------------- | ----------------------------------------------------------- |
   | `a < b`          | `mut.Cmp(id, a, b, "<", "<=")` (generic over `Ordered`)     |
   | `a == b`         | `mut.Not(id, a == b)` (`!=` is exactly the negation)        |
   | `a + b`, `a % b` | `mut.Arith(id, a, b, "+", "-")`, `mut.ArithInt` for `%`     |
   | `a && b`         | `mut.And(id, a, func() bool { return b })` (short-circuit)  |
   | `if c`           | `if mut.Not(id, c)`                                         |
   | `i++`            | `mut.Inc(id, &i)`; map elements use `if mut.Active(id) {…}` |
   | `return x`       | `{ if mut.Active(id) { return <zero> }; return x }`         |

   The operators added after this phase follow the same two shapes: an expression becomes a
   helper call, a statement is wrapped in an `if mut.Active(id)`. Where several mutants sit
   on one node, one rebuilds the node and the others wrap what it left (`Site.Wraps`), so the
   lowerings nest.

   Sites the lowering declines stay as they are and their mutants are reported `NOT_VIABLE`
   under schemata: constant expressions (a call is not a constant), boolean results or
   operands of a defined type (the helpers return plain `bool`), untyped non-constant
   operands, `&&`/`||` whose right operand calls `recover()`, `m[k]++` in a `for` post
   statement, a body whose last statement makes its `switch` or `if` terminating, and a
   generic library function, which has no function value. `mutrim gen -schemata` flips
   `viable` to false for them so the runner never selects an ID the binary does not contain.

   The `mut` runtime reads `GOMUTANT_ID` once at init. With the variable unset every helper
   is the identity; `TestSchemataIdentity` runs the fixture suite against the schemata source
   and pins the generated file with a golden.
2. **Runner** (`mutrim run -test-bin <path> -mutants mutants.json`). For each viable mutant
   whose `id % TEST_TOTAL_SHARDS == TEST_SHARD_INDEX`: exec the binary with `GOMUTANT_ID=id`,
   `-test.v -test.failfast` and a timeout (issue #31: `-timeout-factor` (3) × the traced
   durations of the tests reaching the mutant + `-timeout-const` (2s), at least 10s — tests that
   spawn the Go toolchain can miss the build cache under a mutant — and at most
   `-timeout-factor` × the baseline run; `-timeout` overrides it), classify
   KILLED / LIVED / TIMEOUT / RUN_ERROR (issue #27: a run that dies from outside the tests —
   no `--- FAIL:` line, a `fatal error:`, a signal, an exit code the testing package never
   uses — is never a kill; a `--- FAIL:` or `panic:` line overrides it). `-tests` is an
   allowlist for `-test.run`; without it the whole
   binary runs (Phase 4 narrows by per-test coverage and drops failfast).
3. **`report.json`** written to `TEST_UNDECLARED_OUTPUTS_DIR` (or `-out`):
   `{mutant_id, status, tests_run, killed_by, duration_ms, timeout_ms}` plus totals, the
   baseline and the timeout cap. TIMEOUT counts as KILLED in the score; RUN_ERROR counts towards no score
   (`totals.run_error`) and is never copied forward.
4. **Incremental re-runs.** `-previous report.json`: mutants whose ID is present are copied
   forward; new IDs are executed; `NOT_VIABLE` is always recomputed from `mutants.json`. IDs
   are content hashes, so an untouched function keeps its result.

Done: `runner/testdata/schemata.golden` is the report of the schemata fixture, produced by
`gen -schemata` + `go test -c -overlay` + `run`, and every embedded mutant of a tested
function is KILLED there; the CLI test runs the same pipeline through `mutrim`.

## Phase 3 — Bazel integration (first release, done)

Goal: `bazel_dep(name = "mutrim")` + `mutation_test(name, srcs, embed, shard_count = N)`.

1. **Loading without `go list`.** A sandboxed action has no module cache, so
   `mutrim gen -importpath <path> -importcfg <file> -stdlib <dir> files...` type-checks the
   package's files with `go/types`, reading dependency types from the export data rules_go
   already compiled (`mutator.LoadFiles`, `gcexportdata`; the same approach as nogo). The
   importcfg has `go build`'s format; the standard library is found under
   `<stdlib>/<goos_goarch>/<path>.a`. Files excluded by build constraints are copied through
   untouched, so the schemata directory is always a complete copy of the package.
2. **`mutrim_schemata` rule** (`bazel/mutation_test.bzl`). Reads the library's `GoInfo` /
   `GoArchive`, writes the importcfg from `GoArchive.transitive`, runs one `MutrimGen` action
   producing the schemata sources and `mutants.json`, and provides a `GoInfo` with the same
   import path plus a dependency on `//mut`, so a `go_test` can embed it in place of the
   original library.
3. **`mutation_test` macro** expands to that rule, a `go_test` on the schemata sources (the
   identity check: with `GOMUTANT_ID` unset the tests must still pass), and an `sh_test`
   (`bazel/run.sh`) that runs `mutrim run` on the test binary with `shard_count`; `report.json`
   lands in the undeclared outputs. The runner strips Bazel's test-protocol variables
   (`TEST_TOTAL_SHARDS`, `TESTBRIDGE_TEST_ONLY`, `XML_OUTPUT_FILE`, ...) from the child
   environment: the rules_go test main acts on them too.
4. **Repository.** `MODULE.bazel` (rules_go 0.63, gazelle 0.54, rules_shell; buildifier as a
   dev dependency), gazelle-generated BUILD files, `examples/calc` and `examples/stats`
   (a dependency on another workspace package and on the standard library) dogfooding the
   macro, `mise.bazel.toml` (`fmt:bazel`, `lint:bazel`, `test:bazel`, `ci:bazel`) and the
   `ci_bazel.yml` workflow on `ubuntu-latest`.

Not supported: cgo packages, and `//go:embed` in the library under test (the schemata sources
are generated into a subdirectory, so the embed paths no longer resolve). mutrim's own tests
that shell out to `go` are excluded from the Bazel build and stay on `go test ./...`.

Done: `bazel test //...` passes and `report.json` is visible as an undeclared test output.
Tag `v0.1.0`.

## Phase 4 — Criteria and minimizer (second release, done)

Goal: a per-test kill matrix, and `mutrim minimize` reporting what it implies.

1. **Per-test coverage without `-cover`.** The plan was block coverage from
   `-test.coverprofile`, but `go test -c -cover -overlay` instruments the on-disk sources and
   ignores the overlay (the mutants are not even selectable in such a binary), and under Bazel
   instrumentation only exists in `bazel coverage`. Instead the `mut` runtime got a trace mode:
   with `GOMUTANT_TRACE=<file>` every site the process reaches appends its ID once. The
   runner lists the binary's tests (`-test.list`), runs each on its own that way, and records
   per test its duration and reached sites (`report.json` → `tests`). This is _site coverage_:
   exact for narrowing, identical under Bazel and `go test`, blind only to code with no mutant
   site at all. Block coverage can still be added as another `Criterion` later.
2. **Narrowed runner.** Each mutant runs only against the tests that reach its site, without
   failfast, so `killed_by` is the complete kill matrix (a timed-out mutant is attributed to the
   tests that started and never finished). A mutant no test reaches is `NO_COVERAGE`, never
   executed, and counts as surviving in the score. A run that dies from outside the tests —
   no `--- FAIL:` line, the runtime's `fatal error:`, a signal, or an exit code the testing
   package never uses — is a `RUN_ERROR` (issue #27): no test failed, so the exit says
   nothing about the mutant. It counts towards no score, `-previous` never copies it forward,
   and `minimize` ignores it. `-previous` copies forward KILLED / LIVED / TIMEOUT only;
   NOT_VIABLE and NO_COVERAGE are recomputed, both being free.
3. **`criteria`.** `Criterion` (`Name`, `Rows`: test → labels) with `SiteCoverage` and
   `Mutation`; `Compose` unions weighted criteria into a `Matrix` of `Requirement{Label,
   Weight}` columns and `Test{Name, DurationMS, Covers bitset}` rows, sorted so the output is
   deterministic. The JSON form (indices per test) is the export for an exact solver.
4. **`minimize`.** `Greedy`: protected tests first, then repeatedly the test with the best
   gain = Σ weight of newly satisfied requirements / max(duration ms, 1), ties broken by name,
   until no test gains. Every other test is redundant and comes with the selected tests that
   each subsume it. Protection: a name regexp (default `^TestRegression_`) and `Tagged`, which
   parses `_test.go` files for a `//mutrim:keep` doc-comment line (a directive-shaped line,
   so it is read from the raw comments, not `CommentGroup.Text`).
5. **CLI and Bazel.** `mutrim minimize -mutants mutants.json -srcs dir [-keep re] [-tag t]
   [-w-site 1] [-w-kill 5] [-matrix out.json] report.json...` prints `selected`, `redundant`
   and `weak_spots` (functions with LIVED or NO_COVERAGE mutants, from `runner.WeakSpots`).
   `bazel/run.sh` runs it after `mutrim run`, with the macro's `srcs` as data, so every
   `mutation_test` leaves `report.json` and `minimize.json` in its undeclared outputs.

6. **Subtest rows** (`run -subtests`, `mutation_test(subtests = True)`). Go suites are
   mostly table-driven, so the unit a person deletes is `TestParse/empty_input`, not
   `TestParse`. The baseline's `-test.v` output enumerates the subtests (there is no
   `-test.list` for them); each is traced with `-test.run '^TestX$/^case$'`, and per mutant
   the reaching subtests of one parent run in one process, attributed by their `--- FAIL:`
   lines. Rows carry `parent`; `-keep` matches the full name and a tag on the parent protects
   every row. Known hazard, as in Stryker's per-test mode: subtests must be order-independent.

7. **Confirmation reruns** (`run -confirm-kills`, `run -confirm-baseline`, the matching
   `mutation_test` attributes). A kill read from one run is unstable (Shi, Bell, Marinov,
   ISSTA 2019: 9% of mutant × test pairs), and a per-test matrix turns one flaky kill into
   an "essential" or a "redundant" test. `-confirm-kills N` reruns only the killing tests
   until each has failed N runs; an unreproduced failure goes to `suspicious_by`, and a
   mutant whose every killer turned suspicious is `LIVED`. `-confirm-baseline N` repeats
   each trace run; a test that fails in some runs and passes in others is `flaky` in
   `tests[]` instead of an error, its failures are never kills, and `minimize` drops it
   from the matrix and lists it in `flaky_tests`. `totals.suspicious` sizes the problem.

Done: the schemata fixture has a redundant test, a `TestRegression_*` test and a tagged test;
the runner golden shows complete `killed_by` lists and `NO_COVERAGE` for the untested
function, and the CLI test checks the minimize verdict end to end. `criteria` and `minimize`
run `mutation_test` on themselves under Bazel, next to `examples/`.

## Testing approach

Golden files under `mutator/testdata/` list every mutant with its position, operator,
viability and the source line after the rewrite, and the test re-generates after all
apply/undo round trips to prove the AST is restored. The per-mutant expected-output idea
follows go-mutesting's test layout; the fixtures and expectations here are written from
scratch, and nothing is copied from gremlins (Apache-2.0) or go-mutesting (MIT).

## Risks and open questions

- **Generic helpers vs. untyped constants.** `mut.Cmp(id, 1, x, …)` infers `T` from the
  typed operand and converts the constant exactly as the original expression did. Only an
  untyped _non-constant_ operand (a bare shift) with no typed sibling is declined.
- **`//go:generate` output detection** is heuristic (`Code generated ... DO NOT EDIT`
  header); document it rather than trying to be clever.
- **Timeouts** need a per-package baseline run; the runner measures it once before mutating.

## Immediate next steps

1. Tag `v0.2.0` once the Bazel workflow is green on `main`.
2. A block-coverage `Criterion` for code without mutant sites, once a build-agnostic source of
   per-test cover profiles exists (rules_go instrumented builds outside `bazel coverage`).
3. Merge shard reports inside the Bazel test tree (a `mutrim_minimize` rule over the shards'
   outputs) instead of asking the user to pass them together.
