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
cmd/mutrim/          CLI: `gen`, `run`, `overlay`, later `minimize` (thin; JSON on stdout)
mutator/             AST rewriting, type-check pre-filter, mutant IDs, mutants.json
mutator/operator/    one file per operator family
mutator/schemata/    schemata lowering + `mut` runtime package used by generated code
runner/              re-exec test binary per mutant, sharding, report.json, incremental
criteria/            Criterion interface, BlockCoverage, Mutation (Phase 4)
minimize/            matrix + greedy set cover (Phase 4)
internal/testdata/   fixture packages for mutator/runner unit tests
examples/            Bazel fixtures calling mutation_test on themselves (Phase 3)
defs.bzl, MODULE.bazel
```

The placeholder `mutrim.go` / `mutrim_test.go` at the root are removed in Phase 0.

## Phase 0 — Foundation

Goal: real module layout, dependencies pinned, fixtures in place.

- Add `golang.org/x/tools` (`go/packages`, `cover`) and `bits-and-blooms/bitset` to `go.mod`.
- Create the packages above with a doc.go each; delete root placeholders.
- Add `internal/testdata/` fixtures: one package per operator family with hand-written
  golden expectations, plus one "excluded files" package (`_test.go`, `.pb.go`, `mock_*.go`,
  cgo, `//go:generate` output) to drive the exclusion filter.
- Wire `cmd/mutrim` with cobra-free `flag` subcommands; JSON to stdout, logs to stderr.

Done when: `mise run ci` passes with the new layout and an empty CLI that prints usage.

## Phase 1 — Mutator (Bazel-independent)

Goal: `mutrim gen ./pkg` writes a `mutants.json` listing only type-checked-viable mutants,
and `mutrim overlay` lets `go test -overlay` run a single mutant.

1. **Loading.** `go/packages` once per package with
   `NeedName|NeedFiles|NeedSyntax|NeedTypes|NeedTypesInfo|NeedDeps|NeedImports`.
   Exclusion filter applied to file names and build constraints before any rewriting.
2. **Operator interface.**
   ```go
   type Operator interface {
       Name() string                       // stable, used in mutant IDs
       Match(ast.Node) []Candidate         // sites this operator can mutate
   }
   type Candidate interface {
       Apply() (undo func())               // in-place AST rewrite
       Describe() string                   // "< -> <=" for mutants.json
   }
   ```
   Families in order of value / simplicity: relational, condition negation, logical,
   arithmetic, increment, return replacement. Each family has its own file and golden test.
3. **Mutant ID.** `sha256(pkgPath, enclosing func name, AST path from func root, operator
   name, description)` truncated to 16 hex chars. Position-independent by construction; a test
   asserts that inserting a blank line above a site does not change its ID.
4. **Type-check pre-filter.** For each candidate: `Apply`, run `types.Config.Check` over the
   package's syntax with the original importer, record `viable=false` on error, `undo`.
   Measured per package in a benchmark; target is tens of ms.
5. **`mutants.json`.** One entry per candidate:
   `{id, pkg, file, line, col, func, operator, description, viable}`. Non-viable entries are
   kept in the file (so the runner can report NOT_VIABLE counts) but never executed.
6. **Overlay mode.** `mutrim overlay --id <id>` prints a `go build -overlay` JSON pointing at
   a temp file with that single mutant applied (`go/format`). This is the fast dev loop and
   the non-Bazel user path.

Done when: golden tests cover every operator, the ID stability test passes, and running
`go test -overlay` with a known-killable mutant on a fixture fails the fixture's test.

## Phase 2 — Schemata and runner

Goal: one build per package; the test binary re-executed per mutant.

1. **Schemata lowering** in `mutator/schemata`. Every viable mutant of a package is embedded
   into rewritten sources that import the `mut` runtime package:

   | Original   | Lowered                                                     |
   | ---------- | ----------------------------------------------------------- |
   | `a < b`    | `mut.Cmp(site, a, b)` with a generated per-site op table    |
   | `a && b`   | closure that keeps short-circuit evaluation, switches on id |
   | `!c` / `c` | `mut.Not(site, c)`                                          |
   | `a + b`    | `mut.Arith(site, a, b)` (generic on numeric constraint)     |
   | `i++`      | `if mut.Active(id) { i-- } else { i++ }`                    |
   | `return x` | `if mut.Active(id) { return <zero> }; return x`             |

   The `mut` runtime reads `GOMUTANT_ID` once at init. With the variable unset every helper
   is the identity; an equivalence test runs the fixture suite against the schemata source and
   the original and asserts identical results.
2. **Runner** (`mutrim run --test-bin <path> --mutants mutants.json`). For each viable mutant
   whose `id % TEST_TOTAL_SHARDS == TEST_SHARD_INDEX`: exec the binary with `GOMUTANT_ID=id`
   and a timeout (default 3× baseline run), classify KILLED / LIVED / TIMEOUT. `-test.run`
   narrowing uses per-test coverage when available (Phase 4 provides it properly; until then
   the runner has a `--tests` allowlist flag and otherwise runs the whole binary).
3. **`report.json`** written to `TEST_UNDECLARED_OUTPUTS_DIR` (or `--out`):
   `{mutant_id, status, tests_run, duration_ms}` plus totals. TIMEOUT counts as KILLED in
   totals.
4. **Incremental re-runs.** `--previous report.json`: mutants whose ID is present with a
   terminal status are skipped and copied forward; new IDs are executed. IDs are content
   hashes, so an untouched function keeps its result.

Done when: on a fixture package, `mutrim gen` + build + `mutrim run` produces a report whose
KILLED set matches the golden expectations, and the schemata identity test passes.

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

## Risks and open questions

- **Type-check cost under schemata.** Each mutant is still checked individually on the
  original AST before being embedded; if that dominates, batch candidates per function.
- **Generic helpers vs. untyped constants.** `mut.Cmp(site, 1, x)` must not change constant
  typing; the lowering keeps the original expression types by wrapping operands in
  conversions when `go/types` reports an untyped constant.
- **`//go:generate` output detection** is heuristic (`Code generated ... DO NOT EDIT`
  header); document it rather than trying to be clever.
- **Timeouts** need a per-package baseline run; the runner measures it once before mutating.

## Immediate next steps (first PR after this plan)

1. Phase 0 in full.
2. Phase 1 steps 1–3 with the relational operator only, including the ID stability test.
3. Type-check pre-filter and `mutants.json` output.
