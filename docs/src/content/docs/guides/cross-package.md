---
title: Cross-package kills
description: "Count kills from the tests of packages importing the mutated one."
---

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
