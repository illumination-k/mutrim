---
title: Schemata runs
description: "Build once with every mutant embedded, run them all, and minimize the suite."
---

```bash
# Write sources with every mutant embedded and every block traced, plus
# overlay.json, mutants.json and blocks.json.
go run ./cmd/mutrim gen -schemata out -blocks blocks.json -o mutants.json ./path/to/pkg

# Build the test binary once from the schemata sources.
go test -c -overlay out/overlay.json -o pkg.test ./path/to/pkg

# Re-execute it once per mutant and write report.json.
go run ./cmd/mutrim run -test-bin pkg.test -mutants mutants.json -dir ./path/to/pkg -out report.json

# Report redundant tests, functions whose mutants survive and functions no test
# runs. Never deletes anything.
go run ./cmd/mutrim minimize -mutants mutants.json -blocks blocks.json -srcs ./path/to/pkg report.json

# Render the run for another tool: Stryker JSON, its HTML viewer, CI annotations.
go run ./cmd/mutrim report -mutants mutants.json -srcs ./path/to/pkg report.json
```

These steps are what [`mutrim test`](../test/) runs per package. Run by hand, the schemata
sources import `github.com/illumination-k/mutrim/mut`, so the target module needs mutrim
as a dependency; `mutrim test` injects the runtime through the overlay instead. `run` honors `TEST_SHARD_INDEX` / `TEST_TOTAL_SHARDS` and
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
duration, reached sites and blocks) and, per mutant, every test that killed it. A mutant no
test reaches is `NO_COVERAGE` and never executed.

Site coverage is blind to code without a mutant site: a function of straight-line calls and
assignments has none, so a test that only exercises it would satisfy nothing and be called
redundant. The schemata sources therefore also call `mut.Reach(id)` at the head of every
block (function and function-literal bodies, `if` / `else` / `for` / `range` bodies, `case`
and `select` clauses). It is a trace-only site, never a mutant: untraced it does nothing, and
traced it records the block like a site, so `tests[].blocks` is exact per-test block coverage
under `go test` and Bazel alike, with no `-cover` involved. `gen -blocks` lists the blocks
(`blocks.json`: ID, function, position). `minimize` adds each reached block as a requirement
(`-w-block`, default 1, next to `-w-site` 1 and `-w-kill` 5), and with `-blocks blocks.json`
lists under `uncovered` the functions with a block no test reaches (`blocks`, `unreached`, the
first unreached `line`), next to `weak_spots`: a function with no mutant is never a weak spot,
but it can be uncovered.

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
