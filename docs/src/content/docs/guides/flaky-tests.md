---
title: Flaky tests, timeouts and minimization
description: "Confirm kills with reruns, tune timeouts, and read the minimizer's output."
---

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
tests kept with their gain, the essential ones first: a test that satisfies a requirement no
other test in the whole suite does is `essential` (Harrold–Gupta–Soffa; Chen & Lau) and comes
with the labels of those requirements (`unique`), so the list reads top-down as must keep →
keep for now. `redundant` the rest with the selected tests that subsume each of them and how
many other tests share its requirements (`shared_with`; one is a single deletion away from
essential), and `weak_spots` (with `-mutants`) the functions whose mutants survive. Tests
matching `-keep` (default `^TestRegression_`) or tagged `//mutrim:keep` in their doc comment
(`-srcs` names the `_test.go` files or directories to scan) are always kept; `-keep` sees the
full `TestX/case` name of a subtest row, and a tag on the parent keeps every one of its
subtests; with qualified rows both match the name within its package. `-matrix` exports
the composed test × requirement matrix as JSON for an exact solver. The shards of one package
can be passed together, and so can the reports of several packages: each bare test name is
then qualified with its report's package, so a test is one row wherever it appears — the
tests of `app` that ran against `pkg`'s mutants with `-extra-test` and against `app`'s own
mutants carry the sites and kills of both.
