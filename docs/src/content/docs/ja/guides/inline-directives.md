---
title: "インラインディレクティブ"
description: "//mutrim: コメントで、行・範囲・関数単位でミュータントを抑制する。"
---

上記の `gen` フラグは実行全体のスイッチであり、コメントは局所的なスイッチである。
ディレクティブが抑制したサイトは、フィルタされたサイトとまったく同じように報告される。
`mutants.json` には引き続き載り、`"ignored"` がディレクティブを、`"reason"` がその文言を保持し、
`run` はそれをビルドも実行もせずに `IGNORED` と報告する。

```go
//mutrim:disable [op,...] [reason]            // until //mutrim:enable
//mutrim:disable-next-line [op,...] [reason]  // the line below
//mutrim:disable-func [op,...] [reason]       // in a function's doc comment
//mutrim:enable                               // closes every open disable
```

```go
func Retry(attempts int) error {
	//mutrim:disable-next-line relational,constant the bound is arbitrary
	for i := 0; i < 3; i++ {
	}
	return nil
}
```

オペレータのリストは省略可能で、省略時はすべてのオペレータになる (`all` と明示的にも書ける)。
オペレータ名でない最初の単語から理由が始まるので、理由を引用符で囲む必要はない。閉じられない
`//mutrim:disable` はファイルの終わりまで有効である。ディレクティブは `gen` のフィルタに優先し、
フィルタはどのディレクティブも覆わないサイトに対してのみ参照される。
