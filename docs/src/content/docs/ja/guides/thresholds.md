---
title: "しきい値"
description: "ミューテーションスコアが低すぎる実行を失敗させる。"
---

デフォルトでは `run` はスコアにかかわらず成功する。`run -threshold 0.8` は `score` が 0.8 未満のとき、
`-threshold-covered 0.9` は `covered_score` が 0.9 未満のときに実行を失敗させる (PIT の
`mutationThreshold` / `testStrengthThreshold`、Stryker の `thresholds.break`)。いずれも
`report.json` を書き出した後にだけ失敗し、採点対象のない実行は成功する。`mutation_test` の
`threshold` 属性と `threshold_covered` 属性はこれらをそのまま渡し、ターゲットをゲートにする。
その他の出力も書き出される。シャーディング時は各シャードが自身のミュータントを検査する。
パッケージ全体のスコアにはシャードのレポートのマージが必要である。
