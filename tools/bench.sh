#!/usr/bin/env bash
# Runs the Go benchmarks and summarizes them with benchstat. Given a git ref
# (tools/bench.sh origin/main), it first runs the same benchmarks at that ref
# in a temporary worktree and compares the two; only benchmarks both sides
# have are compared.
#
# BENCH selects benchmarks (-bench regexp, default: all), COUNT the runs of
# each (default: 6, enough for benchstat's confidence intervals), PKGS the
# packages (default: ./...). The raw results land in .bench/, named after
# the side they measure.
set -euo pipefail

cd "$(dirname "$0")/.."
bench="${BENCH:-.}"
count="${COUNT:-6}"
read -ra pkgs <<<"${PKGS:-./...}"
mkdir -p .bench
out="$(cd .bench && pwd)"

# measure runs the benchmarks of the checkout in $1 into $2, echoing them
# to stderr as they finish.
measure() {
	(cd "$1" && go test -run '^$' -bench "$bench" -benchmem -count "$count" "${pkgs[@]}") | tee "$2" >&2
}

if [ $# -eq 0 ]; then
	measure . "$out/new.txt"
	benchstat "$out/new.txt"
	exit
fi

ref=$1
wt="$(mktemp -d)"
trap 'git worktree remove --force "$wt"' EXIT
git worktree add --quiet --detach "$wt" "$ref"
measure "$wt" "$out/old.txt"
measure . "$out/new.txt"
benchstat "$ref=$out/old.txt" "working tree=$out/new.txt"
