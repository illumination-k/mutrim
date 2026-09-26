---
title: External mutants
description: "Feed in mutants proposed by an LLM or mined from bug fixes, and hand the survivors out."
---

The rule-based operators stay the default. External mutants are opt-in: mutrim reads
them, type-checks them and runs them like its own, and exports what survives, but never
calls a model itself. Both directions are plain JSON, so any loop (an LLM, a script
mining a project's bug-fix commits) can sit on the other side.

LLM mutants find more real faults than rule-based ones (Wang et al., TOSEM 2026: 88% vs
42% fault detection on 851 bugs) but compile less often and duplicate more; operators
mined from a project's own fixes catch faults classic ones cannot (Brown et al., FSE
2017). mutrim's type check and content-hash IDs are what make such proposals usable.

## Input: `gen -extra`

```json
[
  {
    "file": "calc.go",
    "func": "Clamp",
    "line": 11,
    "original": "hi",
    "replacement": "hi - 1",
    "description": "off-by-one upper bound"
  },
  {
    "file": "calc.go",
    "func": "Words",
    "original": "n++",
    "replacement": "n += 2"
  }
]
```

```sh
mutrim gen -extra extra.json -schemata out -o mutants.json ./calc
mutrim test -extra extra.json ./...   # the one-shot driver takes it too
```

- The site is found by its source text: `original` is compared with every expression or
  statement of `file` (a path or a trailing part of it) regardless of spacing. `func`,
  `line` and `col` narrow the search; an `original` matching several sites is an error
  that asks for them.
- When `original` and `replacement` both parse as expressions, the expression is
  replaced; otherwise `original` is one statement and `replacement` zero or more
  (empty removes it).
- Each entry becomes a mutant of operator `extra`, with its own content-hash ID. It is
  type-checked by re-checking the whole package with the rewrite in place, so an unused
  variable or a mismatched type is caught; a failing entry is `viable: false` with the
  error in `reason`. Entries that name no single site are reported on stderr and skipped,
  never fatal.
- An entry whose rewrite a generated mutant (or an earlier entry) already makes is
  ignored as `duplicate`, with `reason` naming that mutant; one that changes nothing is
  `equivalent: "identical"`.
- Inline directives (`//mutrim:disable extra`) and the gen filters apply; the arid rules
  do not, since a proposal is deliberate.
- Schemata embed an expression as `func() T { if mut.Active(id) { return repl }; return orig }()`
  and a statement as `if mut.Active(id) { repl } else { orig }`, so only one side runs.
  An expression that must be addressable or constant, whose type the file cannot name,
  or a statement that declares or carries a label is not viable.

## Output: `export-survivors`

```sh
mutrim export-survivors -mutants mutants.json -srcs ./calc -o survivors.json report.json
```

Each LIVED, NO_COVERAGE and SUSPECT_EQUIVALENT mutant comes with its unified diff, the
source of its function, and the tests that reach it (from the trace) with their source:
what a loop needs to write a killing test (Foster et al., FSE 2025: 73% of such generated
tests were accepted at Meta) or to judge the mutant equivalent (Tian et al., ISSTA 2024).
`-srcs` names the files or directories holding the sources and their `_test.go` files.
