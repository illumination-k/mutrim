---
title: "パッケージをまたぐ kill"
description: "変異させたパッケージを import するパッケージのテストによる kill も数える。"
---

パッケージを検査するのはそのパッケージ自身のテストだけではない。下流パッケージのテストしか
捕まえられないミュータントは、そのパッケージのテストだけを実行すると `LIVED` か `NO_COVERAGE`
になる。`run -extra-test pkg=bin[,dir]` (繰り返し指定可) は、変異させたパッケージを import する
パッケージ `pkg` のテストバイナリを、同じ overlay でビルドしたものとして追加する。そのテストは
パッケージ自身のテストと同様にトレースされ、ミュータントに対して実行され、`tests`、`killed_by`、
`suspicious_by` では `pkg.TestX` という名前になり、`tests[].pkg` が設定される。パッケージ自身の
テストは修飾なしの名前のままである。

```bash
go test -c -overlay out/overlay.json -o app.test ./path/to/app
go run ./cmd/mutrim run -test-bin pkg.test -dir ./path/to/pkg \
  -extra-test example.com/app=app.test,./path/to/app -mutants mutants.json -out report.json
```
