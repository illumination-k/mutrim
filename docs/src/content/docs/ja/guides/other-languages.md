---
title: "他の言語"
description: "Stryker、cargo-mutants、テストごとのカバレッジから TypeScript や Rust のテストスイートを最小化する。"
---

最小化（`mutrim minimize`）に必要なのは、テスト × 要件の行列の各行、つまり各テストがどのコードに到達し、
どのミュータントを殺すかだけである。`mutrim import` は他の言語のツールの出力からその行を
**observations** ファイルに読み込み、`minimize` は Go の `report.json` と並べて（あるいは代わりに）
observations ファイルを受け取る。mutrim 自身はそれらのツールを実行しない。

| ツール                                     | `mutrim import` | 得られる行                               |
| ------------------------------------------ | --------------- | ---------------------------------------- |
| StrykerJS (mutation-testing-elements)      | `stryker`       | 到達したサイト、殺したミュータント       |
| cargo-mutants (`mutants.out`)              | `cargo-mutants` | 殺したミュータント、nextest なら実行時間 |
| vitest / jest のカバレッジ (Istanbul JSON) | `istanbul`      | 1 テストが実行した文                     |
| llvm-cov の JSON export                    | `llvm-cov`      | 1 テストが実行した行                     |
| JUnit XML (vitest, nextest)                | `junit`         | 実行時間                                 |

1 つのスイートのファイルはテスト名で統合されるので、どのアダプタもテストを同じ規則で命名する。

- TypeScript: `<テストファイル>#<describe とテスト名を空白で連結>`。StrykerJS の命名と同じ
  (`src/calc.test.ts#clamp low`)。
- Rust: `<crate>::<テストのパス>`。crate はテストバイナリのもので、単体テストならライブラリ、
  結合テストならファイル名 (`rsdemo::tests::low`、`sign::pos`)。

要件のラベルはソース範囲 (`src/calc.ts:2:7-2:13`)、行 (`src/lib.rs:2`) かミュータント
(`src/lib.rs:2:10: replace < with == in clamp`) なので、2 つのファイルの行が同じラベルを持てば同じ要件になる。

## TypeScript: vitest + StrykerJS

Stryker には、各ミュータントに到達したテストと殺したテストをすべて記録させる必要がある。
テストごとのカバレッジを有効にし、最初の kill で打ち切らないようにする (既定では殺したテストが 1 つしか
記録されず、全テストが essential に見える)。

```json
{
  "testRunner": "vitest",
  "coverageAnalysis": "perTest",
  "disableBail": true,
  "reporters": ["json"]
}
```

```sh
npx stryker run
mkdir -p obs
mutrim import stryker -o obs/stryker.json reports/mutation/mutation.json

# 実行時間 (任意): vitest の JUnit レポート。
npx vitest run --reporter=junit --outputFile=junit.xml
mutrim import junit -runner vitest -o obs/junit.json junit.xml
```

これだけで最小化できる。Stryker の `coveredBy` がサイトカバレッジになるからである。ミュータントのない
コードまで見るブロックカバレッジには、テストごとに 1 回のカバレッジ実行が必要である。

```sh
jq -r '.tests[].name' obs/stryker.json | while IFS= read -r t; do
  file=${t%%#*} name=${t#*#}
  pattern="^$(printf '%s' "$name" | sed 's/[][\.*^$(){}?+|/]/\\&/g')\$"
  npx vitest run --coverage.enabled --coverage.reporter=json -t "$pattern" "$file"
  mutrim import istanbul -test "$t" -o "obs/cov-$(printf '%s' "$t" | md5sum | cut -c1-8).json" \
    coverage/coverage-final.json
done

mutrim minimize obs/*.json
```

`-root` (既定はカレントディレクトリ) はカバレッジのパスを相対パスにし、その外のファイルを除外する。
`-exclude-files` は正規表現でさらに除外する。テストファイルは vitest 自身がカバレッジから外す。

Stryker の vitest ランナーはテストを空白連結の名前で選ぶ。名前の照合が異なる vitest のバージョンでは、
`describe` 内のテストがミュータントに到達するのに何も殺さないと報告される。最小化の前にレポートの
`killedBy` を確認すること。

## Rust: cargo-mutants + cargo llvm-cov

cargo-mutants はミュータントごとにスイート全体を実行し、失敗したかどうかしか記録しない。
`import cargo-mutants` は失敗したテストを各ミュータントのログから読み戻す。テストバイナリが 1 つ失敗すると
`cargo test` は後続のバイナリを実行しないので、`--no-fail-fast` を渡す。

```sh
cargo mutants --cargo-test-arg=--no-fail-fast
# 実行時間も取るなら: cargo mutants --test-tool=nextest --cargo-test-arg=--no-fail-fast
mkdir -p obs
mutrim import cargo-mutants -o obs/mutants.json mutants.out
```

caught なのにログに失敗テストが見つからないミュータント (クラッシュ、doctest ハーネスの失敗) は stderr に
報告され、何も殺さない扱いになる。タイムアウトと unviable は除外し、missed は生存として数える。
cargo-mutants は各ミュータントの関数名を記録しているので、生存ミュータントは Go と同じく `minimize` の
`weak_spots` にも出る。

テストごとのカバレッジ: テストバイナリを `-C instrument-coverage` 付きで (専用の target ディレクトリに)
一度だけビルドし、各テストを単独で実行して、`llvm-tools-preview` コンポーネントの `llvm-profdata` /
`llvm-cov` でプロファイルを export する。テストごとに `cargo llvm-cov` を呼んでもよいが、毎回ビルドと
レポート生成が走るので数倍遅い。

```sh
pkg=rsdemo
export CARGO_TARGET_DIR=target/cov RUSTFLAGS="-C instrument-coverage" LLVM_PROFILE_FILE=/dev/null
cargo test -p "$pkg" --no-run --message-format=json |
  jq -r 'select(.executable != null and .profile.test) | "\(.target.name | gsub("-"; "_")) \(.executable)"' >bins.txt
llvm=$(rustc --print sysroot)/lib/rustlib/$(rustc -vV | sed -n 's/^host: //p')/bin
jq -r '.tests[].name' obs/mutants.json | while IFS= read -r t; do
  crate=${t%%::*} path=${t#*::}
  exe=$(awk -v c="$crate" '$1 == c { print $2 }' bins.txt)
  LLVM_PROFILE_FILE=cov.profraw "$exe" --exact "$path" --quiet >/dev/null
  "$llvm/llvm-profdata" merge -sparse cov.profraw -o cov.profdata
  "$llvm/llvm-cov" export -format=text -instr-profile cov.profdata "$exe" >cov.json
  mutrim import llvm-cov -test "$t" -o "obs/cov-$(printf '%s' "$t" | md5sum | cut -c1-8).json" cov.json
done

mutrim minimize obs/*.json
```

テスト関数自身の本体はそのテストだけが実行するので、残すと全テストが essential になる。そのため
`import llvm-cov` はテストコードを除外する。`tests/`・`benches/`・`examples/` 以下のファイル
(`-exclude-files`) と、デマングルしたパスに `tests`・`test_support`・`test_utils` モジュールを含む関数
(`-exclude-fn`) である。プロジェクトの構成に合わせて調整すること。ワークスペースでは
`-files '^crates/rsdemo/src/'` で対象パッケージだけを残す。依存先のコードはそれ自身のテストが
カバーすべきもので、残すとそこに到達するだけでテストが essential になる。

ブロックは行 (`src/lib.rs:2`) で、実行された各領域をその先頭行で数える。llvm-cov は `&&` の各オペランドや
呼び出しごとに 1 行を複数の領域に分けるので、領域のままだと演算子の数で行に重みが付く。`-regions` を付けると
領域 (`src/lib.rs:2:8-2:14`) のまま残す。

## minimize の出力

選択、冗長なテストとそれを包含するテスト、essential なテスト、dominator の集計は Go の場合とまったく同じように
計算される。`-keep` (行名に対する正規表現) でテストを保護できる。`-tag`、`-mutants`、
`-blocks` (到達されない関数) は Go のソースと成果物を読むので、ここでは読むものがない。取り込んだ実行の
weak spots は cargo-mutants の出力だけから出る。重み
(`-w-site`、`-w-block`、`-w-kill`) は取り込んだサイト、ブロック、kill にそのまま適用される。
