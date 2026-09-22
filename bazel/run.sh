#!/usr/bin/env bash
# Test script of mutation_test: runs `mutrim run` on the schemata test binary,
# then `mutrim minimize` and `mutrim report` on its report. Arguments are the
# flags of `mutrim run` (such as -subtests), the rlocationpaths of mutrim, the
# test binary and mutants.json, then the test sources (scanned for the
# mutrim:keep tag), "--", and the library sources (quoted by the Stryker
# report).

# --- begin runfiles.bash initialization v3 ---
# Copy-pasted from the Bazel Bash runfiles library v3.
set -uo pipefail
set +e
f=bazel_tools/tools/bash/runfiles/runfiles.bash
# shellcheck disable=SC1090
source "${RUNFILES_DIR:-/dev/null}/$f" 2>/dev/null ||
	source "$(grep -sm1 "^$f " "${RUNFILES_MANIFEST_FILE:-/dev/null}" | cut -f2- -d' ')" 2>/dev/null ||
	source "$0.runfiles/$f" 2>/dev/null ||
	source "$(grep -sm1 "^$f " "$0.runfiles_manifest" | cut -f2- -d' ')" 2>/dev/null ||
	source "$(grep -sm1 "^$f " "$0.exe.runfiles_manifest" | cut -f2- -d' ')" 2>/dev/null ||
	{
		echo >&2 "ERROR: cannot find $f"
		exit 1
	}
f=
set -e
# --- end runfiles.bash initialization v3 ---

run_flags=()
while [[ $# -gt 0 && "$1" == -* ]]; do
	run_flags+=("$1")
	shift
done
mutrim="$(rlocation "$1")"
test_bin="$(rlocation "$2")"
mutants="$(rlocation "$3")"
shift 3
test_srcs=""
while [[ $# -gt 0 && "$1" != "--" ]]; do
	test_srcs="${test_srcs:+$test_srcs,}$(rlocation "$1")"
	shift
done
shift
lib_srcs=""
for src in "$@"; do
	lib_srcs="${lib_srcs:+$lib_srcs,}$(rlocation "$src")"
done
out="${TEST_UNDECLARED_OUTPUTS_DIR:?}"

"$mutrim" run "${run_flags[@]}" -test-bin "$test_bin" -mutants "$mutants" -out "$out/report.json"
"$mutrim" minimize -mutants "$mutants" -srcs "$test_srcs" -o "$out/minimize.json" "$out/report.json"
"$mutrim" report -format stryker -mutants "$mutants" -srcs "$lib_srcs" \
	-o "$out/mutation-report.json" "$out/report.json"
"$mutrim" report -format html -mutants "$mutants" -srcs "$lib_srcs" \
	-o "$out/mutation-report.html" "$out/report.json"
