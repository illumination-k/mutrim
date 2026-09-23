---
title: "はじめに"
description: "ミュータントを生成し、そのひとつを go test -overlay で適用する。"
---

```bash
# List type-checked-viable mutants of a package as JSON.
go run ./cmd/mutrim gen ./path/to/pkg > mutants.json

# Select operators: names, "default", and "-name" to remove one. Every operator but
# "call" (a non-void call replaced by its result's zero value) and "concurrency" is in
# the default set.
go run ./cmd/mutrim gen -operators default,-constant ./path/to/pkg
go run ./cmd/mutrim gen -operators default,call ./path/to/pkg

# Narrow the sites: by function name, by file, or by the mutant itself.
go run ./cmd/mutrim gen -match '^\(\*Tree\)\.' -exclude-files '_gen\.go$' \
  -exclude-re 'constant: 0 -> 1' ./path/to/pkg

# Arid code (logging, sleeps, stdout writes, ...) is ignored by default; name more
# callees, or turn the built-in rules off.
go run ./cmd/mutrim gen -arid '(*Metrics).Observe' ./path/to/pkg
go run ./cmd/mutrim gen -no-arid ./path/to/pkg

# Apply one mutant through go build -overlay and run the package's tests.
go run ./cmd/mutrim overlay -id <mutant id> -o overlay.json ./path/to/pkg
go test -overlay overlay.json ./path/to/pkg
```

`go test` が失敗すれば、そのミュータントは kill されたことになる。

`-match <re>` は、`func` フィールドの `(*T).Name` 形式の名前がマッチする関数だけを残す。
`-files <glob,...>` はいずれかの glob にマッチするファイルだけを残し、`-exclude-files <re,...>`
はいずれかの正規表現にマッチするファイルを除外する (どちらも、報告されたままのパス、作業
ディレクトリからの相対パス、ベース名に対して試される)。`-exclude-re <re>` は
`func operator: description` がマッチするミュータントを除外するので、ある書き換えをどこに
現れても除外できる。

_arid_ ノード内のミュータントは `"arid"` として無視される (Petrović et al., "State of mutation
testing at Google", ICSE-SEIP 2018)。ノードは、どのテストもそれを観測しないとルールが判断
すれば arid であり、複合ノードはその子がすべて arid なら arid である。そのため
`if err != nil { log.Print(err) }` の本体を空にする変異は無視されるが、条件式のミュータントは
残る。組み込みルールが対象とするのは、`log`、`slog`、`zap`、`zerolog`、`fmt.Print*`、`testing`
ヘルパー、`prometheus` / `metrics` の `Inc` / `Add` / `Observe`、`time.Sleep` の呼び出し
(呼び出しとその引数内のすべて)、`os.Stdout` / `os.Stderr` への `fmt.Fprint*`、
`context.WithTimeout` や `time.After` などに渡す duration や deadline、`_ = x` のシンク、
マップキャッシュの参照 `if v, ok := m[k]; ok { return v }`、`panic("unreachable")`、そして
`init` と `String`、`Error`、`GoString` メソッドの本体である。`-arid <glob,...>` は呼び出し先を
追加する。呼び出し先は go/types が名付けるとおりに、パッケージパス付きとパッケージパスなしの
両方でマッチする (`slog.Info` と `log/slog.Info`、`(*slog.Logger).Info`)。`-no-arid` は組み込み
ルールを無効にし、`-arid` の呼び出し先だけを残す。

printf 系の呼び出し (`fmt.Errorf("boom")`、および最後のパラメータが `string, ...any` である
任意の関数) の書式引数は、`string` オペレータでは `"printf-format"` として無視される。
スキーマタに下ろすと定数でなくなり、`go test` と rules_go の `nogo` が実行する vet の `printf`
チェックでビルドが失敗するためである。

フィルタされたミュータントもリストには残り、`"ignored"` がそれを除外したルールを示す。`run`
はそれをビルドも実行もせずに `IGNORED` と報告する。フィルタした実行のカウントはフィルタ
しない実行と比較可能なままであり、`IGNORED` ミュータントはどのスコアにも数えられない。
