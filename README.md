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

## Usage (schemata: one build, all mutants)

```bash
# Write sources with every mutant embedded, plus overlay.json and mutants.json.
go run ./cmd/mutrim gen -schemata out -o mutants.json ./path/to/pkg

# Build the test binary once from the schemata sources.
go test -c -overlay out/overlay.json -o pkg.test ./path/to/pkg

# Re-execute it once per mutant and write report.json.
go run ./cmd/mutrim run -test-bin pkg.test -mutants mutants.json -dir ./path/to/pkg -out report.json
```

The schemata sources import `github.com/illumination-k/mutrim/mut`, so the target module
needs mutrim as a dependency. `run` honors `TEST_SHARD_INDEX` / `TEST_TOTAL_SHARDS` and
`TEST_UNDECLARED_OUTPUTS_DIR`, and `-previous report.json` copies earlier results forward
so only new mutants execute.

## Development

```bash
mise install
mise run ci    # fmt/lint/test; see CLAUDE.md
```

Benchmark mutant generation, including the go/types pre-filter, with:

```bash
go test ./mutator -run '^$' -bench . -benchtime 3x         # fixture + go/parser
go test ./mutator -run '^$' -bench . -benchpkg go/types    # one extra package
```

`BenchmarkGenerate` reports `ms/mutant` for the full pipeline.
