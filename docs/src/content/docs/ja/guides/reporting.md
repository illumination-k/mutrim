---
title: "レポート"
description: "実行を Stryker JSON、HTML ビューア、GitHub アノテーションとしてエクスポートする。"
---

`report` は `report.json` を他のツールがすでに読める形式でレンダリングする。`-mutants` は必須で
(ソースの位置を持つ)、1 つの実行のシャードはまとめて渡し、`-srcs` は変異させたソースを含む
ファイルまたはディレクトリを指定する。

```bash
# Stryker mutation-testing-report-schema v2, which the Stryker dashboard accepts.
mutrim report -mutants mutants.json -srcs ./pkg report.json > mutation-report.json

# The same JSON inside the single-file mutation-test-report-app viewer.
mutrim report -format html -mutants mutants.json -srcs ./pkg -o mutation-report.html report.json

# One ::warning per surviving mutant, which GitHub Actions shows on the diff;
# -max-per-line 1 surfaces one mutant per line, as Google's code review does.
mutrim report -format github -max-per-line 1 -mutants mutants.json report.json
```

`KILLED`、`LIVED`、`TIMEOUT`、`NO_COVERAGE` はスキーマの `Killed`、`Survived`、`Timeout`、
`NoCoverage` に対応する。`RUN_ERROR` は `RuntimeError` であり、スキーマはこれをスコアから除外する。
`NOT_VIABLE` はミュータントがビルドされなかったので `CompileError` である。
`IGNORED`、`SKIPPED`、`EQUIVALENT`、`SUSPECT_EQUIVALENT` は `Ignored` であり、抑制したディレクティブや
フィルタ、`not in the diff`、または等価性を `statusReason` に持つ (suspect は `-count-suspect` 指定時
`Survived`)。各ミュータントはそれに到達するテスト (`coveredBy`) と、それを kill したテスト
(`killedBy`) を持つので、ビューアはミュータントごとに kill 行列を表示する。どのレポートにも現れない
ミュータントは除外されるので、1 つのシャードのレポートはそのシャードをレンダリングする。
`mutants.json` のミュータントの `diff` は、Stryker レポートでは説明の後に、GitHub アノテーション
ではメッセージの後に続く。

`-format html` と `-format github` は JSON ではなく独自のテキストを書き出す。GitHub はステップの
stdout からアノテーションを読み、HTML ビューは unpkg からビューアを読み込むので、表示時に
ネットワークが必要だが、公開するのは単一のファイルで済む。
