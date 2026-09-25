---
title: "クイックスタート"
description: "mutrim を依存に加えずに、モジュール全体を一つのコマンドでミューテーションテストする。"
---

```bash
go install github.com/illumination-k/mutrim/cmd/mutrim@latest

# In the module to test: every package with tests, results under .mutrim/.
mutrim test ./...

# Also minimize the suite and render the reports.
mutrim test -minimize -report html,github ./...

# In CI: only the mutants a pull request touches, failing below a score.
mutrim test -in-diff origin/main -threshold 0.8 ./...
```

`mutrim test` は[スキーマタ実行](../schemata/)の手順をパッケージごとに組み合わせる。`go list`
でパッケージを列挙し、ミュータントを生成し (`gen -schemata`)、テストバイナリを一度だけビルドして
(`go test -c -overlay`)、ミュータントごとに実行する (`run`)。モジュールの `go.mod` は変わらない。
スキーマタのソースは `mut` ランタイムを `<module>/internal/mutrimrt` から import し、このパッケージは
`mutrim test` がランタイムのコピーを書き込むビルド overlay の中にだけ存在する。`_test.go` ファイルの
ないパッケージはスキップされる。

パッケージごとに `.mutrim/<import path>/` へ `mutants.json`、`blocks.json`、`overlay.json`、
スキーマタのソース、テストバイナリ、`report.json` を書き、合算した totals を stdout に出力する:

```json
{
  "packages": [{ "pkg": "example.com/m/a", "report": ".../report.json", "totals": { … } }],
  "totals": { "mutants": 51, "killed": 36, "score": 0.87, … }
}
```

次回の実行は `.mutrim/<import path>/report.json` を `run -previous` として読むので、結果は
それを観測したテストが変わらない限り引き継がれる ([スキーマタ実行](../schemata/)の `-test-srcs`
を参照。`mutrim test` はパッケージの `_test.go` ファイルを渡す)。`.mutrim/` は `.gitignore` に加える。

| フラグ               | 意味                                                                                                            |
| -------------------- | --------------------------------------------------------------------------------------------------------------- |
| `-out`               | 出力ディレクトリ (既定 `.mutrim`)                                                                               |
| `-previous`          | 結果を引き継ぐ以前の実行のディレクトリ (既定 `.mutrim`)。`-previous=` ですべて実行                              |
| `-operators`         | オペレータ。`gen -operators` と同じ                                                                             |
| `-in-diff <ref>`     | `git diff --merge-base <ref>` に実行を絞る。commit-relevant ミュータントを含む `run -in-diff` と同じ            |
| `-threshold`         | 合算した `score` がこれ未満なら、すべての出力を書いた後に失敗する                                               |
| `-threshold-covered` | 合算した `covered_score` について同様                                                                           |
| `-p`                 | 同時にビルド・実行するパッケージ数 (既定 GOMAXPROCS)                                                            |
| `-jobs`              | パッケージあたりのテストプロセス数 (既定 GOMAXPROCS / `-p`)                                                     |
| `-minimize`          | 全パッケージにわたる `minimize.json` を書く (`minimize -blocks`)                                                |
| `-report`            | カンマ区切り: `stryker` (`mutation-report.json`)、`html` (`mutation-report.html`)、`github` (`annotations.txt`) |

`-minimize` と `-report` は合算した `mutants.json` と `blocks.json` を `-out` に書き、全パッケージの
レポートに対して `minimize` と `report` を実行する。GitHub はアノテーションをステップの stdout から
読むが、`mutrim test` は stdout を JSON に使うので、後続のステップで `cat .mutrim/annotations.txt`
する。ドライバが公開していない機能 (サブテスト、サンプリング、追加のテストバイナリ、確認のための
再実行) は低レベルのコマンドを使う。
