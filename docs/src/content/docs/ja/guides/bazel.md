---
title: "Bazel"
description: "mutation_test マクロ。"
---

```python
# MODULE.bazel
bazel_dep(name = "mutrim", version = "0.1.0")
local_path_override(module_name = "mutrim", path = "../mutrim")  # until it is published
```

```python
# BUILD.bazel: mirror the package's go_test
load("@mutrim//:defs.bzl", "mutation_test")

mutation_test(
    name = "mutant_calc",
    srcs = ["calc_test.go"],
    embed = [":calc"],
    embedsrcs = ["testdata/golden.txt"],   # optional; files the tests' //go:embed name
    operators = ["default", "-constant"],  # optional; see `mutrim gen -operators`
    match = "^Compute",                    # optional; the gen filters, per attribute
    exclude_files = ["_gen\\.go$"],
    arid = ["(*Metrics).Observe"],         # optional; extends the built-in arid rules
    subtests = True,                       # optional; `mutrim run -subtests`
    confirm_kills = 3,                     # optional; `mutrim run -confirm-kills`
    confirm_baseline = 3,                  # optional; `mutrim run -confirm-baseline`
    extra_tests = ["//app:app_test"],      # optional; `mutrim run -extra-test`
    threshold = 0.8,                       # optional; `mutrim run -threshold`
    sample = 0.2,                          # optional; `mutrim run -sample`
    seed = 42,                             # optional; `mutrim run -seed`
    jobs = 4,                              # optional; `mutrim run -jobs`, pair with tags
    min_timeout = "2s",                    # optional; `mutrim run -min-timeout`
    tags = ["cpu:4"],
    shard_count = 4,
)
```

`bazel test //pkg:mutant_calc` は、全ミュータントを埋め込んだパッケージを一度だけビルドし、
それに対してテストを実行し (`mutant_calc_schemata_test`。元のテストと同様に成功しなければ
ならない)、次にそのバイナリをミュータントごとに、そのミュータントに到達するテストに対して再実行し、
`report.json`、`minimize.json`、Stryker の `mutation-report.json` / `mutation-report.html` を
テストの undeclared outputs (`bazel-testlogs/pkg/mutant_calc/test.outputs/`。シャードごとに
1 ディレクトリ。パッケージ全体の判定には、各シャードのレポートをまとめて `mutrim minimize` や
`mutrim report` に渡す) に書き出す。ミュータントはハーメティックに生成される。ルールは、
rules_go が依存に対してコンパイルしたエクスポートデータに対してライブラリを型検査するので、
サンドボックス内で `go` コマンドは一切実行されない。`extra_tests` は、ライブラリを import する
パッケージの `go_test` を指定する。それぞれはスキーマタライブラリに対して再リンクされ
(`mutant_calc_extra0`, ...)、`go_test` 自身が外部テストに対して行うように、テストとライブラリの
間にあるすべてのパッケージが再コンパイルされ、そのテストもミュータントに対して実行される。
ターゲットは `mutation_test` のパッケージから可視でなければならず、今のところ `race` と併用
できない。`--test_env=MUTRIM_IN_DIFF=$PWD/pr.diff` を渡すと、上記と同様に実行を diff に限定できる。

`//go:embed` は両側で動作する。ライブラリの `embedsrcs` は `embed` とともに渡され (rules_go は、
スキーマタのソースがどこにあっても、埋め込まれたファイルに対してパターンを解決する)、テストの
ものは `go_test` と同様に `embedsrcs` として渡す。未対応: cgo パッケージ。
`examples/` はこのマクロをドッグフーディングしている。ここで
`bazel test //...` を実行すれば動作を確認できる。
