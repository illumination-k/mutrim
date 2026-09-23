---
title: Scoping a run to a diff
description: "Run only the mutants a pull request is relevant to."
---

`run -in-diff` runs only the mutants a unified diff is relevant to, which is what a
pull request needs:

```bash
git diff --unified=0 --merge-base origin/main > pr.diff
go run ./cmd/mutrim run -test-bin pkg.test -mutants mutants.json -in-diff pr.diff -out report.json
```

The scope is the commit-relevant mutants, found through the per-test trace: the mutants
on the lines the diff adds (`changed: true` in `report.json`), the tests reaching any of
them (they exercise the change), and every mutant those tests reach, in whichever file of
the package. Most commit-relevant mutants lie outside the changed lines (Ojdanić et al.,
TOSEM 2023), so the lines alone miss them; `-diff-expand=false` keeps the lines alone.

Every other mutant is reported `SKIPPED`: it is not executed and counts towards no score,

Only the new side of the diff is read. A removed line holds no mutant, and a context line
is code the diff did not change, so a mutant is kept when its span overlaps an added line;
`_test.go` files are skipped, since mutants never live there. The diff's paths are matched
against a mutant's file by path suffix, so a repository-relative diff matches both the
absolute paths of a `go list` run and the execroot-relative ones of a Bazel run.

`gen` is unchanged: mutant IDs are content hashes, so `mutants.json` is the same either
way and a scoped run stays comparable with a full one. The diff must be taken against the
sources the test binary was built from; mutrim does not verify that, and a diff of another
tree simply selects the wrong lines.

Under Bazel the diff is passed by path, which keeps the runner hermetic:

```bash
git diff --unified=0 --merge-base origin/main > "$PWD/pr.diff"
bazel test --test_env=MUTRIM_IN_DIFF="$PWD/pr.diff" //...:all
# the changed lines alone
bazel test --test_env=MUTRIM_IN_DIFF="$PWD/pr.diff" --test_arg=-diff-expand=false //...:all
```
