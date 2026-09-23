"""Implementation of mutation_test.

mutrim_schemata lowers a go_library into schemata sources, with every mutant
embedded and selected at runtime through GOMUTANT_ID, and exposes them as a Go
library with the library's import path. mutation_test embeds that library into
a go_test, the identity check, and _mutrim_test re-executes the resulting
binary once per mutant, with mutrim itself as the sharded test executable.
mutrim_relink relinks the go_test of another package against the schemata
library, so its tests can kill the mutants too.
"""

load("@rules_go//go:def.bzl", "GoArchive", "GoInfo", "go_context", "go_rule", "go_test", "new_go_info")

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
    args.add_joined("-arid", ctx.attr.arid, join_with = ",", omit_if_empty = True)
    if ctx.attr.no_arid:
        args.add("-no-arid")
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
        "arid": attr.string_list(
            doc = """Extends the built-in arid rules with callee globs: the mutants of a
matching call and of its arguments are ignored (`mutrim gen -arid`).""",
        ),
        "no_arid": attr.bool(
            doc = """Turns the built-in arid rules off; `arid` still applies
(`mutrim gen -no-arid`).""",
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

def _fields(info):
    return {name: getattr(info, name) for name in dir(info)}

def _rlocationpath(ctx, f):
    """The runfiles path of f, as $(rlocationpath) spells it."""
    if f.short_path.startswith("../"):
        return f.short_path[3:]
    return ctx.workspace_name + "/" + f.short_path

def _mutrim_relink_impl(ctx):
    go = go_context(
        ctx,
        go_context_data = ctx.attr._go_context_data,
        maybe_needs_cc_toolchain = False,
    )
    library = ctx.attr.library.label
    test = ctx.attr.test
    root = test[GoArchive]

    # Every archive of the test binary, dependencies first. Archives are
    # keyed by importmap, which is unique within one link; the internal and
    # external test archives share the test's label. Starlark has no
    # recursion or while loop, so the depth-first walk runs off an explicit
    # stack, bounded by the pushes it can make: an archive is pushed once
    # per importer and once more after its dependencies.
    transitive = root.transitive.to_list()
    bound = 2
    for data in transitive:
        bound += 2 * (1 + len(data._dep_labels))
    order = []
    expanded = {}
    stack = [(root, False)]
    for _ in range(bound):
        if not stack:
            break
        arc, after = stack.pop()
        key = arc.data.importmap
        if after:
            order.append(arc)
            continue
        if key in expanded:
            continue
        expanded[key] = True
        stack.append((arc, True))
        if arc.data.label == library:
            continue
        for dep in arc.direct:
            if dep.data.importmap not in expanded:
                stack.append((dep, False))
    if stack:
        fail("mutation_test: the archives of {} were not all visited".format(test.label))

    # The mutated library is replaced by its schemata, and every archive
    # that imports it, directly or not, is recompiled against the
    # replacement: the linker rejects export data that differs from the one
    # a package was compiled with. This is what go_test itself does to link
    # the library under test with its external tests.
    archives = {}
    changed = {}
    importpath = None
    test_srcs = []
    for i, arc in enumerate(order[:-1]):
        key = arc.data.importmap
        if arc.data.label == test.label:
            test_srcs += [f for f in arc.data.srcs if f.basename.endswith("_test.go") and f not in test_srcs]
            if getattr(arc.source, "testfilter", None) == "exclude":
                importpath = arc.data.importpath
        if arc.data.label == library:
            archives[key] = ctx.attr.schemata[GoArchive]
            changed[key] = True
            continue
        if not [d for d in arc.direct if changed.get(d.data.importmap)]:
            archives[key] = arc
            continue
        source = _fields(arc.source)
        source["deps"] = [archives[d.data.importmap] for d in arc.direct]
        archives[key] = go.archive(go, GoInfo(**source), _recompile_suffix = ".{}{}".format(ctx.label.name, i))
        changed[key] = True
    if library not in [arc.data.label for arc in order]:
        fail("mutation_test: {} does not depend on {}; extra_tests are the tests of packages importing it".format(test.label, library))

    # The root is the test main; relink it with the settings go_test uses,
    # so the binary changes to its own package directory under the runfiles
    # tree like the original.
    source = _fields(root.source)
    source["name"] = ctx.label.name + "~testmain"
    source["deps"] = [archives[d.data.importmap] for d in root.direct]
    run_dir = test.label.package or "."
    if test.label.repo_name:
        run_dir = "../{}/{}".format(test.label.repo_name, run_dir)
    _, executable, runfiles = go.binary(
        go,
        name = ctx.label.name,
        source = GoInfo(**source),
        gc_linkopts = [
            "-X",
            "+initfirst/github.com/bazelbuild/rules_go/go/tools/bzltestutil/chdir.RunDir=" + run_dir,
            "-X",
            "testing.testBinary=1",
        ],
    )

    # `mutrim bazel-test -extra-test` reads the manifest: the package the
    # binary tests, the binary, and its test files, scanned for the
    # mutrim:keep tag, all as runfiles paths.
    manifest = ctx.actions.declare_file(ctx.label.name + ".json")
    ctx.actions.write(manifest, json.encode({
        "pkg": importpath or root.data.importpath,
        "bin": _rlocationpath(ctx, executable),
        "srcs": [_rlocationpath(ctx, f) for f in test_srcs],
    }))
    return [DefaultInfo(
        files = depset([manifest]),
        runfiles = runfiles.merge(test[DefaultInfo].default_runfiles).merge(
            ctx.runfiles(files = [executable] + test_srcs),
        ),
    )]

mutrim_relink = go_rule(
    _mutrim_relink_impl,
    attrs = {
        "test": attr.label(
            mandatory = True,
            providers = [GoArchive],
            doc = "The go_test of a package that imports the mutated library.",
        ),
        "library": attr.label(
            mandatory = True,
            doc = "The go_library mutation_test mutates.",
        ),
        "schemata": attr.label(
            mandatory = True,
            providers = [GoArchive],
            doc = "The mutrim_schemata of that library.",
        ),
        "_go_context_data": attr.label(
            default = "@rules_go//:go_context_data",
        ),
    },
    doc = """Relinks a go_test against the schemata of the library it imports.

The output is a JSON manifest naming the package the test binary tests, the
binary and the package's test files, as runfiles paths; the runfiles hold
them.""",
)

def _mutrim_test_impl(ctx):
    mutrim = ctx.executable._mutrim
    executable = ctx.actions.declare_file(ctx.label.name + ("." + mutrim.extension if mutrim.extension else ""))
    ctx.actions.symlink(output = executable, target_file = mutrim, is_executable = True)
    runfiles = ctx.runfiles(files = ctx.files.data)
    for target in [ctx.attr._mutrim] + ctx.attr.data:
        runfiles = runfiles.merge(target[DefaultInfo].default_runfiles)
    return [
        DefaultInfo(executable = executable, runfiles = runfiles),
        RunEnvironmentInfo(environment = {
            k: ctx.expand_location(v, ctx.attr.data)
            for k, v in ctx.attr.env.items()
        }),
    ]

_mutrim_test = rule(
    _mutrim_test_impl,
    test = True,
    attrs = {
        "data": attr.label_list(
            allow_files = True,
            doc = "Files the test reads, named in args by their runfiles paths.",
        ),
        "env": attr.string_dict(
            doc = "Environment of the test, subject to $(location) expansion.",
        ),
        "_mutrim": attr.label(
            default = Label("//cmd/mutrim"),
            executable = True,
            cfg = "target",
        ),
    },
    doc = "Runs `mutrim` as the test executable, with the rule's args.",
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
        arid = [],
        no_arid = False,
        subtests = False,
        confirm_kills = 1,
        confirm_baseline = 1,
        count_suspect = False,
        threshold = None,
        threshold_covered = None,
        extra_tests = [],
        race = False,
        jobs = None,
        min_timeout = None,
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

    With `subtests`, every subtest (`TestX/case`) is a row of the kill
    matrix instead of its parent, so `minimize.json` can call a table row
    redundant; a tag on the parent protects every row. Subtest names must be
    stable across runs.

    A flaky test makes one run call a test essential and the next call it
    redundant, so `confirm_kills` and `confirm_baseline` buy confidence in
    the matrix with reruns: an unreproduced kill lands in `suspicious_by`
    instead of `killed_by`, and a test that does not pass reliably on its
    own is marked flaky and left out of `minimize.json` entirely.

    `extra_tests` names the go_tests of other packages that import the
    library. Each is relinked against the mutants (`<name>_extra<i>`), and
    its tests run against them too, named `<importpath>.TestX` in the
    report, so a mutant only a downstream package's tests catch is KILLED
    instead of LIVED or NO_COVERAGE. It cannot be combined with `race` yet.

    `threshold` and `threshold_covered` make the target a gate: it fails,
    after writing its outputs, when the mutation score (or the score over
    the covered mutants) is below them. Unset, the target passes whatever
    the score. Each shard checks its own mutants; a whole-package score
    needs the shards' reports merged.

    `race` builds the test binary with the race detector, which the
    opt-in `concurrency` operator needs: a data race it introduces fails
    the test that hits it, and a deadlock ends as a TIMEOUT, a kill.

    Setting `MUTRIM_IN_DIFF` to the absolute path of a unified diff
    (`bazel test --test_env=MUTRIM_IN_DIFF=$PWD/pr.diff //...`) scopes the run
    to the lines that diff adds; every other mutant is reported `SKIPPED` and
    counts towards no score.

    `match`, `files`, `exclude_files`, `exclude_re` and `arid` narrow
    the sites that are mutated, and so do the built-in arid rules (logging,
    sleeps, stdout writes, ...) unless `no_arid`; a mutant they reject is still listed in `mutants.json` and
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
        arid: extends the built-in arid rules with callee globs: the mutants
            of a matching call and of its arguments are ignored
            (`mutrim gen -arid`).
        no_arid: turns the built-in arid rules off; `arid` still applies
            (`mutrim gen -no-arid`).
        subtests: makes each subtest a row of the kill matrix
            (`mutrim run -subtests`).
        confirm_kills: reruns a mutant's killing tests until each has failed
            this many runs (`mutrim run -confirm-kills`); a kill that does not
            reproduce is recorded in `suspicious_by` and is no kill. 1 trusts
            the first run.
        confirm_baseline: runs each test this many times while tracing
            (`mutrim run -confirm-baseline`); one that fails in some runs and
            passes in others is marked flaky and takes no part in the kill
            matrix or the cover. 1 trusts the first run.
        count_suspect: counts `SUSPECT_EQUIVALENT` mutants (survivors whose
            tests reached the same sites as without them) as survivors
            (`mutrim run -count-suspect`); by default they count towards no
            score.
        threshold: fails the test when the mutation score is below this
            fraction, e.g. `0.8` (`mutrim run -threshold`); unset is off.
        threshold_covered: fails the test when the score over the covered
            mutants (NO_COVERAGE left out) is below this fraction
            (`mutrim run -threshold-covered`); unset is off.
        extra_tests: go_tests of packages importing the library, whose tests
            also run against its mutants (`mutrim run -extra-test`).
        race: builds the test binary with the race detector (`race = "on"`
            of go_test), for the `concurrency` operator.
        jobs: test processes each shard runs at once (`mutrim run -jobs`);
            unset is GOMAXPROCS. Bazel counts a test as one CPU, so pair it
            with `tags = ["cpu:N"]` when many mutation tests run together.
        min_timeout: floor of the derived per-mutant timeout, a Go duration
            such as `"2s"` (`mutrim run -min-timeout`); unset is 10s. Every
            looping mutant waits it out, so fast, self-contained tests run
            much faster with a lower one.
        shard_count: splits the mutants across this many shards.
        env: environment of the test binary.
        **kwargs: common test attributes (size, timeout, tags, data, ...),
            applied to both tests.
    """
    if len(embed) != 1:
        fail("mutation_test: embed must name exactly one go_library, got {}".format(embed))
    if race and extra_tests:
        # The race transition of go_test does not reach the relinked tests,
        # whose archives would then mix modes.
        fail("mutation_test: race and extra_tests cannot be combined yet")
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
        arid = arid,
        no_arid = no_arid,
        testonly = True,
        visibility = ["//visibility:private"],
    )
    go_test(
        name = schemata + "_test",
        srcs = srcs,
        embed = [":" + schemata],
        deps = deps,
        env = env,
        race = "on" if race else "auto",
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
    extra = []
    for i, test in enumerate(extra_tests):
        relinked = "{}_extra{}".format(name, i)
        mutrim_relink(
            name = relinked,
            test = test,
            library = embed[0],
            schemata = ":" + schemata,
            testonly = True,
            visibility = ["//visibility:private"],
        )
        extra.append(":" + relinked)
    _mutrim_test(
        name = name,
        # Flags of `mutrim bazel-test`; the arguments are the library
        # sources, which the Stryker report quotes, and flags after "--" go
        # to `mutrim run`.
        args = ["bazel-test", "-test-bin", "$(rlocationpath :{}_test)".format(schemata), "-mutants", "$(rlocationpath :{})".format(mutants)] +
               ["-test-src=$(rlocationpath {})".format(src) for src in srcs] +
               ["-extra-test=$(rlocationpath {})".format(t) for t in extra] +
               ["$(rlocationpaths :{})".format(lib_srcs), "--"] +
               (["-subtests"] if subtests else []) +
               (["-confirm-kills={}".format(confirm_kills)] if confirm_kills > 1 else []) +
               (["-confirm-baseline={}".format(confirm_baseline)] if confirm_baseline > 1 else []) +
               (["-count-suspect"] if count_suspect else []) +
               (["-jobs={}".format(jobs)] if jobs != None else []) +
               (["-min-timeout={}".format(min_timeout)] if min_timeout != None else []) +
               (["-threshold={}".format(threshold)] if threshold != None else []) +
               (["-threshold-covered={}".format(threshold_covered)] if threshold_covered != None else []),
        data = [":" + schemata + "_test", ":" + mutants, ":" + lib_srcs] + srcs + extra,
        # Makes the rules_go test binary change to its package directory
        # under the runfiles tree, as it does when Bazel runs it directly.
        env = {"GO_TEST_RUN_FROM_BAZEL": "1"} | env,
        shard_count = shard_count,
        **kwargs
    )
