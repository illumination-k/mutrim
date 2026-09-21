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
