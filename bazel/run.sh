#!/usr/bin/env bash
# Test script of mutation_test: runs `mutrim run` on the schemata test binary,
# then `mutrim minimize` on its report. Arguments are the rlocationpaths of
# mutrim, the test binary, mutants.json and the test sources (scanned for the
# mutrim:keep tag).

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

mutrim="$(rlocation "$1")"
test_bin="$(rlocation "$2")"
mutants="$(rlocation "$3")"
srcs=""
for src in "${@:4}"; do
	srcs="${srcs:+$srcs,}$(rlocation "$src")"
done
out="${TEST_UNDECLARED_OUTPUTS_DIR:?}"

"$mutrim" run -test-bin "$test_bin" -mutants "$mutants" -out "$out/report.json"
"$mutrim" minimize -mutants "$mutants" -srcs "$srcs" -o "$out/minimize.json" "$out/report.json"
