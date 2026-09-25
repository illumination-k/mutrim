# mutrim

Bazel-native mutation testing for Go that also uses the results to minimize test suites.
Pre-alpha.

**Documentation: <https://illumination-k.github.io/mutrim/>** (English / 日本語; sources in
[`docs/`](docs/), an Astro Starlight site).

## Quick start

```bash
go install github.com/illumination-k/mutrim/cmd/mutrim@latest
mutrim test -minimize -report html ./...   # results under .mutrim/, totals as JSON on stdout
```

`mutrim test` runs gen, build and run per package with the runtime injected through the
build overlay, so the tested module never depends on mutrim.

## Usage (non-Bazel dev loop)

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

## Development

```bash
mise install
mise run ci    # fmt/lint/test; see CLAUDE.md
```

See [Development](https://illumination-k.github.io/mutrim/reference/development/) for tasks, benchmarks and dogfooding.

## Documentation site

The site is written in English and Japanese (`docs/src/content/docs/` and its `ja/` mirror).

```bash
MISE_ENV=docs mise install
MISE_ENV=docs mise run docs:dev    # http://localhost:4321/mutrim/
```

`.github/workflows/docs.yml` builds it (`mise run ci:docs`) on every change and publishes it to
GitHub Pages from `main`.
