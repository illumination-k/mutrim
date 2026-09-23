---
title: Reporting
description: "Export a run as Stryker JSON, an HTML viewer or GitHub annotations."
---

`report` renders `report.json` in the formats other tools already read. `-mutants` is
required (it carries the source locations), the shards of a run are passed together, and
`-srcs` names the files or directories holding the mutated sources.

```bash
# Stryker mutation-testing-report-schema v2, which the Stryker dashboard accepts.
mutrim report -mutants mutants.json -srcs ./pkg report.json > mutation-report.json

# The same JSON inside the single-file mutation-test-report-app viewer.
mutrim report -format html -mutants mutants.json -srcs ./pkg -o mutation-report.html report.json

# One ::warning per surviving mutant, which GitHub Actions shows on the diff;
# -max-per-line 1 surfaces one mutant per line, as Google's code review does.
mutrim report -format github -max-per-line 1 -mutants mutants.json report.json
```

`KILLED`, `LIVED`, `TIMEOUT` and `NO_COVERAGE` map onto the schema's `Killed`, `Survived`,
`Timeout` and `NoCoverage`; `RUN_ERROR` is a `RuntimeError`, which the schema keeps out of the
score; `NOT_VIABLE` is a `CompileError`, since the mutant never built,
and `IGNORED`, `SKIPPED`, `EQUIVALENT` and `SUSPECT_EQUIVALENT` are `Ignored`, with the
directive or filter that suppressed it, `not in the diff`, or the equivalence, as its
`statusReason` (a suspect is `Survived` under `-count-suspect`). Each mutant carries the tests that reach it (`coveredBy`) and the ones that
killed it (`killedBy`), so the viewer shows the kill matrix per mutant. A mutant no report
mentions is left out, so one shard's report renders that shard. A mutant's `diff` from
`mutants.json` follows its description in the Stryker report and its message in a GitHub
annotation.

`-format html` and `-format github` write their own text rather than JSON: GitHub reads its
annotations from the step's stdout, and the HTML view loads the viewer from unpkg, so it
needs a network at display time but is a single file to publish.
