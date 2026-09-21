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
criteria/            Criterion interface, BlockCoverage, Mutation (Phase 4)
minimize/            matrix + greedy set cover (Phase 4)
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
   Families: relational, arithmetic, logical (one table-driven `BinaryOp`), condition
   negation, increment, return replacement. Only function bodies are walked.
3. **Mutant ID.** `sha256(pkgPath, enclosing func name, AST path from func root, operator
   name, description)` truncated to 16 hex chars. Position-independent by construction; a test
   asserts that inserting lines above a site does not change its ID.
4. **Type-check pre-filter.** For each binary-operator site: `Apply`, run `types.CheckExpr`
   on the rewritten expression in its original scope, record `viable=false` on error, `Undo`.
   Statement mutants are well-typed by construction and have no check. Context-dependent
   failures (constant overflow, unused import after a return replacement) are left to the
   build, which the runner counts as NOT VIABLE.
5. **`mutants.json`.** One entry per candidate:
   `{id, pkg, file, line, col, func, operator, description, viable}`. Non-viable entries are
   kept in the file (so the runner can report NOT_VIABLE counts) but never executed.
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

   Sites the lowering declines stay as they are and their mutants are reported `NOT_VIABLE`
   under schemata: constant expressions (a call is not a constant), boolean results or
   operands of a defined type (the helpers return plain `bool`), untyped non-constant
   operands, `&&`/`||` whose right operand calls `recover()`, and `m[k]++` in a `for` post
   statement. `mutrim gen -schemata` flips `viable` to false for them so the runner never
   selects an ID the binary does not contain.

   The `mut` runtime reads `GOMUTANT_ID` once at init. With the variable unset every helper
   is the identity; `TestSchemataIdentity` runs the fixture suite against the schemata source
   and pins the generated file with a golden.
2. **Runner** (`mutrim run -test-bin <path> -mutants mutants.json`). For each viable mutant
   whose `id % TEST_TOTAL_SHARDS == TEST_SHARD_INDEX`: exec the binary with `GOMUTANT_ID=id`,
   `-test.v -test.failfast` and a timeout (default 3× the baseline run, at least 2s), classify
   KILLED / LIVED / TIMEOUT. `-tests` is an allowlist for `-test.run`; without it the whole
   binary runs (Phase 4 narrows by per-test coverage).
3. **`report.json`** written to `TEST_UNDECLARED_OUTPUTS_DIR` (or `-out`):
   `{mutant_id, status, tests_run, killed_by, duration_ms}` plus totals and the baseline /
   timeout used. TIMEOUT counts as KILLED in the score.
4. **Incremental re-runs.** `-previous report.json`: mutants whose ID is present are copied
   forward; new IDs are executed; `NOT_VIABLE` is always recomputed from `mutants.json`. IDs
   are content hashes, so an untouched function keeps its result.

Done: `runner/testdata/schemata.golden` is the report of the schemata fixture, produced by
`gen -schemata` + `go test -c -overlay` + `run`, and every embedded mutant of a tested
function is KILLED there; the CLI test runs the same pipeline through `mutrim`.

## Phase 3 — Bazel integration (first release)

Goal: `bazel_dep(name = "mutrim")` + `mutation_test(name, deps=..., shard_count=N)`.

- `MODULE.bazel` with rules_go + gazelle; BUILD files generated only by gazelle.
- `defs.bzl`: `mutation_test` macro expands to a rule that runs `mutrim gen`
  (schemata sources + `mutants.json`) → `go_test` on the generated sources → a `sh_test` /
  `go_test` runner target `mutant_<pkg>` with `shard_count`, writing `report.json` to
  undeclared outputs.
- `examples/` fixtures dogfood the macro; `bazel test //...` is the integration test.
- New mise tasks `lint:bazel` (`gazelle --mode=diff`, buildifier) and `test:bazel`; a separate
  `ci_bazel.yml` workflow on `ubuntu-latest`.

Done when: `bazel test //...:mutant_*` passes in CI and the report is visible as an
undeclared test output. Tag `v0.1.0`.

## Phase 4 — Criteria and minimizer (second release)

- `criteria.Criterion` interface; `BlockCoverage` builds test → bitset from per-test cover
  profiles (`-test.run '^Name$' -test.coverprofile`); `Mutation` builds it from `report.json`.
  The runner then uses `BlockCoverage` for `-test.run` narrowing.
- `minimize`: compose the matrix, weighted greedy set cover with
  gain = (w_cov × new blocks + w_mut × new kills) / test time, default weights 1 : 5;
  subsumption detection; protection rules (`TestRegression_*`, comment tag); JSON matrix
  export for an external MIP solver.
- `mutrim minimize` CLI printing redundant tests and functions no test kills. Never deletes.

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

1. Phase 3: `MODULE.bazel`, `defs.bzl` with `mutation_test`, and an `examples/` fixture that
   runs `mutrim gen -schemata` → `go_test` → `mutrim run` under `shard_count`.
