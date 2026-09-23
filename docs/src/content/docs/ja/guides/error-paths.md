---
title: "エラーパスのミュータント"
description: "errpath 演算子とクラスごとのスコア。"
---

エラー処理のコードは最もテストされていない (Lima et al., "Assessing exception handling testing
practices in open-source libraries", 2021)。デフォルトで有効な `errpath` 演算子はこれを変異させる。
`fmt.Errorf("...: %w", err)` はラップをやめ (`%w` -> `%v`)、`%w` 単独なら `err` そのものを返す。
`errors.Is` / `errors.As` は `true` と `false` に固定される。`panic(x)` 文は、関数が終端文として
必要としない限り削除される。`recover()` は panic を止めたまま `nil` を返す。`nil` に置き換えた
`return` の結果 (`return x, err` -> `return x, nil`、nil のポインタ、map、slice、func) と、強制された
nil チェック (`if err != nil` -> `if true` / `if false`) は `return` 演算子と `condition` 演算子の
ミュータントであり、同じクラスが付く。

すべてのミュータントは `mutants.json` と `report.json` で `class` を持つ。値は `errpath`、
`concurrency`、`default` のいずれかである。`totals.classes` は全体の `score` と並べて各クラスを
個別に採点するので、「エラーパス: 40% kill」が見える:

```json
"classes": {
  "default": { "mutants": 120, "killed": 96, "survived": 14, "score": 0.87 },
  "errpath": { "mutants": 25, "killed": 8, "survived": 12, "score": 0.4 }
}
```

`totals.covered_score` はカバーされたミュータントだけのスコアで、`NO_COVERAGE` を除く
(PIT の test strength)。テストが到達したコードをどれだけよく検査しているかを表す。
