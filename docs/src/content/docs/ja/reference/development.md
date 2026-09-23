---
title: "開発"
description: "ツールチェーン、CI タスク、ベンチマーク、ドッグフーディング。"
---

```bash
mise install
mise run ci    # fmt/lint/test; see [CLAUDE.md](https://github.com/illumination-k/mutrim/blob/main/CLAUDE.md)
```

タスクは `MISE_ENV` でグループ化されている: `base` (フォーマット、GitHub Actions)、`golang`
(`go test`、golangci-lint、govulncheck)、`bazel` (gazelle、buildifier、`bazel test //...`)。
たとえば `MISE_ENV=bazel mise run ci:bazel` は Bazel のチェックだけを実行する。

`mise run bench` はすべてのベンチマークを実行し、
[benchstat](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat) で要約する。git ref を与えると、
まず一時的な worktree でその ref の同じベンチマークを実行して両者を比較する。性能の変更はこの方法で
判断すべきである:

```bash
mise run bench origin/main                  # working tree vs main, 6 runs each
BENCH=Greedy PKGS=./minimize mise run bench # one benchmark, no comparison
go test ./mutator -run '^$' -bench . -benchpkg go/types    # an extra package to generate for
```

`BENCH` (`-bench` の正規表現)、`COUNT` (ベンチマークごとの実行回数)、`PKGS` で絞り込める。
生の結果は `.bench/` に残る。

| パッケージ | ベンチマーク        | 測定対象                                                                                                  |
| ---------- | ------------------- | --------------------------------------------------------------------------------------------------------- |
| `mutator`  | `BenchmarkGenerate` | go/types の事前フィルタ付きのミュータント生成、ミュータントあたり                                         |
| `mut`      | `BenchmarkSite`     | 1 つのスキーマタサイト: 恒等ビルド、別のミュータントが有効、トレースあり、全 goroutine からのトレースあり |
| `runner`   | `BenchmarkRun`      | ループするミュータントを除いたスキーマタのフィクスチャの実行全体、1 ジョブと GOMAXPROCS ジョブ            |
| `minimize` | `BenchmarkGreedy`   | 集合被覆、100 テスト × 1000 サイトから 3000 × 30000 まで                                                  |
| `minimize` | `BenchmarkCompose`  | 被覆が実行される行列の構築、同じサイズ                                                                    |

`mise run dogfood` (`tools/dogfood.sh`) は mutrim 自身のパッケージでスキーマタのパイプラインを実行し、
パッケージごとに `report.json` と `minimize.json` を `.mutrim/` の下に書き出す。時間がかかる。
`runner` のテストはテストバイナリをビルドして実行し、すべてのミュータントがそれを再実行するからである。
パッケージのディレクトリを渡すと範囲を絞れる。たとえば `tools/dogfood.sh criteria minimize` である。
各フェーズ (gen、build、run、minimize) の経過時間は `.mutrim/timings.tsv` に記録されるので、これは
エンドツーエンドのベンチマークでもある。
