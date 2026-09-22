# AGENTS Guideline

This repository is pre-alpha and under active development. The API is not stable and may change without a major version bump, so backwards compatibility is not guaranteed at this stage.
So developers of this repository DO NOT need to worry about breaking changes or maintaining backwards compatibility. We prefer to iterate quickly and make breaking changes as needed, rather than trying to maintain backwards compatibility.

## Policy

Follow the YANGI, SOLID, DRY, and KISS principles in all code and documentation. Prioritize simplicity, readability, and maintainability over cleverness or optimization. Avoid premature optimization and over-engineering. Strive for clear and concise code that is easy to understand and modify.

## Development Process

Run `mise install` first to install the toolchain and project tools. Task files are selected
by `MISE_ENV` (`base`, `golang`, `bazel`; `.claude/settings.json` enables all of them), so the
tasks below cover Go and Bazel alike.

At the end of a session, run `mise run ci` and make sure it passes. Use the narrower tasks while iterating:

```bash
mise run fmt      # Format
mise run lint     # Lint and policy checks
mise run test     # Tests
mise run ci       # Full required verification
```

## Commands

Run `mise install` first to install all tools.

```bash
mise run ci    # Run all ci:* tasks
mise run fmt   # Run all fmt:* tasks
mise run lint  # Run all lint:* tasks
mise run test  # Run all test:* tasks
```

## Tools

All tools are managed by mise. Run `mise install` to install them.

| Tool          | Purpose                                       |
| ------------- | --------------------------------------------- |
| uv            | Python package manager                        |
| dprint        | Code formatter                                |
| prek          | Pre-commit hook runner                        |
| shfmt         | Shell script formatter                        |
| actionlint    | GitHub Actions linter                         |
| zizmor        | GitHub Actions security linter                |
| shellcheck    | Shell script linter                           |
| ghalint       | GitHub Actions linter                         |
| pinact        | Pin GitHub Actions versions to SHAs           |
| go            | Go toolchain                                  |
| golangci-lint | Go linter suite and formatter                 |
| govulncheck   | Go vulnerability scanner                      |
| bazel         | Bazel (gazelle and buildifier run through it) |

## Purpose

mutrim (mutation + trim) is a Bazel-native mutation testing tool for Go that also uses the
results to minimize test suites. It builds a "test × requirement" bit matrix, where
requirements are the union of coverage blocks and mutants, and runs a weighted greedy set
cover over that matrix to report redundant tests and functions that no test can kill.

Two gaps in existing Go tools (gremlins, go-mutesting) motivate it: none runs natively under
Bazel, and none produces a per-test kill matrix. Coverage alone is a weak criterion for
pruning (Rothermel 2002); mutation is the real objective function, coverage is the cheap
first-pass filter.

The tool lives in this repo; it is consumed from a separate Bazel monorepo via
`bazel_dep(name = "mutrim")` + `local_path_override`. First release is mutation only
("`bazel test //...:mutant_*` works"); minimization comes after.

## Architecture

### Packages

| Package    | Responsibility                                                                                                              | Depends on                          |
| ---------- | --------------------------------------------------------------------------------------------------------------------------- | ----------------------------------- |
| `mutator`  | AST rewriting (`go/ast` + `go/format`), `go/types` pre-check, `mutants.json` output, schemata lowering. Bazel-independent   | `go/ast`, `go/types`, `go/packages` |
| `mut`      | Runtime imported by schemata sources; reads `GOMUTANT_ID` once, identity when unset; `GOMUTANT_TRACE` records reached sites | stdlib only                         |
| `runner`   | Per-test trace run, re-exec a test binary per mutant against the tests reaching it, sharding, `report.json`, incremental    | `mutator` (for `Mutant`)            |
| `criteria` | `Criterion` interface with `SiteCoverage` / `Mutation` implementations; `Compose` → weighted test × requirement `Matrix`    | `bits-and-blooms/bitset`            |
| `minimize` | Weighted greedy set cover, subsumption per redundant test, protection rules (name regexp, `//mutrim:keep` tag)              | `criteria`, `bitset`, `go/parser`   |

Bazel-specific logic is confined to Starlark (`defs.bzl`, `bazel/mutation_test.bzl`) and a
thin CLI. `mutator` must keep working without Bazel via `go test -overlay` so the fast dev
loop and non-Bazel users are both served. Under Bazel there is no `go list`: `mutrim gen
-importpath` type-checks the package's files against the export data rules_go compiled for
its dependencies (`mutator.LoadFiles`, the nogo approach), and the same `Generate`/`Lower`
run on the result.

### Mutation engine

- Operators: relational (`<` ↔ `<=`, `==` ↔ `!=`), arithmetic, logical (`&&` ↔ `||`),
  bitwise (`&` ↔ `|`, `^`/`&^` → `&`, `<<` ↔ `>>`), unary minus removal (`-x` → `x`),
  condition negation, forced conditions (`if cond` → `if true` / `if false`), increment
  (`i++` ↔ `i--`), void call removal, numeric literal replacement (`c` → `c+1`, never where
  a constant is required), return replacement (`return x` → zero value). These are the PIT
  operators with a Go counterpart; non-void call replacement is not implemented. All of them
  are on by default; `gen -operators` (the `operators` attribute of `mutation_test`) selects
  a subset, e.g. `default,-constant`.
- **Type-check pre-filter is the key differentiator.** Load once with `go/packages`
  (`NeedTypes|NeedTypesInfo|NeedSyntax`). Expression mutants (binary operators) are checked
  locally with `types.CheckExpr` in their original scope, so the cost is microseconds per
  mutant and the package is never re-checked. Statement mutants (condition negation,
  `++`/`--`, return-to-zero) are well-typed by construction and skip the check.
- The local check misses context-dependent failures: constant overflow in the enclosing
  assignment, and an import or variable left unused by a return replacement. Those fail at
  build time; count build failures as NOT VIABLE.
- Equivalent mutants are not detected; surviving mutants are simply dropped from requirements.
- Per-test coverage is site coverage, not `go tool cover` blocks: with `GOMUTANT_TRACE` set the
  `mut` runtime appends every site ID the process reaches, and the runner runs each test once
  on its own that way. `-cover` cannot be used instead: `go test -c -cover -overlay` ignores
  the overlay when instrumenting, and under Bazel coverage instrumentation only exists in
  `bazel coverage`. Site coverage is exact for narrowing (a test that reaches no site of a
  mutant cannot kill it, reported `NO_COVERAGE`) and build-agnostic; its blind spot is code
  with no mutant site at all.
- Exclude: `_test.go`, `.pb.go`, `mock_*.go`, `//go:generate` outputs, cgo.

### Bazel integration: mutant schemata

All mutants of a package are embedded into one source (`if mut.Lt(7, a, b) {`) and selected
at runtime via `GOMUTANT_ID`. One build per package; target count = packages × shards.
Do **not** expand one `go_test` per mutant — tens of thousands of targets break Bazel
loading/analysis.

- `mutation_test(name, srcs, embed, deps, shard_count)` mirrors the package's `go_test`:
  `mutrim_schemata` lowers the embedded library (one `MutrimGen` action) and provides it as a
  `GoInfo` with the same import path; a `go_test` embeds it (the identity check); an `sh_test`
  re-execs that binary per mutant with `GOMUTANT_ID=k` and writes `report.json`
  (KILLED / LIVED / TIMEOUT) to the undeclared outputs. TIMEOUT counts as KILLED. The runner
  drops Bazel's test-protocol variables from the child environment, since the rules_go test
  main would otherwise shard, filter and report a second time.
- Sharding via `shard_count` + `TEST_SHARD_INDEX` / `TEST_TOTAL_SHARDS` (`id % TOTAL == INDEX`).
- Mutant IDs are content hashes (function name + AST path + operator), not positions, so the
  runner can do incremental re-runs from the previous `report.json`. Weekly full run corrects
  drift.
- With `GOMUTANT_ID` unset, schemata source must behave identically to the original; keep a
  test that asserts this.

### Minimizer

Gain = (w_site × new sites reached + w_kill × new kills) / test time, default weights 1 : 5.
Coverage is the cheap first pass (it narrows which tests run per mutant); kills are the
objective. The tool never deletes tests; `mutrim minimize` reports each redundant test with
the selected tests that subsume it, and the functions whose mutants survive (weak spots), and
leaves the decision to a human or LLM. Tests matching `-keep` (default `^TestRegression_`) or
carrying `//mutrim:keep` in their doc comment are always kept. Exact solutions go through the
exported JSON matrix (`-matrix`) + an external MIP solver.

### Build: go.mod is primary, Bazel is secondary

- `go.mod` is the source of truth. Day-to-day development uses the mise tasks
  (`go test ./...`, golangci-lint, govulncheck). `mutator` / `criteria` / `minimize` must build
  and test without Bazel.
- Bazel (`MODULE.bazel` with rules_go + gazelle) serves two purposes: distribution as a Bazel
  module, and dogfooding. `examples/` holds fixture packages that call `mutation_test(...)` on
  themselves, so `bazel test //...` is the integration test for `defs.bzl` and the shard
  runner. `go.mod` must pin a full Go version (`go 1.27.0`): rules_go downloads that SDK.
- BUILD files are generated by gazelle; do not hand-edit them, except to add
  `mutation_test(...)` calls, which gazelle leaves alone. Tests that shell out to `go` or read
  `mutator/testdata` are excluded from Bazel in the root `BUILD.bazel`; `go test ./...` covers
  them. `lint:bazel` runs `gazelle -mode=diff` and buildifier, `test:bazel` runs
  `bazel test //...`.
- Bazel CI runs in its own workflow on `ubuntu-latest` (compute-bound); the Go lint/test
  workflow stays on the template defaults.

### Conventions

- Generated artifacts (`mutants.json`, `report.json`, coverage matrices) are never committed.
- Output on stdout is JSON only; logs go to stderr.
- Reference implementations: gremlins `internal/engine` / `internal/mutator`, cargo-mutants
  `--shard`, PIT (schemata + incremental analysis).
