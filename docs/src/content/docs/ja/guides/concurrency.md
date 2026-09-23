---
title: "並行性ミュータント"
description: "オプトインの concurrency オペレータと、その kill を確認する方法。"
---

オプトインの `concurrency` オペレータ (`-operators default,concurrency`) は、実際の Go の並行性
バグをその修正を反転させることで模倣する (Tu et al., "Understanding real-world concurrency
bugs in Go", ASPLOS 2019)。`defer f()` を削除するかその場で実行する、`go f()` をインラインで
実行する、送信 `ch <- v` を削除する、`make(chan T)` にバッファ 1 を与え `make(chan T, n)` から
バッファを取り除く (実行時に計算されるサイズなら 1 増やす)、select の case のチャネルを nil に
して決して発火しないようにする、`atomic.AddT(&x, d)` / `atomic.StoreT(&x, v)` を通常の
`x += d` / `x = v` にする、`once.Do(f)` を `f()` にする、である。`close`、`Lock` / `Unlock`、
WaitGroup の `Add` / `Done` / `Wait`、`cancel()` の削除は呼び出し文の削除であり、`voidcall` が
すでに行う。

これらの kill はスケジューリングに依存する。テストバイナリをレースディテクタ付きでビルドし
(`go test -c -race`、`mutation_test` の `race` 属性)、持ち込まれたデータ競合がそれに当たった
テストを失敗させるようにし、`run -confirm-kills N` で kill を確認する。デッドロックする
ミュータントはミュータントごとのタイムアウトに達し、kill として数えられる (`TIMEOUT`)。
