---
title: ステップ間のコンテキスト
description: あるステップが次のステップに伝えるもの — 2 つの環境変数と 1 つのファイル。
group: 基本概念
order: 7
slug: context
---

ステップが値を公開し、それに依存するステップがそれを読みます。この機能が行うのは
それだけです。

```yaml
steps:
  - id: extract
    run: python fetch.py

  - id: transform
    run: python transform.py
    depends_on: [extract]
```

```python
# extract
from brevis import context
context.set(bucket="s3://landing/2026-09-07", rows=48213)
```

```python
# transform
from brevis import context
bucket = context.get("extract.bucket")
```

キーは常に `<step id>.<name>` の形です。ステップが読めるのは、**自分の依存先**が公開したものだけで、
それ以外は読めません。

:::note これは何ではないか
メッセージキューでも、run 間の状態でも、データを置く場所でもありません。あるステップが
次のステップに残すメモ、つまりパス、ウォーターマーク、件数のようなものです。
:::

## どの言語でも、ライブラリは不要

契約は**2 つの環境変数と 1 つのファイル**なので、SDK をまったく使わないステップでも
参加できます。

```bash
window=$(jq -r '.extract.watermark' <<< "$BREVIS_INPUT")
./publish --until "$window"
jq -n --arg n "$(date -Is)" '{published_at: $n}' > "$BREVIS_OUTPUT"
```

| | |
|---|---|
| `BREVIS_INPUT` | `{"<step id>": {…}}` — このステップの依存先が公開したもの |
| `BREVIS_OUTPUT` | JSON オブジェクトを**1 つ**書き込むパス |

Kubernetes では、そのパスは `/dev/termination-log` です。エンジンは終了コードを得るために
すでにこれを読んでいます。したがって、**API 呼び出しも、トークンも、ポートも不要**で、
そのためのライブラリがやることは JSON のパースとファイルの書き込みだけです。

| 言語 | |
|---|---|
| Python | [`pip install brevis`](/docs/python/) — [自動パラメータ](/docs/parameters/)を含む `brevis.run` も付属します |
| Go | `sdk/context`。SDK モジュールの一部で、何にも依存しません |
| その他 | 上記の 2 つの変数 |

## 分離

どのステップも、他のステップが公開したものを上書きできません。キーには書き込んだステップの
id がプレフィックスとして付き、そのプレフィックスを決めるのはステップではなくエンジンです。
2 つのステップが `bucket` を公開すると `extract.bucket` と `load.bucket` になり、どちらも
消えません。

## 4096 バイトの上限

ステップが公開できるのは **4096 バイト**までです。この上限は恣意的なものではありません。
Kubernetes では転送手段が `/dev/termination-log` であり、kubelet が受け付けるのがその大きさだからです。

超過すると**ステップのエラー**になり、メッセージには何バイト書き込まれたかが示されます。
代案は切り詰めですが、切り詰められた JSON は不正な JSON です。それを発見するのは次のステップで、
そのステップには、自分に非がないことを知る手段がありません。

値が収まらないなら、それはコンテキストではありません。バケットに書き込み、そのパスを公開してください。

## リトライと再開された run

新しい試行では、ステップがあらためて公開し、前の値は置き換えられます。すでに成功したステップは
再実行も再公開もされません。そのステップが言ったことは、依存するステップにとって有効なままです。

## ステップを手動で実行する

2 つの変数が契約のすべてなので、run の再現とはそれらをエクスポートすることです。

```bash
export BREVIS_INPUT='{"extract":{"bucket":"s3://landing/2026-09-07"}}'
export BREVIS_OUTPUT=/tmp/out.json
python transform.py
cat /tmp/out.json
```

## 次のステップ

- [パラメータ](/docs/parameters/) — run の間で変わるものと、自動パラメータ
- [ステップのランタイム](/docs/runtime/) — ステップが実行場所について宣言するもの
- [Python](/docs/python/) — クライアントライブラリと、その他の言語
