#!/usr/bin/env bash
# Runs mutrim on its own packages: gen -schemata, go test -c with the overlay,
# run, and minimize, writing <out>/<package>/{mutants,report,minimize}.json.
# Arguments are package directories relative to the repo root; the default is
# every library package. Not included: mut, since the runtime cannot import
# itself, and cmd/mutrim, whose mutated path handling makes its tests write
# into the working tree (pass it explicitly and clean up afterwards).
# MUTRIM_OUT selects the output directory (default: .mutrim).
set -euo pipefail

cd "$(dirname "$0")/.."
# Absolute: the runner resolves the test binary from the package directory.
mkdir -p "${MUTRIM_OUT:-.mutrim}"
out="$(cd "${MUTRIM_OUT:-.mutrim}" && pwd)"
pkgs=("$@")
if [ ${#pkgs[@]} -eq 0 ]; then
	pkgs=(criteria minimize mutator runner)
fi

go build -o "$out/mutrim" ./cmd/mutrim
for pkg in "${pkgs[@]}"; do
	dir="$out/$pkg"
	rm -rf "$dir"
	mkdir -p "$dir"
	echo "== $pkg" >&2
	"$out/mutrim" gen -schemata "$dir/schemata" -o "$dir/mutants.json" "./$pkg"
	go test -c -overlay "$dir/schemata/overlay.json" -o "$dir/pkg.test" "./$pkg"
	"$out/mutrim" run -test-bin "$dir/pkg.test" -mutants "$dir/mutants.json" -dir "./$pkg" -out "$dir/report.json" 2>"$dir/run.log"
	"$out/mutrim" minimize -mutants "$dir/mutants.json" -srcs "./$pkg" -o "$dir/minimize.json" "$dir/report.json"
	sed -n '/"totals"/,/}/p' "$dir/report.json" >&2
done
