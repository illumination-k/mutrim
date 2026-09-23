#!/usr/bin/env bash
# Runs mutrim on its own packages: gen -schemata, go test -c with the overlay,
# run, and minimize, writing <out>/<package>/{mutants,report,minimize}.json.
# Arguments are package directories relative to the repo root; the default is
# every library package. Not included: mut, since the runtime cannot import
# itself, and cmd/mutrim, whose mutated path handling makes its tests write
# into the working tree (pass it explicitly and clean up afterwards).
# MUTRIM_OUT selects the output directory (default: .mutrim). Each phase's
# wall time goes to <out>/timings.tsv, printed at the end, so the script
# doubles as the end-to-end benchmark.
set -euo pipefail

cd "$(dirname "$0")/.."
# Absolute: the runner resolves the test binary from the package directory.
mkdir -p "${MUTRIM_OUT:-.mutrim}"
out="$(cd "${MUTRIM_OUT:-.mutrim}" && pwd)"
pkgs=("$@")
if [ ${#pkgs[@]} -eq 0 ]; then
	pkgs=(criteria minimize mutator runner)
fi

# timed runs a phase of package $pkg and records its wall time.
timed() {
	local phase=$1 start=$EPOCHREALTIME
	shift
	"$@"
	awk -v p="$pkg" -v f="$phase" -v s="$start" -v e="$EPOCHREALTIME" \
		'BEGIN { printf "%s\t%s\t%.2fs\n", p, f, e - s }' >>"$out/timings.tsv"
}

go build -o "$out/mutrim" ./cmd/mutrim
: >"$out/timings.tsv"
for pkg in "${pkgs[@]}"; do
	dir="$out/$pkg"
	rm -rf "$dir"
	mkdir -p "$dir"
	echo "== $pkg" >&2
	timed gen "$out/mutrim" gen -schemata "$dir/schemata" -o "$dir/mutants.json" "./$pkg"
	timed build go test -c -overlay "$dir/schemata/overlay.json" -o "$dir/pkg.test" "./$pkg"
	timed run "$out/mutrim" run -test-bin "$dir/pkg.test" -mutants "$dir/mutants.json" -dir "./$pkg" -out "$dir/report.json" 2>"$dir/run.log"
	timed minimize "$out/mutrim" minimize -mutants "$dir/mutants.json" -srcs "./$pkg" -o "$dir/minimize.json" "$dir/report.json"
	sed -n '/"totals"/,/}/p' "$dir/report.json" >&2
done
cat "$out/timings.tsv" >&2
