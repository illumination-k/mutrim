---
title: Error-path mutants
description: "The errpath operator and per-class scores."
---

Error-handling code is the least tested (Lima et al., "Assessing exception handling testing
practices in open-source libraries", 2021). The `errpath` operator, on by default, mutates
it: `fmt.Errorf("...: %w", err)` stops wrapping (`%w` -> `%v`), and with a lone `%w` returns
`err` itself; `errors.Is` / `errors.As` are forced to `true` and `false`; a `panic(x)`
statement is removed unless the function needs it as its terminating statement; and
`recover()` returns `nil` while it still stops the panic. A `return` result replaced by `nil`
(`return x, err` -> `return x, nil`, a nil pointer, map, slice or func) and a forced nil check
(`if err != nil` -> `if true` / `if false`) are the `return` and `condition` operators'
mutants, tagged with the same class.

Every mutant carries a `class` in `mutants.json` and `report.json`: `errpath`,
`concurrency` or `default`. `totals.classes` scores each class on its own, next to the
overall `score`, so "error paths: 40% killed" is visible:

```json
"classes": {
  "default": { "mutants": 120, "killed": 96, "survived": 14, "score": 0.87 },
  "errpath": { "mutants": 25, "killed": 8, "survived": 12, "score": 0.4 }
}
```

`totals.covered_score` is the score over the covered mutants only, `NO_COVERAGE` left out
(PIT's test strength): how well the tests check the code they do reach.
