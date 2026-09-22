"""Implementation of mutation_test.

mutrim_schemata lowers a go_library into schemata sources, with every mutant
embedded and selected at runtime through GOMUTANT_ID, and exposes them as a Go
library with the library's import path. mutation_test embeds that library into
a go_test, the identity check, and re-executes the resulting binary once per
mutant from a sharded sh_test.
"""

load("@rules_go//go:def.bzl", "GoArchive", "GoInfo", "go_context", "go_rule", "go_test", "new_go_info")
load("@rules_shell//shell:sh_test.bzl", "sh_test")

def _mutrim_schemata_impl(ctx):
    library = ctx.attr.library
    lib = library[GoInfo]
    if lib.cgo:
        fail("mutation_test: {} uses cgo, which mutrim does not support".format(library.label))
    go_srcs = [f for f in lib.srcs if f.extension == "go"]
    other_srcs = [f for f in lib.srcs if f.extension != "go"]
    if not go_srcs:
        fail("mutation_test: {} has no Go sources".format(library.label))

    go = go_context(
        ctx,
        importpath = lib.importpath,
        go_context_data = ctx.attr._go_context_data,
        maybe_needs_cc_toolchain = False,
    )

    # A sandboxed action has no go list, so gen type-checks against the export
    # data rules_go already compiled for every transitive dependency, listed
    # in go build's -importcfg format. The standard library is passed as the
    # directory of its compiled packages.
    lines = []
    exports = []
    for data in library[GoArchive].transitive.to_list():
        if data.label == library.label or not data.export_file:
            continue
        if data.importmap != data.importpath:
            lines.append("importmap {}={}".format(data.importpath, data.importmap))
        lines.append("packagefile {}={}".format(data.importmap, data.export_file.path))
        exports.append(data.export_file)
    importcfg = ctx.actions.declare_file(ctx.label.name + ".importcfg")
    ctx.actions.write(importcfg, "\n".join(lines) + "\n")

    overlay = ctx.actions.declare_file(ctx.label.name + "/overlay.json")
    schemata = [
        ctx.actions.declare_file("{}/{}/{}".format(ctx.label.name, lib.importpath, f.basename))
        for f in go_srcs
    ]

    args = ctx.actions.args()
    args.add("gen")
    args.add("-importpath", lib.importpath)
    args.add("-importcfg", importcfg)
    args.add_all("-stdlib", go.stdlib.libs, expand_directories = False)
    args.add_joined("-tags", go.mode.tags, join_with = ",")
    args.add_joined("-operators", ctx.attr.operators, join_with = ",", omit_if_empty = True)
    if ctx.attr.match:
        args.add("-match", ctx.attr.match)
    args.add_joined("-files", ctx.attr.files, join_with = ",", omit_if_empty = True)
    args.add_joined("-exclude-files", ctx.attr.exclude_files, join_with = ",", omit_if_empty = True)
    if ctx.attr.exclude_re:
        args.add("-exclude-re", ctx.attr.exclude_re)
    args.add("-schemata", overlay.dirname)
    args.add("-o", ctx.outputs.mutants)
    args.add_all(go_srcs)
    ctx.actions.run(
        mnemonic = "MutrimGen",
        progress_message = "Generating mutants of %{label}",
        executable = ctx.executable._mutrim,
        arguments = [args],
        inputs = depset(go_srcs + exports + [importcfg], transitive = [go.stdlib.libs]),
        outputs = schemata + [overlay, ctx.outputs.mutants],
        env = go.env_for_path_mapping,
        toolchain = None,
    )

    go_info = new_go_info(
        go,
        struct(
            srcs = [struct(files = schemata + other_srcs)],
            embedsrcs = [struct(files = lib.embedsrcs)],
            x_defs = lib.x_defs,
        ),
        importpath = lib.importpath,
        deps = lib.deps + [ctx.attr._mut[GoArchive]],
        coverage_instrumented = False,
    )

    # new_go_info collects runfiles from deps only; keep the library's own data.
    fields = {name: getattr(go_info, name) for name in dir(go_info)}
    fields["runfiles"] = go_info.runfiles.merge(lib.runfiles)
    go_info = GoInfo(**fields)

    archive = go.archive(go, go_info)
    return [
        go_info,
        archive,
        DefaultInfo(files = depset(schemata + [archive.data.export_file])),
        # The reporters need the text of the files the mutants point at,
        # which is the library's sources, not the lowered ones.
        OutputGroupInfo(original_srcs = depset(go_srcs)),
    ]

mutrim_schemata = go_rule(
    _mutrim_schemata_impl,
    attrs = {
        "library": attr.label(
            mandatory = True,
            providers = [GoInfo, GoArchive],
            doc = "The go_library to mutate.",
        ),
        "mutants": attr.output(
            mandatory = True,
            doc = "Where mutants.json is written.",
        ),
        "operators": attr.string_list(
            doc = """Operators to apply, as `mutrim gen -operators` takes them: names,
"default", and "-name" to remove one. Empty applies the default set, which leaves out
the opt-in operators.""",
        ),
        "match": attr.string(
            doc = """Keeps only the mutants of functions whose name matches this regexp,
in the `(*T).Name` form of `mutrim gen -match`.""",
        ),
        "files": attr.string_list(
            doc = """Keeps only the mutants in files matching one of these globs
(`mutrim gen -files`). Empty keeps every file.""",
        ),
        "exclude_files": attr.string_list(
            doc = """Drops the mutants in files matching any of these regexps
(`mutrim gen -exclude-files`).""",
        ),
        "exclude_re": attr.string(
            doc = """Drops the mutants whose `func operator: description` matches this
regexp (`mutrim gen -exclude-re`).""",
        ),
        "_mutrim": attr.label(
            default = Label("//cmd/mutrim"),
            executable = True,
            cfg = "exec",
        ),
        "_mut": attr.label(
            default = Label("//mut"),
            providers = [GoArchive],
        ),
        "_go_context_data": attr.label(
            default = "@rules_go//:go_context_data",
        ),
    },
    doc = """Lowers a go_library into schemata sources.

Provides the same GoInfo as a go_library with the library's import path, so
it can be embedded into a go_test in place of the original library.""",
)

def mutation_test(
        name,
        srcs,
        embed,
        deps = [],
        operators = [],
        match = "",
        files = [],
        exclude_files = [],
        exclude_re = "",
        shard_count = None,
        env = {},
        **kwargs):
    """Runs the tests in srcs against every mutant of the embedded library.

    Mirror the go_test of the package: `srcs` are its test files, `embed` the
    go_library under test, `deps` the test dependencies. The macro defines

    - `<name>_schemata`: the library with every mutant embedded and
      `<name>.mutants.json` listing them,
    - `<name>_schemata_test`: a go_test of those sources with no mutant
      selected, which must pass like the original tests do,
    - `<name>`: a test that re-executes that binary once per mutant against
      the tests reaching it, writes `report.json` (the per-test kill matrix)
      to its undeclared outputs, and next to it `minimize.json` (the tests a
      greedy set cover over that matrix finds redundant, and the functions
      whose mutants survive) and `mutation-report.json` / `.html` (the same
      run in the Stryker mutation-testing-elements schema, and its
      single-file viewer). Tests named `TestRegression_*` or tagged
      `//mutrim:keep` in their doc comment are never called redundant.

    Setting `MUTRIM_IN_DIFF` to the absolute path of a unified diff
    (`bazel test --test_env=MUTRIM_IN_DIFF=$PWD/pr.diff //...`) scopes the run
    to the lines that diff adds; every other mutant is reported `SKIPPED` and
    counts towards no score.

    `match`, `files`, `exclude_files` and `exclude_re` narrow the sites that
    are mutated; a mutant they reject is still listed in `mutants.json` and
    reported `IGNORED`, so the counts stay comparable across runs. A single
    site or function is suppressed in the source instead, with a
    `//mutrim:disable` directive, which is reported the same way.

    Args:
        name: name of the runner test.
        srcs: the test sources.
        embed: exactly one go_library.
        deps: dependencies of the test sources.
        operators: operators to apply, as `mutrim gen -operators` takes them
            (e.g. `["default", "-constant"]`); empty applies the default set,
            which leaves out the opt-in operators.
        match: keeps only the mutants of functions whose name matches this
            regexp, in the `(*T).Name` form (`mutrim gen -match`).
        files: keeps only the mutants in files matching one of these globs
            (`mutrim gen -files`); empty keeps every file.
        exclude_files: drops the mutants in files matching any of these
            regexps (`mutrim gen -exclude-files`).
        exclude_re: drops the mutants whose `func operator: description`
            matches this regexp (`mutrim gen -exclude-re`).
        shard_count: splits the mutants across this many shards.
        env: environment of the test binary.
        **kwargs: common test attributes (size, timeout, tags, data, ...),
            applied to both tests.
    """
    if len(embed) != 1:
        fail("mutation_test: embed must name exactly one go_library, got {}".format(embed))
    schemata = name + "_schemata"
    mutants = name + ".mutants.json"
    mutrim_schemata(
        name = schemata,
        library = embed[0],
        mutants = mutants,
        operators = operators,
        match = match,
        files = files,
        exclude_files = exclude_files,
        exclude_re = exclude_re,
        testonly = True,
        visibility = ["//visibility:private"],
    )
    go_test(
        name = schemata + "_test",
        srcs = srcs,
        embed = [":" + schemata],
        deps = deps,
        env = env,
        **kwargs
    )
    lib_srcs = schemata + "_srcs"
    native.filegroup(
        name = lib_srcs,
        srcs = [":" + schemata],
        output_group = "original_srcs",
        testonly = True,
        visibility = ["//visibility:private"],
    )
    mutrim = str(Label("//cmd/mutrim"))
    inputs = [mutrim, ":" + schemata + "_test", ":" + mutants] + srcs
    sh_test(
        name = name,
        srcs = [Label("//bazel:run.sh")],
        # "--" separates the test sources, scanned for the mutrim:keep tag,
        # from the library sources, which the Stryker report quotes.
        args = ["$(rlocationpath {})".format(t) for t in inputs] +
               ["--", "$(rlocationpaths :{})".format(lib_srcs)],
        data = inputs + [":" + lib_srcs],
        deps = ["@bazel_tools//tools/bash/runfiles"],
        # Makes the rules_go test binary change to its package directory
        # under the runfiles tree, as it does when Bazel runs it directly.
        env = {"GO_TEST_RUN_FROM_BAZEL": "1"} | env,
        shard_count = shard_count,
        **kwargs
    )
