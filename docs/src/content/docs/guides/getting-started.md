---
title: Getting started
description: "Generate mutants and apply one through go test -overlay."
---

```bash
# List type-checked-viable mutants of a package as JSON.
go run ./cmd/mutrim gen ./path/to/pkg > mutants.json

# Select operators: names, "default", and "-name" to remove one. Every operator but
# "call" (a non-void call replaced by its result's zero value) and "concurrency" is in
# the default set.
go run ./cmd/mutrim gen -operators default,-constant ./path/to/pkg
go run ./cmd/mutrim gen -operators default,call ./path/to/pkg

# Narrow the sites: by function name, by file, or by the mutant itself.
go run ./cmd/mutrim gen -match '^\(\*Tree\)\.' -exclude-files '_gen\.go$' \
  -exclude-re 'constant: 0 -> 1' ./path/to/pkg

# Arid code (logging, sleeps, stdout writes, ...) is ignored by default; name more
# callees, or turn the built-in rules off.
go run ./cmd/mutrim gen -arid '(*Metrics).Observe' ./path/to/pkg
go run ./cmd/mutrim gen -no-arid ./path/to/pkg

# Apply one mutant through go build -overlay and run the package's tests.
go run ./cmd/mutrim overlay -id <mutant id> -o overlay.json ./path/to/pkg
go test -overlay overlay.json ./path/to/pkg
```

A failing `go test` means the mutant was killed.

`-match <re>` keeps only the functions whose name matches, in the `(*T).Name` form of the
`func` field; `-files <glob,...>` keeps only the files matching one of the globs and
`-exclude-files <re,...>` drops the files matching one of the regexps (both are tried
against the path as reported, its path relative to the working directory, and its base
name); `-exclude-re <re>` drops the mutants whose `func operator: description` matches, so
a rewrite can be excluded wherever it occurs.

Mutants inside _arid_ nodes are ignored as `"arid"` (Petrović et al., "State of mutation
testing at Google", ICSE-SEIP 2018): a node is arid when a rule says no test observes it,
and a compound node is arid when all of its children are — so emptying
`if err != nil { log.Print(err) }` is ignored while the condition keeps its mutants. The
built-in rules cover calls to `log`, `slog`, `zap`, `zerolog`, `fmt.Print*`, `testing`
helpers, `prometheus` / `metrics` `Inc` / `Add` / `Observe` and `time.Sleep` (the call and
everything in its arguments); `fmt.Fprint*` to `os.Stdout` / `os.Stderr`; the duration or
deadline passed to `context.WithTimeout`, `time.After` and the like; `_ = x` sinks; the
map-cache lookup `if v, ok := m[k]; ok { return v }`; `panic("unreachable")`; and the
bodies of `init` and of `String`, `Error` and `GoString` methods. `-arid <glob,...>` adds
callees, matched as go/types names them, with and without the package path (`slog.Info`
and `log/slog.Info`, `(*slog.Logger).Info`); `-no-arid` turns the built-in rules off,
leaving only the `-arid` callees.

The format argument of a printf-like call (`fmt.Errorf("boom")`, and any function whose
last parameters are `string, ...any`) is ignored as `"printf-format"` by the `string`
operator: lowered into the schemata it would no longer be constant, and vet's `printf`
check, which `go test` and rules_go's `nogo` run, would fail the build.

A filtered mutant is still listed, with
`"ignored"` naming the rule that rejected it, and `run` reports it `IGNORED` without
building or executing it — the counts of a filtered run stay comparable with an unfiltered
one, and `IGNORED` mutants count towards no score.
