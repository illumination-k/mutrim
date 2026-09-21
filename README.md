# mutrim

Bazel-native mutation testing for Go that also uses the results to minimize test suites.
Pre-alpha; see [docs/plan.md](docs/plan.md) for the roadmap.

## Usage (non-Bazel dev loop)

```bash
# List type-checked-viable mutants of a package as JSON.
go run ./cmd/mutrim gen ./path/to/pkg > mutants.json

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

The go/types pre-filter re-checks the whole package once per mutant, so its cost is what
scales. Measure it with:

```bash
go test ./mutator -run '^$' -bench . -benchtime 3x            # fixture + go/parser
go test ./mutator -run '^$' -bench Check -benchpkg go/types    # one extra package
```

`BenchmarkCheck` is one re-check; `BenchmarkGenerate` is the full pipeline with `ms/mutant`.
