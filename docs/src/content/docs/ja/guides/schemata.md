---
title: "スキーマタ実行"
description: "全ミュータントを埋め込んで一度だけビルドし、すべて実行して、テストスイートを最小化する。"
---

```bash
# Write sources with every mutant embedded and every block traced, plus
# overlay.json, mutants.json and blocks.json.
go run ./cmd/mutrim gen -schemata out -blocks blocks.json -o mutants.json ./path/to/pkg

# Build the test binary once from the schemata sources.
go test -c -overlay out/overlay.json -o pkg.test ./path/to/pkg

# Re-execute it once per mutant and write report.json.
go run ./cmd/mutrim run -test-bin pkg.test -mutants mutants.json -dir ./path/to/pkg -out report.json

# Report redundant tests, functions whose mutants survive and functions no test
# runs. Never deletes anything.
go run ./cmd/mutrim minimize -mutants mutants.json -blocks blocks.json -srcs ./path/to/pkg report.json

# Render the run for another tool: Stryker JSON, its HTML viewer, CI annotations.
go run ./cmd/mutrim report -mutants mutants.json -srcs ./path/to/pkg report.json
```

これらの手順は [`mutrim test`](../test/) がパッケージごとに実行するものである。手で実行する場合、
スキーマタのソースは `github.com/illumination-k/mutrim/mut` を import するので、対象モジュールは
mutrim を依存に持つ必要がある。`mutrim test` は代わりにランタイムを overlay で注入する。`run` は `TEST_SHARD_INDEX` / `TEST_TOTAL_SHARDS` と
`TEST_UNDECLARED_OUTPUTS_DIR` に従い、`-previous report.json` は以前の結果を引き継ぐので、
新しいミュータントだけが実行される。`-test-srcs` (パッケージの `_test.go` ファイル。
`-extra-test` には `-extra-test-srcs pkg=files`) を指定すると、`report.json` の各テストはその
関数のソースの SHA-256 を持ち、結果はそのテストが変わっていない間だけ引き継がれる。KILLED /
TIMEOUT は `killed_by` の各テストがまだ存在し、ミュータントに到達し、同じハッシュを持つ間だけ、
それ以外の結果はミュータントに到達するテストとそのハッシュが同じである間だけ引き継がれる。
そのため、新しく追加されたテストや書き直されたテストには生存ミュータントを kill する機会が
与えられる。`mutation_test` は自身の `srcs` を渡す。

`run` はまず、`GOMUTANT_TRACE` を設定した状態で各トップレベルテストを単独で実行する。これにより
`mut` ランタイムが、そのテストが到達したミュータントサイトを記録する。その後、各ミュータントは
それに到達するテストに対してだけ実行され、`report.json` にはテストごとの kill 行列が保持される。
すなわち `tests` (名前、所要時間、到達したサイトとブロック) と、ミュータントごとにそれを kill した
すべてのテストである。どのテストも到達しないミュータントは `NO_COVERAGE` であり、実行されない。

サイトカバレッジはミュータントサイトのないコードを見られない。関数呼び出しと代入だけが並ぶ
関数にはサイトがないので、それだけを実行するテストは何も満たさず、冗長と判定されてしまう。
そこでスキーマタのソースは、すべてのブロックの先頭 (関数と関数リテラルの本体、`if` / `else` /
`for` / `range` の本体、`case` 節と `select` 節) でも `mut.Reach(id)` を呼ぶ。これはトレース専用の
サイトで、ミュータントにはならない。トレースしなければ何もせず、トレース時にはサイトと同じように
ブロックを記録するので、`tests[].blocks` は `-cover` を使わずに `go test` でも Bazel でも正確な
テストごとのブロックカバレッジになる。`gen -blocks` はブロックの一覧 (`blocks.json`: ID、関数、
位置) を書き出す。`minimize` は到達したブロックをそれぞれ要件に加え (`-w-block`、既定 1。
`-w-site` 1、`-w-kill` 5 と並ぶ)、`-blocks blocks.json` を指定すると、どのテストも到達しない
ブロックを持つ関数を `weak_spots` と並べて `uncovered` に列挙する (`blocks`、`unreached`、
最初の未到達ブロックの `line`)。ミュータントのない関数は weak spot にはならないが、uncovered には
なり得る。

`totals` はカウントと並べて実行のスコアを報告する。`score` はミューテーションスコア
(killed + timeout) / (killed + timeout + lived + no_coverage) であり、どれだけテストされていないか
を表す。`covered_score` はカバーされたミュータントだけに対するスコア
(killed + timeout) / (killed + timeout + lived) であり、既存のテストがどれだけ良いかを表す。
`coverage` はテストが到達する viable なミュータントの割合 (killed + timeout + lived) /
(… + no_coverage) である。viable なものが何もなければ、3 つともゼロになる。

テストの外側の原因で死んだ実行、すなわちランタイムが `fatal error:` に当たった後にテスト
バイナリが `--- FAIL:` 行を一切出さずに終了した場合、プロセスがシグナルで kill された場合、
testing パッケージが決して使わない終了コードでバイナリが終了した場合は `RUN_ERROR` であり、
決して kill ではない。どのテストも失敗していないので、その終了はミュータントについて何も
語らない。これはどのスコアにも数えられず (`run -previous` は決して引き継がないので、その
ミュータントは再度実行される)、`minimize` もこれを無視する。どのテストも存続させず、弱点にも
ならない。`--- FAIL:` 行や `panic:` 行があればこれより優先される。その場合、失敗はテストの
内側から来たものであり、ミュータントに原因があるので、その実行は通常の kill となる。

等価ミュータントは 2 つの方法で除外される。`gen` は一部を静的に等価と証明し、`mutants.json` で
`"equivalent"` とそれを判定したルールを付ける。`identity-operand` (`x * 1` → `x / 1`、
`x << 0` → `x >> 0`、整数に対する `x + 0` → `x - 0`、およびそれらの複合代入) と
`non-negative-operand` (`len`、`cap` や符号なしの値では区別できない定数に対する境界の入れ替え、
`len(s) > -1` → `len(s) >= -1`) である。`run` はそれらをビルドも実行もせずに `EQUIVALENT` と
報告する。`run` はまた、すべてのミュータント実行をトレースする。どのテストも失敗せず、テストが
ミュータントなしの場合とまったく同じサイトに到達した生存ミュータントは `SUSPECT_EQUIVALENT`
となる (結果も実行経路も変えておらず、これは等価性を示唆する。Schuler & Zeller, STVR 2013)。
どちらもスコアには数えられない。`run -count-suspect` (`mutation_test` の `count_suspect` 属性)
は、スコア、弱点、エクスポートされるレポートにおいて、疑わしいものを再び生存として数える。

`run -subtests` は、親テストの代わりに各サブテストをこの行列の行にする。ベースライン実行が
サブテストに名前を付け (`=== RUN TestParse/empty_input`)、それぞれが
`-test.run '^TestParse$/^empty_input$'` で単独でトレースされ、ミュータントごとに、ある親に属する
到達サブテストが 1 つのプロセスで実行され、その `--- FAIL:` 行がサブテストごとの kill を与える。
サブテストを持たないテストは 1 行のままであり、各行は自身の `parent` を持つ。これによりテーブル
駆動のスイートをケース単位で最小化できるが、代償としてサブテストごとに 1 つのトレースプロセスが
必要になる。サブテスト名は実行間で安定している必要があり (ランダムなデータから導いた名前は安定
しない)、Stryker のテストごとのカバレッジと同様に、サブテストは順序に依存してはならない。兄弟
テストが残した状態に依存するケースは単独では失敗しうるし、`run` はそれをエラーとして報告する。
