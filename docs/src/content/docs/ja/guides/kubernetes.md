---
title: "Kubernetes (Argo Workflows)"
description: "schemata パイプラインを、パッケージとシャードに展開する Argo Workflows の DAG。"
---

[`deploy/argo/mutation-pipeline.yaml`](https://github.com/illumination-k/mutrim/blob/main/deploy/argo/mutation-pipeline.yaml)
は、Bazel を使わないパイプライン（`mise run dogfood` がローカルで流すもの）をクラスタ上で
実行する `WorkflowTemplate` です。パッケージごとに 1 つの Pod がビルドし、シャードごとに 1 つの
Pod が実行し、レポートはパッケージ単位とスイート全体の結果にまとめられます。

```text
checkout ─┬─ package(a) ─┐
          ├─ package(b) ─┼─ suite
          └─ ...        ─┘

package(pkg) = prepare ─┬─ shard 0 ─┬─ summarize
                        ├─ shard 1 ─┤
                        └─ ...     ─┘
```

| ステップ    | 実行内容                                                                                  | 出力                                                                           |
| ----------- | ----------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------ |
| `checkout`  | git clone、`go mod download`、`go install .../cmd/mutrim@<mutrim-version>`                | 以降の全ステップが使うワークスペース（ソース、モジュールキャッシュ、`mutrim`） |
| `prepare`   | `mutrim gen -schemata -blocks`、`go test -c -overlay`                                     | `mutants.json`、`blocks.json`、schemata テストバイナリ                         |
| `shard`     | `TEST_SHARD_INDEX` / `TEST_TOTAL_SHARDS` 付きの `mutrim run`                              | `reports/<pkg>/<shard>.json`                                                   |
| `summarize` | パッケージの全シャードに対する `mutrim minimize` と `mutrim report`（Stryker JSON、HTML） | `results/<pkg>/`                                                               |
| `suite`     | 全パッケージのレポートに対する 1 回の `mutrim minimize -matrix`                           | `results/suite/`：テストをどこで到達・kill したかで判定                        |

```bash
kubectl apply -f deploy/argo/mutation-pipeline.yaml
argo submit --watch --from workflowtemplate/mutrim-mutation-pipeline \
  -p repo=https://github.com/you/yours.git -p revision=main \
  -p packages='["pkg/a", "pkg/b"]' -p shards=8 -p jobs=4
```

| パラメータ       | デフォルト                                      | 意味                                                               |
| ---------------- | ----------------------------------------------- | ------------------------------------------------------------------ |
| `repo`           | このリポジトリ                                  | 変異させるモジュールの git URL（ルートに `go.mod`）                |
| `revision`       | `main`                                          | ブランチ、タグ、コミット                                           |
| `packages`       | `["criteria", "minimize", "mutator", "runner"]` | パッケージディレクトリの JSON リスト                               |
| `shards`         | `4`                                             | パッケージあたりのシャード数                                       |
| `jobs`           | `2`                                             | `mutrim run -jobs`、および各シャード Pod が要求する CPU 数         |
| `sample`, `seed` | `0`, `0`                                        | `mutrim run -sample` / `-seed`（[サンプリング](../sampling/)）     |
| `mutrim-version` | `main`                                          | `go install` する mutrim のバージョン                              |
| `image`          | `golang:1.27`                                   | 全ステップのイメージ。モジュールが要求する Go ツールチェーンが必要 |

ステップ間のファイルはデフォルトのアーティファクトリポジトリを経由し、これは S3 互換
（S3、MinIO、S3 API 経由の GCS）である必要があります。シャードのレポートは
`<workflow name>/reports/<pkg>/` 配下の key-only アーティファクトで、パッケージごと、および
スイート全体で 1 つのディレクトリとして読み戻されます。結果は `<workflow name>/results/` 配下に
置かれます。ミュータントは ID でシャーディングされるため、パッケージのシャード同士は準備済みの
ビルド以外を共有せず、ノード喪失で落ちたシャードは 1 回だけ再試行されます。
