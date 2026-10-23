---
title: "実装計画"
description: "mutrim を構築する際に用いた段階的な計画。"
---

これは現在のスキャフォールドから mutrim を構築するための初期計画である。各フェーズが `mise run ci`
をグリーンに保ち、それ単体でテスト可能な成果物を生むように作業を順序付けている。
アーキテクチャと規約は [CLAUDE.md](https://github.com/illumination-k/mutrim/blob/main/CLAUDE.md) で確定しており、本書は
_何をどの順で作るか_ と、各ステップの「完了」が何を意味するかだけを決める。

## 最初のリリースの範囲

- **対象:** Go パッケージのミューテーションテスト。まず Bazel なし (`go test -overlay`) で、次に
  schemata + シャーディングを用いた `mutation_test(...)` によって Bazel 上で行う。
  `examples/` のフィクスチャで `bazel test //...:mutant_*` が動けば完了である。
- **対象外 (後続リリース):** テストごとのカバレッジ行列、重み付き貪欲集合被覆、包含関係の
  レポート。API がそれらに向けて拡張できるようパッケージの骨格は初日から存在するが、Bazel の
  マイルストーンを出荷するまで最小化の作業は始めない。

## リポジトリ構成

```
cmd/mutrim/          CLI: `gen`, `overlay`, `run`, later `minimize` (thin; JSON on stdout)
mutator/             AST rewriting, operators (ops_*.go), type-check pre-filter, mutant IDs,
                     mutants.json, overlay source, schemata lowering (schemata.go)
mutator/testdata/    fixture packages + golden files for the mutator tests
mut/                 runtime package imported by schemata sources; reads GOMUTANT_ID
runner/              re-exec test binary per mutant, sharding, report.json, incremental
criteria/            Criterion interface, SiteCoverage, Mutation, matrix composition
minimize/            greedy set cover, subsumption, protection rules
examples/            Bazel fixtures calling mutation_test on themselves (Phase 3)
defs.bzl, MODULE.bazel
```

パッケージは空のプレースホルダーとしてではなく、そのフェーズの開始時に作成する。

## Phase 0 — 基盤 (完了)

目標: 実際のモジュール構成、依存関係の固定、フィクスチャの配置。

- `go.mod` に `golang.org/x/tools` (`go/packages`) を追加し、ルートのプレースホルダーを削除する。
- `mutator/testdata/` のフィクスチャ: ゴールデンの期待値を持つ演算子ファミリーごとのパッケージ
  1 つずつに加え、「除外ファイル」パッケージ (`_test.go`、`.pb.go`、`mock_*.go`、生成ヘッダー) と、
  エンドツーエンドの overlay チェック用テストを持つ `killable` パッケージ。
- `cmd/mutrim` は素の `flag` サブコマンドを使う。JSON は stdout に、ログは stderr に出す。

## Phase 1 — Mutator (Bazel 非依存、完了)

目標: `mutrim gen ./pkg` が型検査で viable な mutant だけを列挙した `mutants.json` を書き出し、
`mutrim overlay` によって `go test -overlay` で単一の mutant を実行できるようにする。

1. **読み込み。** パッケージごとに 1 回、
   `NeedName|NeedFiles|NeedSyntax|NeedTypes|NeedTypesInfo|NeedDeps|NeedImports` で `go/packages`
   を使う。除外フィルタは、書き換えの前にファイル名とビルド制約に適用する。
2. **Operator インターフェース。**
   ```go
   type Operator interface {
       Name() string                          // stable, used in mutant IDs
       Sites(ctx *Context, n ast.Node) []Site // rewrites this operator can perform on n
   }
   type Site struct {
       Node        ast.Node
       Description string   // "< -> <=" for mutants.json
       Apply, Undo func()   // in-place AST rewrite and its exact inverse
   }
   ```
   ファミリー: 演算子の入れ替えにはテーブル駆動の `BinaryOp` 1 つ、文のファミリーにはそれぞれ
   1 つの型を用いる。mutrim が提供する全演算子は CLAUDE.md に列挙されている。走査するのは関数本体のみである。
3. **Mutant ID。** `sha256(pkgPath, enclosing func name, AST path from func root, operator
   name, description)` を 16 桁の 16 進数に切り詰めたもの。構成上、位置に依存しない。サイトの上に
   行を挿入しても ID が変わらないことをテストで確認する。
4. **型検査による事前フィルタ。** 二項演算子の各サイトについて、`Apply` し、書き換えた式に対して
   元のスコープで `types.CheckExpr` を実行し、エラーなら `viable=false` を記録して `Undo` する。
   文の mutant は構成上 well-typed なのでチェックはない。文脈依存の失敗 (定数オーバーフロー、
   return 置換後の未使用 import) はビルドに任せ、runner はそれを NOT VIABLE として数える。
5. **`mutants.json`。** 候補ごとに 1 エントリ:
   `{id, pkg, file, line, col, func, operator, description, viable, ignored?, reason?}`。
   viable でないエントリも (runner が NOT_VIABLE の数を報告できるよう) ファイルに残すが、
   実行はしない。`gen` のフィルタやインラインの `//mutrim:disable` ディレクティブで抑制された
   エントリも同様で、runner はそれを IGNORED と報告し、どのスコアにも数えない。
6. **Overlay モード。** `mutrim overlay -id <id>` は、その単一の mutant を適用した一時ファイル
   (`go/format`) を指す `go build -overlay` 用 JSON を出力する。これが高速な開発ループであり、
   Bazel を使わないユーザーの経路でもある。

完了: ゴールデンテストが全演算子をカバーし、ID 安定性テストが通り、フィクスチャ上で kill 可能と
わかっている mutant を使って `go test -overlay` を実行し、テストが失敗することを確認するテストがある。

## Phase 2 — Schemata と runner (完了)

目標: パッケージごとに 1 回のビルドで、テストバイナリを mutant ごとに再実行する。

1. **Schemata lowering** (`mutator.Lower`、各演算子の `Site.Schemata` コールバックで駆動)。
   パッケージの viable な mutant はすべて、`mut` ランタイムパッケージを import する書き換え後の
   ソースに埋め込まれる。ヘルパーは Go が被演算子を先行評価する箇所では先行評価で受け取るため、
   評価順序と `recover()` のセマンティクスは変わらず、被演算子の型は明示せずジェネリクスで推論される:

   | 元の式           | lowering 後                                                     |
   | ---------------- | --------------------------------------------------------------- |
   | `a < b`          | `mut.Cmp(id, a, b, "<", "<=")` (`Ordered` に対するジェネリック) |
   | `a == b`         | `mut.Not(id, a == b)` (`!=` はちょうどその否定)                 |
   | `a + b`, `a % b` | `mut.Arith(id, a, b, "+", "-")`、`%` には `mut.ArithInt`        |
   | `a && b`         | `mut.And(id, a, func() bool { return b })` (短絡評価)           |
   | `if c`           | `if mut.Not(id, c)`                                             |
   | `i++`            | `mut.Inc(id, &i)`。map 要素には `if mut.Active(id) {…}` を使う  |
   | `return x`       | `{ if mut.Active(id) { return <zero> }; return x }`             |

   このフェーズ以降に追加された演算子も同じ 2 つの形に従う。式はヘルパー呼び出しになり、文は
   `if mut.Active(id)` で包まれる。1 つのノードに複数の mutant がある場合は、1 つがノードを
   再構築し、他はそれが残したものを包む (`Site.Wraps`) ため、lowering が入れ子になる。

   lowering が辞退したサイトはそのまま残り、その mutant は schemata 下で `NOT_VIABLE` と報告
   される: 定数式 (呼び出しは定数ではない)、defined type の boolean の結果や被演算子 (ヘルパーは
   素の `bool` を返す)、型なしの非定数被演算子、右被演算子が `recover()` を呼ぶ `&&`/`||`、
   `for` の post 文中の `m[k]++`、最後の文によって `switch` や `if` が終端文になる本体、そして
   関数値を持たないジェネリックなライブラリ関数である。`mutrim gen -schemata` はこれらの
   `viable` を false に切り替え、runner がバイナリに含まれない ID を選ばないようにする。

   `mut` ランタイムは init 時に `GOMUTANT_ID` を 1 回だけ読む。変数が未設定なら各ヘルパーは
   恒等関数である。`TestSchemataIdentity` は schemata ソースに対してフィクスチャのテスト群を
   実行し、生成ファイルをゴールデンで固定する。
2. **Runner** (`mutrim run -test-bin <path> -mutants mutants.json`)。
   `id % TEST_TOTAL_SHARDS == TEST_SHARD_INDEX` を満たす viable な mutant ごとに、
   `GOMUTANT_ID=id`、`-test.v -test.failfast` とタイムアウトを付けてバイナリを実行し (issue #31:
   `-timeout-factor` (3) × mutant に到達するテストのトレース時間 + `-timeout-const` (2s)、最低
   10s — Go ツールチェーンを起動するテストは mutant 下でビルドキャッシュを外すことがある — かつ最大で
   `-timeout-factor` × ベースライン実行。`-timeout` で上書きできる)、
   KILLED / LIVED / TIMEOUT / RUN_ERROR に分類する (issue #27: テストの外側で死んだ実行 —
   `--- FAIL:` 行がない、`fatal error:`、シグナル、testing パッケージが決して使わない終了コード —
   は kill ではない。`--- FAIL:` や `panic:` 行があればそちらが優先される)。`-tests` は
   `-test.run` の許可リストである。指定がなければ
   バイナリ全体を実行する (Phase 4 でテストごとのカバレッジによって絞り込み、failfast をやめる)。
3. **`report.json`** を `TEST_UNDECLARED_OUTPUTS_DIR` (または `-out`) に書き出す:
   `{mutant_id, status, tests_run, killed_by, duration_ms, timeout_ms}` に加え、合計、
   ベースライン、タイムアウト上限。スコア上 TIMEOUT は KILLED として数える。RUN_ERROR はどのスコアにも数えず
   (`totals.run_error`)、決して引き継がない。`totals` はミューテーションスコアの隣に
   mutant カバレッジ `totals.coverage`、すなわちテストが到達する viable な mutant の割合も
   報告する (issue #34)。
4. **インクリメンタル再実行。** `-previous report.json`: ID が存在する mutant は結果を引き継ぎ、
   新しい ID は実行し、`NOT_VIABLE` は常に `mutants.json` から再計算する。ID はコンテンツハッシュ
   なので、変更されていない関数は結果を保つ。テストもチェックする (issue #30、PIT / StrykerJS の
   インクリメンタル方式): `tests[].hash` はテスト関数のソース (`-test-srcs`) の SHA-256 であり、
   kill を引き継ぐのは各 killer がまだ存在し、mutant に到達し、ハッシュを保っている間だけである。
   それ以外の結果は、到達するテストとそのハッシュが変わっていない間だけ引き継ぐ。

完了: `runner/testdata/schemata.golden` は `gen -schemata` + `go test -c -overlay` + `run`
で生成した schemata フィクスチャのレポートであり、テストされた関数に埋め込まれた mutant はそこで
すべて KILLED となる。CLI テストは同じパイプラインを `mutrim` 経由で実行する。

## Phase 3 — Bazel 統合 (最初のリリース、完了)

目標: `bazel_dep(name = "mutrim")` + `mutation_test(name, srcs, embed, shard_count = N)`。

1. **`go list` なしでの読み込み。** サンドボックス化されたアクションにはモジュールキャッシュが
   ないため、`mutrim gen -importpath <path> -importcfg <file> -stdlib <dir> files...` は
   rules_go がすでにコンパイルしたエクスポートデータから依存先の型を読み込み、パッケージの
   ファイルを `go/types` で型検査する (`mutator.LoadFiles`、`gcexportdata`。nogo と同じ手法)。
   importcfg は `go build` の形式で、標準ライブラリは `<stdlib>/<goos_goarch>/<path>.a` から
   探す。ビルド制約で除外されたファイルはそのままコピーされるため、schemata ディレクトリは常に
   パッケージの完全なコピーとなる。
2. **`mutrim_schemata` ルール** (`bazel/mutation_test.bzl`)。ライブラリの `GoInfo` /
   `GoArchive` を読み、`GoArchive.transitive` から importcfg を書き出し、schemata ソースと
   `mutants.json` を生成する `MutrimGen` アクションを 1 つ実行し、同じ import パスに `//mut`
   への依存を加えた `GoInfo` を提供する。これにより `go_test` は元のライブラリの代わりに
   それを embed できる。
3. **`mutation_test` マクロ** は、そのルール、schemata ソースに対する `go_test` (恒等性の
   チェック: `GOMUTANT_ID` 未設定ではテストが通らなければならない)、そして `shard_count` 付きで
   テストバイナリに `mutrim run` を実行する `sh_test` (`bazel/run.sh`) に展開される。`report.json`
   は undeclared outputs に置かれる。runner は Bazel のテストプロトコル変数
   (`TEST_TOTAL_SHARDS`、`TESTBRIDGE_TEST_ONLY`、`XML_OUTPUT_FILE`、...) を子プロセスの環境から
   取り除く。rules_go のテスト main もそれらに反応するためである。
4. **リポジトリ。** `MODULE.bazel` (rules_go 0.63、gazelle 0.54、rules_shell。buildifier は
   開発依存)、gazelle で生成した BUILD ファイル、マクロを dogfooding する `examples/calc` と
   `examples/stats` (ワークスペース内の別パッケージと標準ライブラリへの依存を持つ)、
   `mise.bazel.toml` (`fmt:bazel`、`lint:bazel`、`test:bazel`、`ci:bazel`)、そして
   `ubuntu-latest` 上の `ci_bazel.yml` ワークフロー。

非対応: cgo パッケージ。`//go:embed` は動作する (`examples/greet`): schemata ソースは
サブディレクトリに生成されるが、rules_go はライブラリのパターンをその `embedsrcs` に対して
解決し、`mutation_test` はテストの `embedsrcs` を受け取る。`go` を呼び出す mutrim 自身の
テストは Bazel ビルドから除外され、`go test ./...` で実行する。

完了: `bazel test //...` が通り、`report.json` が undeclared なテスト出力として見える。
`v0.1.0` をタグ付けする。

## Phase 4 — Criteria と minimizer (2 回目のリリース、完了)

目標: テストごとの kill 行列と、それが示唆するものを報告する `mutrim minimize`。

1. **`-cover` を使わないテストごとのカバレッジ。** 当初の計画は `-test.coverprofile` による
   ブロックカバレッジだったが、`go test -c -cover -overlay` はディスク上のソースを計装して
   overlay を無視し (そのようなバイナリでは mutant を選択することすらできない)、Bazel では計装は
   `bazel coverage` の中にしか存在しない。代わりに `mut` ランタイムにトレースモードを追加した:
   `GOMUTANT_TRACE=<file>` があると、プロセスが到達した各サイトがその ID を 1 回だけ追記する。
   runner はバイナリのテストを列挙し (`-test.list`)、それぞれを単独でその方法で実行し、テストごとに
   実行時間と到達したサイトを記録する (`report.json` → `tests`)。これが _サイトカバレッジ_ である:
   絞り込みには正確で、Bazel と `go test` で同一であり、見えないのは mutant サイトが一切ないコード
   だけである。ブロックカバレッジは同じ方法でこの穴を埋める (issue #39): スキーマタのソースは
   すべてのブロックの先頭で `mut.Reach(id)` を呼ぶ。これはミュータントにならないトレース専用の
   サイトなので、`tests[].blocks` がテストごとのブロックカバレッジになり、`criteria.BlockCoverage`
   がその `Criterion` になる (`minimize -w-block`、`uncovered` の一覧には `-blocks blocks.json`)。
2. **絞り込み付き runner。** 各 mutant は、そのサイトに到達するテストに対してのみ failfast なしで
   実行するため、`killed_by` は完全な kill 行列となる (タイムアウトした mutant は、開始して終了
   しなかったテストに帰属させる)。どのテストも到達しない mutant は `NO_COVERAGE` で、実行されず、
   スコア上は生存として数える。テストの外側で死んだ実行 —
   `--- FAIL:` 行がない、ランタイムの `fatal error:`、シグナル、または testing パッケージが決して
   使わない終了コード — は `RUN_ERROR` である (issue #27): どのテストも失敗していないので、
   その終了は mutant について何も語らない。どのスコアにも数えず、`-previous` は決して引き継がず、
   `minimize` は無視する。`-previous` が引き継ぐのは KILLED / LIVED / TIMEOUT だけであり、
   NOT_VIABLE と NO_COVERAGE はどちらもコストがかからないので再計算する。
3. **`criteria`。** `Criterion` (`Name`、`Rows`: テスト → ラベル) と、`SiteCoverage` および
   `Mutation`。`Compose` は重み付きの criteria を合併して、`Requirement{Label,
   Weight}` を列、`Test{Name, DurationMS, Covers bitset}` を行とする `Matrix` を作り、出力が
   決定的になるようソートする。JSON 形式 (テストごとのインデックス) は厳密ソルバー向けのエクスポートである。
4. **`minimize`。** `Greedy`: まず保護されたテスト、次に
   gain = 新たに満たされる要件の重みの合計 / max(duration ms, 1) が最大のテストを、名前で
   タイブレークしながら、どのテストも gain を持たなくなるまで繰り返し選ぶ。それ以外のテストは
   すべて冗長であり、それぞれを包含する選択済みテストとともに報告される。保護: 名前の正規表現
   (デフォルト `^TestRegression_`) と `Tagged`。後者は `_test.go` ファイルを解析して
   `//mutrim:keep` の doc コメント行を探す (ディレクティブ形式の行なので、`CommentGroup.Text`
   ではなく生のコメントから読む)。
5. **CLI と Bazel。** `mutrim minimize -mutants mutants.json -srcs dir [-keep re] [-tag t]
   [-w-site 1] [-w-kill 5] [-matrix out.json] report.json...` は `selected`、`redundant`、
   `weak_spots` (LIVED または NO_COVERAGE の mutant を持つ関数。`runner.WeakSpots` から) を出力する。
   `bazel/run.sh` は `mutrim run` の後に、マクロの `srcs` をデータとしてこれを実行するため、
   すべての `mutation_test` は undeclared outputs に `report.json` と `minimize.json` を残す。

6. **サブテストの行** (`run -subtests`、`mutation_test(subtests = True)`)。Go のテスト群は
   大半がテーブル駆動なので、人が削除する単位は `TestParse` ではなく `TestParse/empty_input`
   である。ベースラインの `-test.v` 出力がサブテストを列挙する (サブテストには `-test.list` がない)。
   各サブテストは `-test.run '^TestX$/^case$'` でトレースし、mutant ごとに 1 つの親の到達する
   サブテストを 1 つのプロセスで実行し、`--- FAIL:` 行で帰属させる。行は `parent` を持ち、
   `-keep` は完全名にマッチし、親のタグはその配下のすべての行を保護する。Stryker のテストごと
   モードと同じく既知の危険がある: サブテストは順序に依存してはならない。

7. **確認のための再実行** (`run -confirm-kills`、`run -confirm-baseline`、対応する
   `mutation_test` 属性)。1 回の実行から読んだ kill は不安定であり (Shi, Bell, Marinov,
   ISSTA 2019: mutant × テストのペアの 9%)、テストごとの行列では 1 つの flaky な kill が
   「必須」あるいは「冗長」なテストを生んでしまう。`-confirm-kills N` は kill したテストだけを、
   それぞれが N 回失敗するまで再実行する。再現しなかった失敗は `suspicious_by` に入り、すべての
   killer が suspicious になった mutant は `LIVED` となる。`-confirm-baseline N` は各トレース
   実行を繰り返す。ある実行では失敗し別の実行では通るテストはエラーではなく `tests[]` で `flaky`
   となり、その失敗は決して kill にならず、`minimize` はそれを行列から除いて `flaky_tests` に
   列挙する。`totals.suspicious` が問題の規模を示す。

完了: schemata フィクスチャは冗長なテスト、`TestRegression_*` テスト、タグ付きテストを持つ。
runner のゴールデンは完全な `killed_by` リストと、テストされていない関数に対する `NO_COVERAGE`
を示し、CLI テストは minimize の判定をエンドツーエンドで確認する。`criteria` と `minimize` は
Bazel 下で `examples/` と並んで自身に `mutation_test` を実行する。`v0.2.0` をタグ付けした。

## テスト方針

`mutator/testdata/` 以下のゴールデンファイルは、すべての mutant を位置、演算子、viability、
書き換え後のソース行とともに列挙し、テストはすべての apply/undo の往復の後に再生成して AST が
復元されることを証明する。mutant ごとの期待出力というアイデアは go-mutesting のテスト構成に
倣っているが、ここでのフィクスチャと期待値はゼロから書いたものであり、gremlins (Apache-2.0) や
go-mutesting (MIT) からは何もコピーしていない。

## リスクと未解決の問題

- **ジェネリックなヘルパー vs. 型なし定数。** `mut.Cmp(id, 1, x, …)` は型付きの被演算子から
  `T` を推論し、元の式とまったく同じように定数を変換する。辞退するのは、型付きの兄弟を持たない
  型なしの _非定数_ 被演算子 (裸のシフト) だけである。
- **`//go:generate` 出力の検出** はヒューリスティック (`Code generated ... DO NOT EDIT`
  ヘッダー) である。凝った工夫をするよりも、そのことを文書化する。
- **タイムアウト** にはパッケージごとのベースライン実行が必要である。runner はミューテーションの
  前に一度それを計測する。

## 直近の次のステップ

1. ユーザーにシャードのレポートをまとめて渡してもらう代わりに、Bazel のテストツリー内でそれらを
   マージする (シャードの出力に対する `mutrim_minimize` ルール)。
