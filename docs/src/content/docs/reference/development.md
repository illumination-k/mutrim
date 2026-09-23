---
title: Development
description: "Toolchain, CI tasks, benchmarks and dogfooding."
---

```bash
mise install
mise run ci    # fmt/lint/test; see [CLAUDE.md](https://github.com/illumination-k/mutrim/blob/main/CLAUDE.md)
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
