# クイックスタート

> 何もない状態から、インターフェース上で動く DAG まで、4 つのステップで。

*https://brevis.sh/ja/docs/quickstart/ · brevis.sh docs (ja)*

---

このガイドでは、brevis.sh を起動し、ワークフローを公開して実行します。最後には、
各ステップのリアルタイムの状態が付いたグラフが表示されます。

要件: 到達可能な PostgreSQL と、`BREVIS_DATABASE_URL` 変数。

## 1. スキーマを適用する

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
brevis migrate up
```

## 2. ワークフローを書く

`hello.yaml` を作成します。この例はシェルだけを使うので、どこでも動きます。

```yaml
name: hello
type: dag
tags: [example]

steps:
  - id: prepare
    run: sh -c 'echo preparing; sleep 1'

  # The next two depend on the same step, so they run IN PARALLEL.
  - id: extract
    run: sh -c 'sleep 2; echo 42 extracted'
    depends_on: [prepare]

  - id: validate
    run: sh -c 'sleep 1; echo validated'
    depends_on: [prepare]

  - id: publish
    run: echo published
    depends_on: [extract, validate]
```

公開する前に確認します。これはデータベースに触れません。

```bash
brevis validate hello.yaml
```

```text
  ok    hello                        dag  4 steps, 4 dependencies  (manual)
```

## 3. キューを使わずに今すぐ実行する

グラフが動くのを見る最短の方法です。

```bash
brevis run hello.yaml
```

```text
workflow hello (dag, 4 steps) in .
  ▶ prepare
    prepare | preparing
  ✓ prepare
  ▶ extract
  ▶ validate
    validate | validated
  ✓ validate
    extract | 42 extracted
  ✓ extract
  ▶ publish
    publish | published
  ✓ publish

workflow hello finished
```

`extract` と `validate` は同じステップに依存しているため、同時に始まります。
ランナーはグラフを**レベルごとに**進みます。同じレベルのものはすべて並列に実行され、
次のレベルは前のレベルが完全に終わってから始まります。

:::note `run` は本質的にローカルです
`brevis run` は `BREVIS_ENV=local`(空の場合もローカルとみなされます)のときだけ動作し、
ステップを実行中のインスタンス自身のプロセスとして実行します。`image:` を宣言している
ステップも同様です。pod を作るのは `scheduler` です。
:::

## 4. 公開して運用する

ワークフローをデータベースに登録し、インターフェースに表示し、スケジュールに従って
動かすための手順です。

```bash
brevis publish hello.yaml
```

```text
  published  hello                      (manual)
```

2 つのターミナルで、インターフェースとスケジューラを起動します。

```bash
brevis serve
```

```bash
brevis scheduler --interval 5s --concurrency 4
```

`http://localhost:8080` を開き、ワークフロー一覧から `hello` を探して ▶ をクリックします。
グラフ上で、各ステップの状態がリアルタイムに変わっていきます。

## 何が起きたのか

| | |
|---|---|
| `migrate up` | テーブルを作成しました |
| `validate` | データベースに触れずに YAML を検査しました |
| `run` | キューを使わず、インスタンス自身で実行しました |
| `publish` | ワークフローとそのスケジュールを保存しました |
| `serve` | API とインターフェースを起動しました |
| `scheduler` | スケジュールを具体化し、キューを消化しました |

## 次のステップ

- [Workflows](/ja/docs/workflows/index.md) — YAML 形式の全体
- [パラメータ](/ja/docs/parameters/index.md) — 2 回の run の間で何が変わるか
- [ステップごとの pod](/ja/docs/pod-per-step/index.md) — これがどのように Kubernetes になるか
