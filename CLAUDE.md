# AGENTS Guideline

This repository is pre-alpha and under active development. The API is not stable and may change without a major version bump, so backwards compatibility is not guaranteed at this stage.
So developers of this repository DO NOT need to worry about breaking changes or maintaining backwards compatibility. We prefer to iterate quickly and make breaking changes as needed, rather than trying to maintain backwards compatibility.

## Policy

Follow the YANGI, SOLID, DRY, and KISS principles in all code and documentation. Prioritize simplicity, readability, and maintainability over cleverness or optimization. Avoid premature optimization and over-engineering. Strive for clear and concise code that is easy to understand and modify.

## Development Process

Run `mise install` first to install the toolchain and project tools.

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

| Tool          | Purpose                             |
| ------------- | ----------------------------------- |
| uv            | Python package manager              |
| dprint        | Code formatter                      |
| prek          | Pre-commit hook runner              |
| shfmt         | Shell script formatter              |
| actionlint    | GitHub Actions linter               |
| zizmor        | GitHub Actions security linter      |
| shellcheck    | Shell script linter                 |
| ghalint       | GitHub Actions linter               |
| pinact        | Pin GitHub Actions versions to SHAs |
| go            | Go toolchain                        |
| golangci-lint | Go linter suite and formatter       |
| govulncheck   | Go vulnerability scanner            |

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

| Package    | Responsibility                                                                                         | Depends on                                |
| ---------- | ------------------------------------------------------------------------------------------------------ | ----------------------------------------- |
| `mutator`  | AST rewriting (`go/ast` + `go/format`), `go/types` pre-check, `mutants.json` output. Bazel-independent | `go/ast`, `go/types`, `go/packages`       |
| `criteria` | `Criterion` interface with `BlockCoverage` / `Mutation` implementations. Output: test name → bitset    | `x/tools/cover`, `bits-and-blooms/bitset` |
| `minimize` | Matrix composition, weighted greedy set cover, subsumption detection, protection rules                 | `bitset` only                             |

Bazel-specific logic is confined to Starlark (`defs.bzl`, `mutation_test` macro) and a thin
CLI. `mutator` must keep working without Bazel via `go test -overlay` so the fast dev loop and
non-Bazel users are both served.

### Mutation engine

- Operators: relational (`<` ↔ `<=`, `==` ↔ `!=`), arithmetic, logical (`&&` ↔ `||`),
  condition negation, increment (`i++` ↔ `i--`), return replacement (`return x` → zero value).
- **Type-check pre-filter is the key differentiator.** After each AST rewrite, re-run
  `types.Config.Check` on the package and drop mutants that fail. Load once with
  `go/packages` (`NeedTypes|NeedDeps`); re-check should be tens of ms per package.
- Unused var/import are not `go/types` errors; count build failures as NOT VIABLE.
- Equivalent mutants are not detected; surviving mutants are simply dropped from requirements.
- Exclude: `_test.go`, `.pb.go`, `mock_*.go`, `//go:generate` outputs, cgo.

### Bazel integration: mutant schemata

All mutants of a package are embedded into one source (`if mut.Lt(7, a, b) {`) and selected
at runtime via `GOMUTANT_ID`. One build per package; target count = packages × shards.
Do **not** expand one `go_test` per mutant — tens of thousands of targets break Bazel
loading/analysis.

- Runner wraps `go_binary`, re-execs the test binary per mutant with `GOMUTANT_ID=k`,
  runs only tests that cover that mutant (`-test.run`), and writes `report.json`
  (KILLED / LIVED / TIMEOUT) as undeclared test outputs. TIMEOUT counts as KILLED.
- Sharding via `shard_count` + `TEST_SHARD_INDEX` / `TEST_TOTAL_SHARDS` (`id % TOTAL == INDEX`).
- Mutant IDs are content hashes (function name + AST path + operator), not positions, so the
  runner can do incremental re-runs from the previous `report.json`. Weekly full run corrects
  drift.
- With `GOMUTANT_ID` unset, schemata source must behave identically to the original; keep a
  test that asserts this.

### Minimizer

Gain = (w_cov × new blocks + w_mut × new kills) / test time, default weights 1 : 5.
Two-stage verdict: coverage-only subsumption yields a "candidate"; mutation restricted to the
candidate's covered code confirms it. The tool never deletes tests; it reports subsumption and
weak spots and leaves the decision to a human or LLM. Tests matching `TestRegression_*` or a
comment tag are always kept. Exact solutions go through an exported JSON matrix + external MIP.

### Build: go.mod is primary, Bazel is secondary

- `go.mod` is the source of truth. Day-to-day development uses the mise tasks
  (`go test ./...`, golangci-lint, govulncheck). `mutator` / `criteria` / `minimize` must build
  and test without Bazel.
- Bazel (`MODULE.bazel` with rules_go + gazelle) is added from the Bazel-integration phase for
  two purposes: distribution as a Bazel module, and dogfooding. `examples/` holds fixture
  packages that call `mutation_test(...)` on themselves, so `bazel test //...` is the
  integration test for `defs.bzl` and the shard runner.
- BUILD files are generated by gazelle; do not hand-edit them. `lint:bazel` runs
  `gazelle --mode=diff` and buildifier, `test:bazel` runs `bazel test //...`.
- Bazel CI runs in its own workflow on `ubuntu-latest` (compute-bound); the Go lint/test
  workflow stays on the template defaults.

### Conventions

- Generated artifacts (`mutants.json`, `report.json`, coverage matrices) are never committed.
- Output on stdout is JSON only; logs go to stderr.
- Reference implementations: gremlins `internal/engine` / `internal/mutator`, cargo-mutants
  `--shard`, PIT (schemata + incremental analysis).
