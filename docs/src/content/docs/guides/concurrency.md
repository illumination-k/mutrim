---
title: Concurrency mutants
description: "The opt-in concurrency operator and how to confirm its kills."
---

The opt-in `concurrency` operator (`-operators default,concurrency`) mimics real Go
concurrency bugs by inverting their fixes (Tu et al., "Understanding real-world concurrency
bugs in Go", ASPLOS 2019): `defer f()` removed or run on the spot, `go f()` run inline, a
send `ch <- v` removed, `make(chan T)` given a buffer of 1 and `make(chan T, n)` losing its
buffer (or, for a size computed at run time, growing by one), a select case whose channel
becomes nil so it never fires, `atomic.AddT(&x, d)` / `atomic.StoreT(&x, v)` made the plain
`x += d` / `x = v`, and `once.Do(f)` made `f()`. Removing a `close`, a `Lock` / `Unlock`, a
WaitGroup `Add` / `Done` / `Wait` or a `cancel()` is a call statement removed, which
`voidcall` already does.

Their kills depend on scheduling. Build the test binary with the race detector
(`go test -c -race`, the `race` attribute of `mutation_test`), so an introduced data race
fails the test that hits it, and confirm kills with `run -confirm-kills N`. A mutant that
deadlocks runs into the per-mutant timeout and counts as killed (`TIMEOUT`).
