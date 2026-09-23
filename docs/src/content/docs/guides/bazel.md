---
title: Bazel
description: "The mutation_test macro."
---

```python
# MODULE.bazel
bazel_dep(name = "mutrim", version = "0.1.0")
local_path_override(module_name = "mutrim", path = "../mutrim")  # until it is published
```

```python
# BUILD.bazel: mirror the package's go_test
load("@mutrim//:defs.bzl", "mutation_test")

mutation_test(
    name = "mutant_calc",
    srcs = ["calc_test.go"],
    embed = [":calc"],
    embedsrcs = ["testdata/golden.txt"],   # optional; files the tests' //go:embed name
    operators = ["default", "-constant"],  # optional; see `mutrim gen -operators`
    match = "^Compute",                    # optional; the gen filters, per attribute
    exclude_files = ["_gen\\.go$"],
    arid = ["(*Metrics).Observe"],         # optional; extends the built-in arid rules
    subtests = True,                       # optional; `mutrim run -subtests`
    confirm_kills = 3,                     # optional; `mutrim run -confirm-kills`
    confirm_baseline = 3,                  # optional; `mutrim run -confirm-baseline`
    extra_tests = ["//app:app_test"],      # optional; `mutrim run -extra-test`
    threshold = 0.8,                       # optional; `mutrim run -threshold`
    jobs = 4,                              # optional; `mutrim run -jobs`, pair with tags
    min_timeout = "2s",                    # optional; `mutrim run -min-timeout`
    tags = ["cpu:4"],
    shard_count = 4,
)
```

`bazel test //pkg:mutant_calc` builds the package once with every mutant embedded, runs the
tests against it (`mutant_calc_schemata_test`, which must pass like the original tests), then
re-executes that binary once per mutant against the tests reaching it and writes
`report.json`, `minimize.json` and the Stryker `mutation-report.json` / `mutation-report.html`
to the test's undeclared outputs (`bazel-testlogs/pkg/mutant_calc/test.outputs/`, one
directory per shard; pass the shards' reports together to `mutrim minimize` or
`mutrim report` for a whole-package verdict). Mutants are generated
hermetically: the rule type-checks the library against the export data rules_go compiled for
its dependencies, so no `go` command runs inside the sandbox. `extra_tests` names the
`go_test`s of packages importing the library: each is relinked against the schemata library
(`mutant_calc_extra0`, ...), recompiling every package between the test and the library as
`go_test` itself does for external tests, and its tests run against the mutants too; the
target must be visible to the `mutation_test`'s package, and `race` cannot be set with it yet. Passing
`--test_env=MUTRIM_IN_DIFF=$PWD/pr.diff` scopes the run to a diff, as above.

`//go:embed` works on both sides: the library's `embedsrcs` come with `embed` (rules_go
resolves the patterns against the embedded files, wherever the schemata sources live), and
the tests' are passed as `embedsrcs`, as to `go_test`. Not supported yet: cgo packages.
`examples/` dogfoods the macro; run
`bazel test //...` here to see it work.
