---
title: Quick start
description: "Mutation-test a whole module with one command, without adding mutrim to it."
---

```bash
go install github.com/illumination-k/mutrim/cmd/mutrim@latest

# In the module to test: every package with tests, results under .mutrim/.
mutrim test ./...

# Also minimize the suite and render the reports.
mutrim test -minimize -report html,github ./...

# In CI: only the mutants a pull request touches, failing below a score.
mutrim test -in-diff origin/main -threshold 0.8 ./...
```

`mutrim test` composes the [schemata](../schemata/) steps per package: it lists the
packages through `go list`, generates their mutants (`gen -schemata`), builds each test
binary once (`go test -c -overlay`) and runs it once per mutant (`run`). The module's
`go.mod` never changes: the schemata sources import the `mut` runtime from
`<module>/internal/mutrimrt`, a package that exists only in the build overlay, where
`mutrim test` writes its own copy of the runtime. A package without `_test.go` files is
skipped.

It writes, per package, `.mutrim/<import path>/` with `mutants.json`, `blocks.json`,
`overlay.json`, the schemata sources, the test binary and `report.json`, and prints the
combined totals on stdout:

```json
{
  "packages": [{ "pkg": "example.com/m/a", "report": ".../report.json", "totals": { … } }],
  "totals": { "mutants": 51, "killed": 36, "score": 0.87, … }
}
```

The next run reads `.mutrim/<import path>/report.json` as its `run -previous`, so a result
is copied forward while the tests it was observed with are unchanged (see `-test-srcs` in
[Schemata runs](../schemata/); `mutrim test` passes the package's `_test.go` files).
Add `.mutrim/` to `.gitignore`.

| Flag                 | Meaning                                                                                                            |
| -------------------- | ------------------------------------------------------------------------------------------------------------------ |
| `-out`               | output directory (default `.mutrim`)                                                                               |
| `-previous`          | directory of the earlier run whose results are copied forward (default `.mutrim`); `-previous=` runs all           |
| `-operators`         | operators, as `gen -operators`                                                                                     |
| `-in-diff <ref>`     | scope the run to `git diff --merge-base <ref>`, as `run -in-diff` with the commit-relevant mutants                 |
| `-threshold`         | fail, after writing every output, when the combined `score` is below it                                            |
| `-threshold-covered` | the same for the combined `covered_score`                                                                          |
| `-skip-failing`      | run without the tests that fail on their own instead of stopping, as `run -skip-failing`                           |
| `-p`                 | packages built and run at once (default GOMAXPROCS)                                                                |
| `-jobs`              | test processes per package (default GOMAXPROCS / `-p`)                                                             |
| `-minimize`          | write `minimize.json` over every package (`minimize -blocks`)                                                      |
| `-report`            | comma-separated: `stryker` (`mutation-report.json`), `html` (`mutation-report.html`), `github` (`annotations.txt`) |

`-minimize` and `-report` write the combined `mutants.json` and `blocks.json` to `-out`
and run `minimize` and `report` over every package's report. GitHub reads annotations from
a step's stdout, which `mutrim test` keeps for its JSON, so `cat .mutrim/annotations.txt`
in a later step. For anything the driver does not expose (subtests, sampling, extra test
binaries, confirmation reruns), run the low-level commands.
