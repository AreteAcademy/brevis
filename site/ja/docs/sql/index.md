# SQL

> 通常の .sql ファイルを、依存関係の順にテーブルとビューへ変換します — ワークフローのステップとして、または単独で。

*https://brevis.sh/ja/docs/sql/ · brevis.sh docs (ja)*

---

テンプレートはありません。モデルは有効な SQL のままのファイルであり、順序は SQL 自体を読むことで決まります。Postgres と BigQuery を、同じ適合性スイートの下でサポートします。

![SQL のフロー: brevis-sql は raw.orders(SDK または Gateway が着地させたテーブル)を読みます。モデル staging.stg_orders はその上のビューになり、marts.orders はそのビューの上のテーブルになります。順序は SQL を読むことで決まります。同じモデルが Postgres でも BigQuery でもビルドされます。](/assets/flow-sql.svg)

## 6 つのコマンド

| コマンド | 内容 |
|---|---|
| `brevis-sql compile` | すべてのモデルをパースし、すべてのエッジを解決します。**何にも接続しません** |
| `brevis-sql graph` | 推論されたエッジを出力します。間違ったエッジは、後で発見されるのではなく、目に見えます |
| `brevis-sql build` | すべてのモデルを、依存関係の順に作成または置換します |
| `brevis-sql test` | すべてのモデルのテストを実行します。各テストは、何も見つからないはずの SELECT です |
| `brevis-sql serve` | 読み取り専用のプレビューとクエリに HTTP で応答するため、Console にウェアハウスの認証情報は不要です |
| `brevis-sql generation bump` | テーブルが再構築されたことを記録し、`brevis-sql-api` のそのテーブルに対するキャッシュ済みの回答を古くします — `build --generations-from` は、ビルドするすべてのモデルについてこれを行います |

`--select orders+` を付けると、どのコマンドも、1 つのモデルとその下流のすべてに絞り込めます。


## `brevis-sql serve` — 認証情報なしでウェアハウスを読む

Console の Data と SQL の画面は、何が着地したか、その中身は何かを表示します。どちらもウェアハウスには届きません。
隣で動き、エンジンが意図的に持たない認証情報を保持する、このサービスに問い合わせます。

```bash
brevis-sql serve --connections brevis.yaml --addr 127.0.0.1:8088
```

上限は予算ではありません。`--max-bytes` は 1 つのクエリを制限し、`--budget` は接続ごとの 1 時間あたりの合計を
制限します。見積もりに対して確認されるので、拒否は費用が発生する前に行われます。制限できるのはバイト数を
報告するウェアハウス、つまり BigQuery であって Postgres ではありません。起動時のバナーは、どの姿勢が
有効か(何も無い場合も含めて)を示します。

**ファイルが宣言したものだけに到達でき**、デフォルトでは、それが行うことに書き込みはありません。すべての
文は実行前に分類され、`--max-bytes` を超えてスキャンする読み取りは、費用が発生する前に見積もられて拒否され、
ウェアハウス自身にも書き込みの拒否が求められます。監査ログの行には文のハッシュが含まれ、文そのものは
含まれません。

**専用のサービスアカウントはもう不要です。** `brevis-sql build` はテーブルを作成するため、そのロールは書き込みが
でき、そうしたロールを拒否するサービスは、作成、保管、ローテーションする必要のある 2 つ目のアカウントを
意味していました。ロールが防御線を担う唯一のものではなくなりました。Postgres は各文を `READ ONLY` で開いた
トランザクション内で実行し、分類器には見えない書き込みである `SELECT nextval('s')` は Postgres 自身に
拒否されます。BigQuery は、見積もりを行うドライランで文の種類を報告するので、`CREATE TABLE` や
`EXPORT DATA` はウェアハウス自身の判断で拒否されます。どちらも提供できないウェアハウスでも、書き込める
認証情報は引き続き拒否されます。

`BREVIS_ENV=local` 以外では、トークンが必須で、なければサービスは起動しません。

### 誰かが変更してよい開発用ウェアハウス

```bash
BREVIS_SQL_SERVE_WRITES=warehouse_dev
```

これは**接続に名前を付けます**。1 つのサービスが複数のウェアハウスに到達でき、開発に使う 1 つを
解放しても、隣の 1 つが解放されてはなりません。名前を付けた接続では、すべての層が引き下がり、
このサービスはその認証情報と同じだけの力を持ちます。行数の上限は画面を制限し、バイト数の上限は
お金を制限しますが、どちらも文が変更するものを制限しません。

これを設定する場所ではトークンが必須で、`BREVIS_ENV=local` も例外ではありません。起動時のバナーは
解放されたすべての接続を示し、Console はエディタの上にバナーを描画し、`brevis_sql_writable_connection` が
メトリクスに現れます。**本番では未設定にしてください。** このリポジトリのどのマニフェストも、これを設定して
いません。

トークンには**名前を付けられます**。`BREVIS_SQL_SERVE_TOKEN_CONSOLE=…` は `console` という呼び出し元を宣言し、
その bearer が生成するすべての監査ログの行に `"caller":"console"` が含まれます。識別は、呼び出し元が埋める
ヘッダーではなく認証情報そのものなので、ログは、そのトークンの保有者が持っていなかったことを語りません。
拒否されたトークンも行を残しますが、提示されたものは記録しません。

## モデルは有効な SQL のままのファイル

設定は先頭のブロックコメントなので、ファイルはハイライト付きでエディタで開け、Console で手動で実行でき、
`psql -f` でも実行できます。

```sql
/* brevis
materialized: table
tests:
  - not_null: [order_id]
  - unique: [order_id]
  - relationships: {column: customer_id, to: staging.stg_customers, field: customer_id}
*/
select * from staging.stg_orders
```

**テンプレートはありません。** `models/<schema>/<name>.sql` は `<schema>.<name>` であり、依存関係は、
自分で書く必要のある関数呼び出しではなく、SQL を読むことから得られます。`brevis-sql graph` は推論された
ものを出力します。ヘッダーの `depends_on:` は、まれな取りこぼしに対する明示的な上書きです。

## compile から test まで

2 つのモデルがあります。`raw.orders` をクレンジングするビューと、それに依存するテーブルです。

`models/staging/stg_orders.sql`:

```sql
/* brevis
materialized: view
*/
select order_id, customer_id, amount, ordered_at
from raw.orders
where amount is not null
```

`models/marts/orders.sql`:

```sql
/* brevis
materialized: table
tests:
  - not_null: [customer_id]
  - unique: [customer_id]
*/
select customer_id, count(*) as orders, sum(amount) as revenue
from staging.stg_orders
group by customer_id
```

`compile` と `graph` は何にも接続しません。

```bash
docker run --rm -v ./project:/project:ro areteacademy/brevis-sql:0.6.0 \
  compile --project /project
```

```text
2 models, 2 to build, 0 function(s)
  staging.stg_orders                 view  0 test(s)
  marts.orders                       table 2 test(s)
```

```bash
docker run --rm -v ./project:/project:ro areteacademy/brevis-sql:0.6.0 \
  graph --project /project
```

```text
staging.stg_orders
  └─ raw.orders (source)
marts.orders
  ├─ staging.stg_orders
```

Postgres に対する `build` と `test`:

```bash
docker run --rm -v ./project:/project:ro \
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db areteacademy/brevis-sql:0.6.0 \
  build --project /project --dsn-from BREVIS_SQL_DSN
```

```text
  staging.stg_orders                 view  new   4ms
@brevis:{"type":"landed","target":"postgres://postgres/staging/stg_orders"}
  marts.orders                       table new   4ms
@brevis:{"type":"landed","target":"postgres://postgres/marts/orders","rows":2}
2 models built on postgres
```

```bash
docker run --rm -v ./project:/project:ro \
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db areteacademy/brevis-sql:0.6.0 \
  test --project /project --dsn-from BREVIS_SQL_DSN
```

```text
2 tests passed on postgres
```

公開済みのイメージで得た実際の出力です。compile と graph は何にも接続しません。Postgres に対しては、build と test が「2 models built」と「2 tests passed」で終わります。

### テストが失敗したとき

`raw.orders` に顧客のいない注文があると、`build` は通りますが `not_null` テストは通りません。失敗は、
モデル、列、行数、そして**クエリ**を示します。これはコンソールに貼り付けて、絞り込んでいくためのものです。

```text
FAIL  marts.orders                   not_null         customer_id          1 row(s)
      SELECT COUNT(*) FROM (SELECT customer_id FROM marts.orders WHERE customer_id IS NULL) AS v
brevis-sql: 1 of 2 tests failed
```

プロセスは終了コード 1 で終了し、ワークフローのステップもそれとともに失敗します。

## インクリメンタルモデルとその lookback

`materialized: incremental` には `unique_key` と `watermark` が必要です。各 build は、
ターゲットに既にある最大の watermark を超える行だけを読み、キーで MERGE します。
このフィルターは厳密なので、watermark と同値でも build の後にコミットされた行や、
古いタイムスタンプで遅れて届いた行は、`--full-refresh` するまで見えません。
任意の `lookback` がこれを解決します。

```sql
/* brevis
materialized: incremental
unique_key: [order_id]
watermark: updated_at
lookback: 2h
*/
```

値は整数に `s`、`m`、`h`、`d` のいずれかを付けたものです。それ以外はモデル名付きで
拒否されます。フィルターは `updated_at >= max - 2h` になり、重複した行は MERGE が
吸収します。lookback を指定する場合、watermark は TIMESTAMP 列でなければなりません。
`lookback` がなければ SQL は変わりません。

## テストは、違反する行を返す SELECT

`not_null`、`unique`、`accepted_values`、`relationships`。ゼロ行ならパスで、解釈すべきことはほかにありません。

## 書き込んだものを自分で伝える

マテリアライズされたすべてのモデルは、Brevis エンジンがすでに読んでいる `@brevis:` の行を出力するため、
`/data` に他のすべてと並んで表示されます(上の `build` の出力を参照)。ビューは行数をゼロではなく
報告しません。ビューは行を持たず、そこでのゼロは、本当に空のテーブルと区別がつかないからです。

## 2 つのウェアハウス、1 つのスイート

PostgreSQL と BigQuery(`--dialect bigquery`)で、どちらも同じテストスイートを実行します。実装が
1 つしかなかった間に書かれたもので、2 つ目が黙って乖離することはありません。

BigQuery は Application Default Credentials で認証します。ワークロード ID では、それは pod 自身の
ID であり、`--dsn-from` はプロジェクト id を保持する変数を指定します。

## 13 MB

distroless、nonroot、シェルなし。公式の BigQuery クライアントは、空のバイナリで 31 MB、526 パッケージ
なので、代わりに REST エンドポイントを通じて BigQuery と通信します。約 200 行で、黙って間違いうる
あらゆる形に対して 1 つずつテストがあります。リポジトリのゲートがそのクライアントを名前で拒否するので、
この判断は、通常の依存関係のバージョンアップを越えて保たれます。

## dbt との照合

jaffle-shop を、dbt が使ったのと同じシードデータに対して、このイメージでビルドしました。

|  | 結果 |
|---|---|
| モデルごとの、行と各行の md5 | **一致、13 個中 13 個** |
| モデルごとの、列と型 | **一致、13 個中 13 個** |
| 13 モデル、初回ビルド | 1.56 秒 |

## やらないこと

**dbt との互換性はありません**。これは欠陥ではなく判断です。実際の dbt プロジェクトのモデルの約 3 分の 1 は、
`ref`、`source`、`config` 以上のものを使いません。残りはマクロを使い、パッケージは設計上テンプレートです。
誰にも予測できない半端な互換性は、ないよりも悪いものです。

## 次に進む先

| 目的 | 参照先 |
|---|---|
| 4 つの構成要素の中での SQL の位置づけを見る | [エコシステム](/ja/docs/ecosystem/index.md) |
| brevis-sql をスケジュールされたステップとして実行する | [Core](/ja/docs/core/index.md) |
| モデリング対象のデータを着地させる | [Go SDK](/ja/docs/sdk/index.md)または[取り込み](/ja/docs/ingestion/index.md) |
