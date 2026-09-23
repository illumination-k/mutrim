---
title: "実行を diff に絞る"
description: "プルリクエストに関係するミュータントだけを実行する。"
---

`run -in-diff` は unified diff に関係するミュータントだけを実行する。これはプルリクエストに
必要なものである:

```bash
git diff --unified=0 --merge-base origin/main > pr.diff
go run ./cmd/mutrim run -test-bin pkg.test -mutants mutants.json -in-diff pr.diff -out report.json
```

対象範囲はコミットに関係するミュータントであり、テストごとのトレースを通じて求める。diff が追加した
行上のミュータント (`report.json` の `changed: true`)、そのいずれかに到達するテスト (変更を実行する
テスト)、そしてそれらのテストが到達するすべてのミュータントである。ミュータントはパッケージのどの
ファイルにあってもよい。コミットに関係するミュータントの大半は変更行の外にあるため (Ojdanić et al.,
TOSEM 2023)、行だけではそれらを取りこぼす。`-diff-expand=false` は行だけに絞る。

それ以外のミュータントはすべて `SKIPPED` と報告される。実行されず、どのスコアにも算入されない。

diff は新しい側だけを読む。削除された行はミュータントを持たず、コンテキスト行は diff が変更して
いないコードなので、ミュータントはその範囲が追加行と重なるときに残される。ミュータントが存在する
ことはないので、`_test.go` ファイルはスキップされる。diff のパスはミュータントのファイルとパスの
接尾辞で照合されるので、リポジトリ相対の diff は `go list` 実行の絶対パスにも、Bazel 実行の
execroot 相対パスにも一致する。

`gen` は変わらない。ミュータント ID はコンテンツハッシュなので、`mutants.json` はどちらでも同じで
あり、範囲を絞った実行は完全な実行と比較可能なままである。diff はテストバイナリのビルド元の
ソースに対して取る必要がある。mutrim はそれを検証せず、別のツリーの diff は単に誤った行を選ぶ。

Bazel では diff をパスで渡すので、ランナーはハーメティックなままである:

```bash
git diff --unified=0 --merge-base origin/main > "$PWD/pr.diff"
bazel test --test_env=MUTRIM_IN_DIFF="$PWD/pr.diff" //...:all
# the changed lines alone
bazel test --test_env=MUTRIM_IN_DIFF="$PWD/pr.diff" --test_arg=-diff-expand=false //...:all
```
