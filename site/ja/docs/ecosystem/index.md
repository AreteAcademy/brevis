# エコシステム

> brevis.sh の 4 つの構成要素と、それぞれの役割、そしてデータが通る場所。

*https://brevis.sh/ja/docs/ecosystem/ · brevis.sh docs (ja)*

---

Core はスケジューリング、キューイング、監視を担い、データには一切触れません。データに触れるのは、取得する SDK、受け取る Gateway、そしてデータが着地した後にモデリングする SQL です。

## 4 つの構成要素

| 構成要素 | 役割 | 入手方法 | ガイド |
|---|---|---|---|
| **Core** | スケジュールし、キューに積み、ステップごとに 1 つの pod を起動します。データには一切触れません。 | `areteacademy/brevis` | [Core](/ja/docs/core/index.md) |
| **SDK** | ステップの中で抽出、整形、ロードを行う Go ライブラリです。 | `go get github.com/AreteAcademy/brevis/sdk` | [Go SDK](/ja/docs/sdk/index.md) |
| **Gateway** | HTTP POST を受け取り、202 を返し、バッチで書き込みます。専用のデータベースは持ちません。 | `areteacademy/brevis-gateway` | [取り込み](/ja/docs/ingestion/index.md) |
| **SQL** | 通常の .sql ファイルを、依存関係の順にテーブルとビューへ変換します。 | `areteacademy/brevis-sql` | [SQL](/ja/docs/sql/index.md) |

## コントロールプレーンとデータプレーン

**コントロールプレーン**は Core です。各ステップをいつ実行するかを決め、
キューと状態を Postgres に保持し、ステップごとに 1 つの pod を起動します。
データがここを通ることはありません。

**データプレーン**は残りの 3 つの構成要素です。データは API、データベース、
ファイルから届き、2 通りのどちらかで宛先に到達します。1 つは、あなたのステップの中で
**SDK が取得する**方法、もう 1 つは Webhook やアプリから HTTP で
**Gateway にプッシュされる**方法です。宛先はテーブル、トピック、
またはバケット内のファイルです。データが着地した後は、**SQL** が着地先の
データベース内でそのままテーブルをモデリングします。

### 周辺の構成要素

補助的な 3 つの構成要素があります。

- **[Console](/ja/docs/console/index.md)** — Core の上に載る Web インターフェース
- **[agent](/ja/docs/pod-per-step/#各ステップが動く場所)** — 既存のクラスタでステップを実行します
- **[Python 用の `brevis`](/ja/docs/python/index.md)** — Python ステップ向けの実行コンテキスト

## 1 つの構成要素だけを使う

使えます。各構成要素は独自のモジュール、バージョン、イメージを持っています。SDK は任意のプログラムで動く Go ライブラリで、Gateway は YAML ファイルだけでデータベースなしに単独で起動し、brevis-sql はコンテナとして動きます。Core は必要なときに他の構成要素をオーケストレーションしますが、どれの前提条件でもありません。

## 同じ行

SDK が取得した注文と Gateway に届いた注文は、同じ brevis_ingestion_id を持つ同じテーブルに着地します。両者の選択はデプロイの判断であり、スキーマの判断ではなくなります。

SDK では `sdk.Landing` を使います(詳細は[ランディングレイアウト](/ja/docs/sdk/#ランディングレイアウト)):

```go
sdk.Execute(ctx, &sdk.Pipeline{
	Source:    sdk.Source{From: from.Files{Path: "order.json"}},
	Transform: []sdk.Transformer{sdk.Landing("app_orders", sdk.LandingKey("id"))},
	Target: sdk.Target{
		To:     postgres.Table{DSN: dsn, Name: "app_orders"},
		Schema: sdk.LandingSchema(sdk.LandingOptions{}),
	},
}, nil)
```

Gateway では `/v1/tables` への POST を使います(詳細は[1 つのルート、N 個のテーブル](/ja/docs/ingestion/#1-つのルートn-個のテーブル)):

```bash
curl -X POST localhost:8080/v1/tables -H 'Authorization: Bearer dev-key' \
    -d '{"table_name":"app_orders","unique_key":"id",
         "data":{"id":"A-1","customer":"acme.example.com","total":150}}'
```

レスポンス:

```text
{"accepted":1,"rejected":null}  202
```

`app_orders` に着地した内容:

| 列 | SDK 経由 | Gateway 経由 |
|---|---|---|
| `brevis_ingestion_id` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` |
| `brevis_record_key` | `A-1` | `A-1` |
| `brevis_operation` | `INSERT` | `INSERT` |
| `brevis_gateway` | `NULL` | `acme_gateway` |
| `data` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` |

実際の出力です。公開済みのイメージと SDK を使い、同じローカルの Postgres に対して両方の経路を実行しました。違いは誰が書き込んだかだけで、SDK の行では brevis_gateway は空になります。
