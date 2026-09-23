---
title: Thresholds
description: "Fail a run whose mutation score is too low."
---

By default `run` passes whatever the score. `run -threshold 0.8` fails the run when `score` is
below 0.8, and `-threshold-covered 0.9` when `covered_score` is below 0.9 (PIT
`mutationThreshold` / `testStrengthThreshold`, Stryker `thresholds.break`); either fails only
after `report.json` is written, and a run with nothing to score passes. The `threshold` and
`threshold_covered` attributes of `mutation_test` pass them through, turning the target into a
gate, and the other outputs are still written. Under sharding each shard checks its own
mutants; the whole-package score needs the shards' reports merged.
