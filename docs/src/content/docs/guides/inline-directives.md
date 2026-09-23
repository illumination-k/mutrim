---
title: Inline directives
description: "Suppress mutants for a line, a region or a function with //mutrim: comments."
---

The `gen` flags above are the run-wide switches; comments are the local one. A site a
directive suppresses is reported exactly like a filtered one: still listed in
`mutants.json`, with `"ignored"` naming the directive and `"reason"` keeping its text, and
`run` reports it `IGNORED` without building or executing it.

```go
//mutrim:disable [op,...] [reason]            // until //mutrim:enable
//mutrim:disable-next-line [op,...] [reason]  // the line below
//mutrim:disable-func [op,...] [reason]       // in a function's doc comment
//mutrim:enable                               // closes every open disable
```

```go
func Retry(attempts int) error {
	//mutrim:disable-next-line relational,constant the bound is arbitrary
	for i := 0; i < 3; i++ {
	}
	return nil
}
```

The operator list is optional and defaults to every operator, which `all` spells explicitly.
A first word that does not name operators starts the reason instead, so a reason never needs
quoting. A `//mutrim:disable` that nothing closes runs to the end of the file. A directive
wins over the `gen` filters, which are only consulted for a site no directive covers.
