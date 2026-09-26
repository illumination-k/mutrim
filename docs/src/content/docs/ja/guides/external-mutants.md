---
title: "External mutants"
description: "LLM が提案した、またはバグ修正から採掘したミュータントを取り込み、生存ミュータントを書き出す。"
---

ルールベースのオペレーターがデフォルトのままである。外部ミュータントはオプトインで、
mutrim はそれを読み、型検査し、自前のものと同様に実行し、生き残ったものを書き出すが、
モデルを自ら呼ぶことはない。入出力はどちらも素の JSON なので、向こう側にはどんなループ
(LLM、プロジェクトのバグ修正コミットを採掘するスクリプト) でも置ける。

LLM のミュータントはルールベースのものより多くの実バグを見つける (Wang et al., TOSEM
2026: 851 件のバグで検出率 88% 対 42%) が、コンパイルできる割合は低く重複も多い。
プロジェクト自身の修正から採掘したオペレーターは古典的なものが捕まえられないバグを捕まえる
(Brown et al., FSE 2017)。mutrim の型検査とコンテントハッシュ ID が、こうした提案を
使えるものにする。

## 入力: `gen -extra`

```json
[
  {
    "file": "calc.go",
    "func": "Clamp",
    "line": 11,
    "original": "hi",
    "replacement": "hi - 1",
    "description": "off-by-one upper bound"
  },
  {
    "file": "calc.go",
    "func": "Words",
    "original": "n++",
    "replacement": "n += 2"
  }
]
```

```sh
mutrim gen -extra extra.json -schemata out -o mutants.json ./calc
mutrim test -extra extra.json ./...   # ワンショットドライバーも受け付ける
```

- 箇所はソーステキストで探す。`original` を `file` (パスまたはその末尾部分) のすべての式・
  文と、空白を無視して比較する。`func`・`line`・`col` で絞り込める。複数の箇所に一致する
  `original` はそれらを求めるエラーになる。
- `original` と `replacement` が両方とも式としてパースできれば式を置き換える。そうでなければ
  `original` は 1 つの文で、`replacement` は 0 個以上の文 (空なら削除) である。
- 各エントリはオペレーター `extra` のミュータントになり、固有のコンテントハッシュ ID を持つ。
  書き換えを適用したままパッケージ全体を再検査するので、未使用変数や型の不一致も捕まる。
  失敗したエントリは `viable: false` で、エラーは `reason` に入る。単一の箇所を指さない
  エントリは stderr に報告されてスキップされ、致命的にはならない。
- 生成済みミュータント (または先のエントリ) が既に行う書き換えは `duplicate` として無視され、
  `reason` がそのミュータントを示す。何も変えないものは `equivalent: "identical"` になる。
- インラインディレクティブ (`//mutrim:disable extra`) と gen のフィルタは適用されるが、
  arid ルールは適用されない。提案は意図的なものだからである。
- スキーマタは式を `func() T { if mut.Active(id) { return repl }; return orig }()`、文を
  `if mut.Active(id) { repl } else { orig }` として埋め込むので、片側しか実行されない。
  アドレス可能または定数でなければならない式、ファイルが型名を書けない式、宣言やラベルを
  持つ文は viable でない。

## 出力: `export-survivors`

```sh
mutrim export-survivors -mutants mutants.json -srcs ./calc -o survivors.json report.json
```

LIVED・NO_COVERAGE・SUSPECT_EQUIVALENT の各ミュータントに、unified diff、関数のソース、
それに到達するテスト (トレースから) とそのソースを付ける。キルするテストを書く (Foster et
al., FSE 2025: Meta で生成テストの 73% が採用された) か、等価性を判定する (Tian et al.,
ISSTA 2024) ループに必要なものである。`-srcs` はソースとその `_test.go` を含むファイルや
ディレクトリを指す。
