---
title: Go SDK
description: フェッチャーの書き方 — ソースから抽出し、変換し、宛先にロードします。
group: SDK とライブラリ
order: 12
slug: sdk
---

ステップの仕事が**データを取得してどこかにロードする**ことなら、Go SDK を使えば、フェッチャー全体を
数行で書けます。エンジンとは別のモジュールで、バージョンも独立しています。

```bash
go get github.com/AreteAcademy/brevis/sdk@latest
```

Go 1.23 以降が必要です。SDK は行を `iter.Seq2` としてストリーミングします。

:::warning `v0.1.0` は使わないでください
存在しないリビジョンを固定した `go.mod` を含んだまま公開されており、Go モジュールプロキシは
不変です。`v0.1.1` から始めてください。
:::

## 3 つのステップ

```go
import (
	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to/bigquery"
)

rows, err := sdk.Extract(ctx, sdk.Source{
	From: from.HTTP{URL: "https://api.example.com/v1/events"},
})

rows = sdk.Transform(rows, sdk.Accept("id", "created_at", "amount"))

res, err := sdk.Load(ctx, rows, sdk.Target{
	To:      bigquery.Table{Dataset: "bronze", Name: "events"},
	Columns: []string{"id", "created_at", "amount"},
})
```

`Extract` が読み、`Transform` が整形し、`Load` が書き込みます。それぞれがシーケンスを受け取って
シーケンスを返すので、メモリ上にすべてが一度に展開されることはありません。

![SDK のフロー: sdk.Extract が from.HTTP で HTTP API から読み取り、sdk.Transform が受け入れたフィールドだけを残し、sdk.Load が行を BigQuery のテーブルに書き込みます。3 つの呼び出しは、あなた自身のステップの中で動きます。](/assets/flow-sdk.svg)

## ドライバは設定ではなく値

`from.HTTP` は、HTTP ソースが必要とするすべてを持ちます。URL、ヘッダー、リトライ、ページネーション、
そしてレスポンスが何を意味するかです。`from.Files` はパスとフォーマットを持ちます。どちらも相手の
フィールドのために場所を空ける必要がないので、各ドライバが 6 個しか読まない 40 個のオプションを
集めたソース構造体は存在しません。

これは**何をコンパイルするか**も決めます。Go は、使ったフィールドではなく、インポートした
パッケージ単位で依存関係を絞り込みます。

| インポートするもの | パッケージ数 | AWS | Google |
|---|---|---|---|
| `sdk` | 190 | なし | なし |
| `sdk` + `from` | 194 | なし | なし |
| `sdk` + `from` + `to`(ファイル) | 195 | なし | なし |
| `sdk` + `to/bigquery` | 456 | なし | **あり** |
| `sdk` + `from` + `store/s3` | 265 | **あり** | なし |

読み書きを含むファイルパイプライン全体が、195 パッケージで、クラウド SDK は 1 つも要りません。
**ベンダー SDK が背後にあるドライバは、独自のパッケージにあります。** BigQuery が `to/bigquery`、
オブジェクトストアが `store/s3` と `store/gcs` なのはそのためです。

## ソースと宛先

| ソース | パッケージ |
|---|---|
| HTTP | `from.HTTP` |
| ファイル(ローカル、S3、GCS) | `from.Files` |
| Postgres | `from/postgres` |
| MySQL | `from/mysql` |
| 複数のソース | `from.Many` |

| 宛先 | パッケージ |
|---|---|
| ファイル | `to.Files` |
| BigQuery | `to/bigquery` |
| Postgres | `to/postgres` |
| MySQL | `to/mysql` |
| Redshift | `to/redshift` |
| Pub/Sub | `to/pubsub` |

パスのスキームがバックエンドを決め、`Store` はドライバの内部で選ばれるのではなく**外から渡されます**。

```go
from.Files{Path: "s3://bucket/day=1/*.ndjson", Store: s3.New(client)}
to.Files{Path: "gs://bucket/landing/", Store: gcs.New(client)}
```

これにより、ローカルファイルだけを扱うプログラムが、AWS や Google のコードを 1 行たりと
コンパイルせずに済みます。

### トピックへの公開

`to/pubsub` はテーブルではない唯一の宛先で、使う前に知っておく価値のある形で動作が
異なります。**送るものに何も付け加えません。**

```go
Target: sdk.Target{
    To: pubsub.Topic{
        Project: "acme-prod",
        Name:    "orders",

        // Nil is the default, and it means NO attributes at all.
        Attributes: func(e sdk.Envelope) map[string]string {
            row, ok := e.Payload.(map[string]any)
            if !ok {
                return nil
            }
            return map[string]string{"orderId": fmt.Sprint(row["order_id"])}
        },
    },
},
```

サブスクライバは、ペイロードをバイト単位でそのまま受け取り、加えて指定した属性だけを受け取ります。
それ以外は何もありません。`ingestion_id` も、`provider` も、いかなる種類のエンベロープもありません。

これはミニマリズムではなくルールです。テーブルは、それを書き込むパイプラインが作るので、SDK が
列を決めてかまいません。**トピックは違います。** パイプラインより前から存在し、サブスクライバが
先に書かれ、そのフィルタも先に書かれています。勝手に属性を足せば、Brevis が他者の契約を
書き換えることになり、サブスクライバはそれを午前 3 時に知ることになります。

ここから 3 つのことが導かれます。

- **トピックを作ることは決してありません。** トピックを作れるパイプラインは、間違ったトピックも作れます。
  しかもタイプミスしたテーブルと違って、誰も気づきません。メッセージはどこかへ行き、
  受け取るはずだったサブスクライバは静かなままです。
- **`Schema`、`Dedup`、`PartitionBy` は拒否され**、オプション名が示されます。これらはテーブルのための
  ものです。`Schema` は宛先がテーブルを作る方法で、重複排除には置き換える対象の行が必要で、
  パーティションはテーブルの概念です。ここで最も近いのは `OrderingKey` ですが、これは別物で、
  順序どおりに*届く*必要があるメッセージをグループ化します。
- **失敗は部分的です。** 公開はトランザクションではないので、48,000 件のメッセージが
  31,000 件目で失敗した場合、31,000 件は*配信済み*です。結果は実際に送られたものを報告し、
  エラーは、再実行は再配信であると伝えます。サブスクライバは冪等でなければならず、
  Pub/Sub はいずれにしても at-least-once です。

エミュレータに対して実行できる例:
[`examples/13-pubsub`](https://github.com/AreteAcademy/brevis/tree/master/examples/13-pubsub)。

## フェッチャー全体

`sdk.Run` は、フラグ、`-dry-run`、ログ、リトライ、ページネーション、来歴、テーブル作成、終了コードを
引き受けます。ファイルに残るのは、そのソース固有のものだけです。

```go
package main

import (
	"time"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
)

func main() {
	sdk.Run(sdk.Pipeline{
		Source: sdk.Source{
			From: from.HTTP{
				URL:     "https://api.example.com/v1/events",
				Timeout: 15 * time.Second,
			},
			Guard:  sdk.RejectIf("error"),
			Expand: sdk.ArrayAt("results"),
		},
		Target: sdk.Target{
			Provider: "example",
			Entity:   "events",
			Key:      sdk.Key("id"),
			When:     sdk.Field("created_at"),
		},
	})
}
```

```bash
go run ./fetcher -dry-run   # extracts, counts rows and errors, writes nothing
go run ./fetcher
```

## run コンテキスト

フェッチャーが**ワークフローのステップとして**動くとき、エンジンは、そうでなければ知り得ないことを
環境に注入します。これが初回の run かどうか、どのパラメータでディスパッチされたか、どの run かです。
SDK はそれを `Pipeline.Run` で読み取ります。

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	if p.Run.Params["load_full"] == "true" {
		p.Source.From = from.HTTP{URL: base + "?full=1"}
	}
	return nil
},
```

手動で実行した場合、`Run` はゼロ値で返ります。読むのは任意で、無視しても何のコストもありません。

履歴がない場合、「これは初回の run か?」への答えは常に**いいえ**です。確信なしにテーブルを作るのは、
作らないよりも悪いからです。

### 時計

`p.Run.Auto` は、エンジンがこの run について算出したものです。
[自動パラメータ](/docs/parameters/#自動パラメータ)を参照してください。`Auto.Now()` は、
`time.Now()` の代わりに読むべき時計です。スケジュールされた run ではそれは slot なので、run が遅れても
動かず、3 時間後にリトライされても動きません。

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	start, end, ok := p.Run.Auto.Window()
	if !ok { // no schedule: fall back to a fixed window
		start, end = p.Run.Auto.Now().Add(-24*time.Hour), p.Run.Auto.Now()
	}
	p.Source.From = from.HTTP{URL: base +
		"?from=" + start.Format(time.RFC3339) +
		"&to=" + end.Format(time.RFC3339)}
	return nil
},
```

手動で実行した場合、`Auto.Now()` は実際の壁時計*そのもの*で、`Window()` は `false` を返すので、
ローカル開発に特別な場合分けは要りません。

## クライアントライブラリと混同しないこと

この SDK は Go による ETL の仕組みです。ドライバ、ページネーション、来歴、テーブル作成を含みます。
[クライアントライブラリ](/docs/libraries/)は別物で、ステップに run コンテキストと時計を渡す薄い
クライアントです。現在は Python 用で、後に Node.js と Rust 用が加わります。pandas や dbt を
使うステップが必要とするのは、この SDK ではなく後者です。

## パイプラインを構築するパラメータ

`p.Run.Params` はパイプラインの内部で読めますが、パイプラインが**何であるか**を決める値、つまり
状態ごとに 1 つのソース、テナントごとに 1 つのテーブルといったものには遅すぎます。
`Pipeline.Flags` は `Run` の内部でパースされるので、同じ理由で遅すぎます。

```go
func main() {
	sdk.Run(pipeline(sdk.ParamList("ufs")))
}
```

`sdk.Param` と `sdk.ParamList` は、まずエンジンの環境を読み、次に `-param name=value` を読みます。

```bash
./fetch-stations -param ufs=SP,RJ
```

環境が優先されます。エンジンの下ではそれが値であり、マニフェストに残ったフラグが、
オペレータが入力したものを上書きしてはならないからです。エンジンがなければフラグがあるものの
すべてで、実行のたびに `BREVIS_RUN_PARAMS` を JSON で書くよりは良い方法です。

## ランディングレイアウト

パイプラインは、**gateway が着地させるのと同じテーブル**、つまり同じ列と同じ
`brevis_ingestion_id` を着地させられます。そのため両者の選択はデプロイの判断であって、
スキーマの判断ではありません。チームは両方をまとめて読むことができ、マイグレーションなしで
一方から他方に移れます。

```go
const table = "landing.orders"

sdk.Run(sdk.Pipeline{
    Source:    sdk.Source{From: from.HTTP(/* ... */)},
    Transform: []sdk.Transformer{sdk.Landing(table, sdk.LandingKey("id"))},
    Target: sdk.Target{
        To:     postgres.Table{DSN: dsn, Name: table, CreateTable: true},
        Schema: sdk.LandingSchema(sdk.LandingOptions{}),
    },
})
```

`sdk.LandingSchema` は 9 つの列です。レイアウトが持つ 8 つと、プロデューサーが持つ 1 つです。
`sdk.Landing` がそれらを埋め、レコードを丸ごと `data` に入れます。必要なら自分の列をスキーマに
追加するか、`sdk.LandingControlColumns` を使ってレコードのフィールドそれぞれに独自の列を
与えてください。

### 2 つの形: レコードを丸ごと、またはフィールドごとに 1 列

上記は**ドキュメント**形式です。レコードは JSON として `data` に入り、レコードの中身が何であっても
テーブルは同じです。`sdk.LandingColumns()` を加えると、レコードの各フィールドが独自の列を持ちます。

```go
sdk.Landing(table, sdk.LandingKey("source_key"), sdk.LandingColumns())
```

この場合のテーブルは `sdk.LandingControlColumns` にあなたの列を足したものです。`data` を持つ
`sdk.LandingSchema` では**ありません**。

```go
Target{
    To: postgres.Table{DSN: dsn, Name: table, CreateTable: true},
    Schema: append(sdk.LandingControlColumns(sdk.LandingOptions{Keyed: true}),
        sdk.Column{Name: "source_key", Type: sdk.TypeString},
        sdk.Column{Name: "series", Type: sdk.TypeString},
        sdk.Column{Name: "data", Type: sdk.TypeString},
        sdk.Column{Name: "valor", Type: sdk.TypeString},
    ),
}
```

**それらの列は自分で宣言します。** SDK がスキーマを推論することは決してなく、このオプションでも
それは変わりません。変わるのは*行*の組み立て方であって、テーブルが何であるかではありません。
gateway が推論するのは、プロデューサーが手持ちのものを何でも送ってくる他人だからです。ここでは
パイプラインの作者が形を知っていて、誰かがレビューする差分の中にそれを書き下ろします。

これは、gateway が [`shape: columns`](/docs/ingestion-sinks/#2-つの形1-つの契約) として着地させるのと
同じ形で、同じコードを通ります。列も、値も、`brevis_ingestion_id` も同じです。

**フィールド名は列名になれるものでなければなりません。** 英字またはアンダースコアで始まり、
その後に英数字とアンダースコアが続く形です。これは 4 つの宛先の中で最も狭い BigQuery のルールなので、
ここを通る名前はどこでも動きます。`my-field` を持つレコードは、何かが書き込まれる前に、名前を示して
拒否されます。ドキュメント形式にはそのようなルールはありません。ハイフンは JSON のキーとして
まったく問題ありません。

**デフォルトではフラット化しません。** ネストされたオブジェクトは、それを保持していたキーの下の
**1 つの** `JSON` 列になります。`customer` は `customer_id` と `customer_uf` ではなく、
`{"id": 7, "uf": "SP"}` を保持します。配列も `JSON` です。配列の要素ごとに 1 行が欲しいレコードには、
ソースの `Expand` に `sdk.ArrayAt` が必要です。それは `Landing` より前に実行される、別の名前を持つ
別の操作です。[`BREVIS_NORMALIZE_DATA`](#ネストされたフィールドごとに-1-列) は、デプロイ全体について
この前半を変更します。配列はどちらの場合でも `JSON` のままです。

```go
Source: sdk.Source{From: from.HTTP(/* ... */), Expand: sdk.ArrayAt("results")},
```

**`null` は `NULL` のままで、`""` にはなりません。** null として送られたフィールドと空として
送られたフィールドは別の事実であり、列では後から区別できません。

**`data` はあなたのものになります。** この形式では、レイアウトにその名前の列がないため、`data` という
フィールドを持つプロデューサー(Bacen の系列がまさにそうです)は、自分の値を持つ独自の列を得ます。
`document` では、彼らのものはレイアウトの JSON 内の 1 つのキーです。

### `brevis_` プレフィックスは変更できる

```
BREVIS_LANDING_PREFIX=acme
```

とすると、レイアウトの 8 つの列は `acme_ingestion_id`、`acme_record_key` などになります。
未設定なら、従来どおりの名前です。

**テーブルを作るときに選んでください。後から変えることは、リネームではないからです。**
この SDK は列を削除することが決してないため、プレフィックスを切り替えたストリームは 8 つを
リネームするのではなく、8 つを追加して 8 つを放棄します。それ以降のすべての行で古いセットが
NULL になり、

```sql
qualify row_number() over (partition by brevis_record_key
                           order by brevis_received_at desc) = 1
```

は、もう何も書き込まない列でパーティションを切ることになります。行は届き、ダッシュボードは
横ばいになり、何もログに残りません。

そのため、すでにあるプレフィックスでレイアウトを持つテーブルは、別のプレフィックスを宣言した
ロードを、抽出の前に、両方を示して**拒否**します。新しいテーブルを指定するか、先に 8 つの列を
自分でリネームしてください。

**入力したものは正規化されます。** そのため、間違える綴りは 4 通りではなく 1 通りです。

| 設定 | 結果 |
|---|---|
| 未設定、または空白 | `brevis_` |
| `_NAME` | `name_` |
| `_NAME_` | `name_` |
| `NAME` | `name_` |

両端の `_` を取り除き、小文字にし、ちょうど 1 つを付けます。2 つの値は推測せずに拒否されます。
アンダースコアのみの値は、`ingestion_id` が残ってしまうためです。これは、普通のパイプラインすべてで
`sdk.IngestionID()` が書き込む列そのものです。もう 1 つは、列名の先頭になれないものです。
**使えない値を持つプロセスは起動しません。** これが唯一の誠実な結果で、代案は、置き換えようとしていた
プレフィックスの名前を持つテーブルができてしまうことだからです。

**これはプロセス単位であり、制限ではなく意図です。** 1 つの gateway のすべてのストリームがそれを
共有するので、2 つの sink が食い違うことはありません。テーブルを宣言するスキーマと、それを埋める
トランスフォーマーも食い違いません。

**予約ルールもそれに合わせて移ります。** `acme_` の下では、`acme_region` を送るプロデューサーは拒否されます。
制御列を偽造できる人は、本物らしいものを偽造するからです。そして `brevis_region` は普通のフィールドに
なります。

`data` にプレフィックスが付くことはありません。それはレイアウトの列ではなく、プロデューサーの列だからです。

### ネストされたフィールドごとに 1 列

```
BREVIS_NORMALIZE_DATA=true
```

とすると、ネストされたオブジェクトのフィールドが、pandas の `json_normalize` と同様に、独自の列を
持ちます。1 階層だけです。

```python
data = [
    {"id": 1, "name": {"first": "Coleen", "last": "Volk"}},
    {"name": {"given": "Mark", "family": "Regner"}},
    {"id": 2, "name": "Faye Raker"},
]
```

```
id  name_first  name_last  name_given  name_family  name
```

**最初のテーブルの前に選んでください。列を追加するだけではないからです。** 列をリネームし、
この SDK は列を削除することが決してないため、古い名前は既存の行とともに残り、それ以降のものは
新しい名前に着地します。古い名前に対して誰かが書いたクエリは動き続けますが、新しい行を見なくなります。
リネームは 2 種類起きます。

- `userName` は `username` になります。ネストの有無は関係ありません
- オブジェクトを保持していた `name` は、`name_first` と `name_last` に置き換わります

どちらかの形をすでに持つテーブルは、これをオンにしたロードを、抽出の前に、2 つの列を示して
**拒否**します。新しいテーブルを指定するか、先に列を自分でリネームしてください。

**`name` はフラット化されたものと丸ごとのものの両方で現れることがあります。** 上の例がそれを
示しています。3 番目のレコードは `name` を文字列として持つので、テーブルには `name_first`、
`name_last`、そして `name` があります。あるレコードではオブジェクトで、別のレコードではスカラーである
フィールドは、両方を生みます。

**1 階層だけで、配列は値です。** `{"a": {"b": {"c": 1}}}` は、`a_b_c` ではなく、`{"c": 1}` を保持する
`JSON` 列の `a_b` になります。`{"a": [1, 2]}` は `a` で、これも `JSON` です。配列の要素ごとに 1 行が
欲しいレコードには、やはり `sdk.ArrayAt` が必要です。

**連結して小文字にし、camelCase では分割しません。** `userName` は `user_name` ではなく `username` に
なります。もう一方の読み方は、頭字語(`HTTPStatus`、`userID`)について合意された答えがなく、
どのライブラリも食い違うからです。

**衝突は解決されず、拒否されます。** `{"name_first": 1, "name": {"first": 2}}` は 2 つのフィールドから
1 つの列を作ることになり、Go のマップの反復順序はランダム化されています。どちらかを勝たせると、
同じバッチが実行のたびに異なるテーブルになります。拒否のメッセージは両方を示します。

**予約済みプレフィックスは、フィールドではなく列でチェックされます。** そうしないと、
`{"brevis": {"stream": "x"}}` を送るプロデューサーが、フィールドのチェックが見張っていない扉から
`brevis_stream` を作れてしまいます。この場合は `brevis.stream` を示して拒否されるので、
フィールドを見つけられます。

**プレフィックスと同様にプロセス単位です。** 理由も同じで、1 つの gateway のすべてのストリームが
共有するため、両方が書き込むかもしれないテーブルについて 2 つの sink が食い違うことはありません。
適用されるのは `columns` 形式だけです。`document` ではレコードが丸ごと 1 つの `JSON` 列に入り、
フラット化先がありません。

### すでに JSON であるテキスト

`sdk.TypeJSON` と宣言された列は、JSON の**値**、つまりオブジェクト、配列、数値を受け取ります。
BigQuery では行が丸ごとエンコードされるため、Go の `string` は JSON の文字列リテラルになり、
その列はオブジェクトではなく、オブジェクトの TEXT を保持することになります。

```sql
SELECT JSON_TYPE(payload) ...;            -- "string", not "object"
SELECT JSON_VALUE(payload, '$.a') ...;    -- NULL
```

列の型は正しいのに、中のデータには到達できません。何も失敗しないので、これは 3 つの結果の中で
最悪のものです。**BigQuery の宛先は `v0.74.0` からこれを拒否し**、列名を示します。

オブジェクトがあるなら、オブジェクトそのものを渡してください。持っているものがすでに JSON テキスト
である場合(別のデータベースから読み出した列や、再パースしたくないレスポンスボディなど)は、
そう明示します。

```go
row["payload"] = sdk.JSONText(body)
```

`encoding/json` 自身の `json.RawMessage` も同様に動作し、理由も同じです。どちらも、引用符で囲んだ
文字列ではなく、保持している JSON を書き込みます。

**Postgres と MySQL は影響を受けず、拒否もされません。** それらのサーバーは文字列を JSON 列に
パースし、以前からそうしています。

### テーブルにないフィールドが現れたとき

プロデューサーがフィールドを追加すると、ロードはそのフィールドを示して止まります。行は何も宣言されて
いない列を持っており、それを一度も言及していない宛先に書き込むと、黙って捨てることになるからです。
その拒否はデフォルトでは正しいのですが、ランディングテーブルでは、リーダーを壊し得ない唯一の変更で
あり、宛先がそれを行えます。

```go
postgres.Table{DSN: dsn, Name: table, Evolve: sdk.EvolveAdditiveFromPayload}
```

するとバッチがあなたの宣言を**補完**します。バッチが持っていて宣言にないフィールドが列になり、
オブジェクトか配列でない限り `STRING`、その場合は `JSON` になります。gateway が `shape: columns` で
着地させるのと同じルール、同じコードなので、どちらも同じテーブルを生成します。

ほかに 2 つの値があり、その違いは列がどこから来るかです。

| `Evolve` | 追加するもの |
|---|---|
| `sdk.EvolveNone`(デフォルト) | なし。どんな差異も、両側を示して拒否されます |
| `sdk.EvolveAdditive` | **宣言**にあってテーブルにない列 |
| `sdk.EvolveAdditiveFromPayload` | 上記に加えて、**バッチ**が持つ列 |

**行うのは `ADD COLUMN` だけです。** ソースから消えた列は書き込まれなくなりますがテーブルには残り、
リネームも削除もせず、型を狭めることもありません。これは欠陥ではありません。ランディングテーブルは
履歴であり、それを読む誰かを壊し得ない唯一の変更は、null 許容の新しい列だからです。

**宣言を補完するのであって、置き換えるのではありません。** 何も宣言されていなければ、抽出の前に
テーブル名を示して拒否されます。ペイロードからテーブル全体を作るのは別の判断であり、SDK はそれを
行いません。ランディングレイアウトでは害が具体的です。`brevis_received_at` はタイムスタンプではなく
テキストになり、`brevis_loaded_at` は行に載って運ばれないので、エンドツーエンドのレイテンシ計測が
単に欠けることになります。

**SDK は依然として推論しません。** 型はフィールドの形から決まり、値からは決して決まりません。
`21129` と `8.89` はどちらも `STRING` を宣言するので、系列が整数を公開する日にも何も変わりません。
最初のバッチから読み取った型は、このルールが防ごうとしている失敗そのものです。

**そして `null` は何も決めませんが、行には残ります。** これは別々の 2 つの問いであり、1 つの答えで
済ませたことが v0.77.0 のコストになりました。

null には形がないので、**スキーマ**が読み取るものはありません。`null` で届いたフィールドは列を
宣言せず、列は値を持つ最初のレコードから、その値の形で現れます。逆にすると、null は答えがないために
`STRING` になり、その後に最初の配列が来ると、宛先がバッチ全体を拒否します。それを元に戻す
`STRING` から `JSON` へのマイグレーションもありません。

**行**は、プロデューサーが送ったものを、null も含めて保持します。そうする必要があります。
`Target.Schema` で宣言した列が空で届いた場合、それは行に存在しなければなりません。さもないと、
宣言を行と比較するチェックが、すでに生成をやめたチェーンを読み、ロードを拒否します。それが
v0.77.0 のしたことであり、0.78.0 が元に戻したことです。

残りは宛先が処理します。バッチのすべてのレコードで null で、テーブルに列がないフィールドは、
どこにも書き込まれず、何も拒否しません。null と存在しないフィールドは同じ `NULL` に着地するので、
失われるものはありません。

**形が変わるフィールドは、吸収されずに拒否されます。** `valor` がスカラーとして届いてテキスト列を
作った場合、後から届くオブジェクトは種類の変更であり、バッチは止まります。メッセージにはその列自身の
コメントが付くので、誰も宣言していないことがわかります。列をリネームするか、自分でマイグレーション
してください。

**BigQuery では、同じ名前の 2 つの綴りは 1 つの列です。** BigQuery は列の大文字小文字を畳み込みます。
あるレコードに `nationalID`、別のレコードに `nationalId` を持つバッチは、2 つではなく **1 つ**の列を
宣言し、勝つ綴りはソート順で最初のもの、つまり `D` が `d` より前に来るので `nationalID` です。
最初に到着したものではありません。同じバッチを再生しても、綴りの異なる列を作ってはならないからです。
`createdat` を宣言する一方でテーブルの列が `createdAt` である場合も同じ列なので、何も追加されず、
何も拒否されません。

**1 つのレコードが両方の綴りを持つ場合は、両方を示して拒否されます。** 宛先には 1 つの列しかなく、
2 つの値の一方が黙って勝つことになるため、ロードは止まります。`Transform` でどちらかをリネームするか、
両方を 1 つのフィールドにマッピングしてください。それぞれが片方の綴りを持つ 2 つの*レコード*なら
問題ありません。それは普通の CDC トラフィックであり、このルールが着地させるために存在するものです。

**何もマイグレーションされません。** 大文字小文字だけが異なる 2 つの列をすでに持つテーブルは、
両方を保持します。リネームも削除もされません。

**Postgres と MySQL は畳み込まれません。** 引用符付き識別子はそこでは区別されるため、2 つの綴りは
2 つの列になります。これは意図的で、同じフェッチャーを BigQuery に向けると、それらは 1 つの列に
着地します。

**バッチが作成したすべての列は、テーブルの中でそう明記されます。** Postgres と MySQL ではコメント、
BigQuery ではフィールドの説明として、日付が入ります。

```
brevis: added from a batch on 2026-09-29; the type is the landing rule
(scalar text, object and array json), not a decision
```

半年後、「この列はいつ現れ、誰が決めたのか」に答えられるのは、それだけです。そのころには
ログの行はローテーションされて消えています。

**これはモードではありません。** `Target.Schema` は、テーブルが何であるかをあなたが宣言する
ものであることに変わりなく、これは、すでに存在するレイアウトを、自分で入力する代わりに
渡すだけです。使ったからといって SDK の挙動が変わるものは何もなく、独自のスキーマを宣言する
パイプラインは影響を受けません。

**テーブル名は一度だけ書いてください。** それは `Landing` に渡されます。id の 2 番目のスロットだからです。
そして宛先にも渡されます。行の行き先だからです。**この 2 つが一致しているかを確認するものは
ありません。** `Writer` は自分のテーブルを知っていますが、それをログとエラー用の名前である
`Describe` を通してのみ公開しており、それをもとに作った id は、すでに書き込まれたすべての id を
ログの文字列に結びつけてしまうからです。両者がずれると、行は正しく着地しますが、id は誰も書き込んで
いないテーブルのために生成されたものになります。見た目は問題なく、gateway のものとは一致しません。

**3 つの列は `NULL` で、それが誠実な答えです。** `brevis_stream` と `brevis_gateway` は、パイプラインが
持たないものを指します。`brevis_gateway IS NULL` が、パイプラインの行と gateway の行を見分ける方法です。
`brevis_received_bytes` も `NULL` です。gateway のそれは、エンベロープを含めてワイヤ上に届いたものを
数えますが、レコード自身の JSON は約 30% 小さくなります。2 つの意味を持つ 1 つの列では、両方の経路の
行にまたがる合計が、それぞれがどの割合を占めるかの分だけ間違ってしまいます。`length(data)` は
パイプライン版の答えを正確に返します。

**マージには `DedupKey` が必要で、それがなければマージはまったく行われません。** どのドライバも、
指示がない限り `ingestion_id` で照合しますが、このレイアウトの識別列は `brevis_ingestion_id` です。

```go
Target{
    To:       postgres.Table{DSN: dsn, Name: table, CreateTable: true},
    Schema:   sdk.LandingSchema(sdk.LandingOptions{UniqueID: true, Keyed: true}),
    Dedup:    sdk.DedupMerge,
    DedupKey: sdk.LandingColumnID,
}
```

`UniqueID` は id に UNIQUE 制約を付けるもので、Postgres も MySQL も、それなしではマージを拒否します。
`ON CONFLICT` に照合する対象がないからです。`Schema` で宣言されていない `DedupKey` は、何かが
実行される前に拒否されます。テーブルにない列でのマージは何にも一致せず、何にも一致しないマージは、
一致すべきものすべてに一致するマージと見分けがつかないからです。

**宛先ごとに異なり、現時点では MySQL はできません。** BigQuery には一意制約がなく、宣言を
そのまま拒否します。MySQL の `string` は `LONGTEXT` で、MySQL は長さなしで `LONGTEXT` をキーに
しないため、マージするランディングテーブルは、そこではそもそも*作成*できません。

```
Error 1170 (42000): BLOB/TEXT column 'brevis_ingestion_id' used in key
specification without a key length
```

Postgres は動作します。その `string` は `TEXT` だからです。MySQL では、`DedupNone` で着地させて
下流で現行バージョンを解決するか、`CreateSQL` と長さ付きの id 列で自分でテーブルを作成してください。

**id は内容アドレス方式です。** 同じレコードはどちらの経路からも同じ id を生成し、変更された
レコードは新しい id を生成します。これにより、再実行が重複ではなく何もしない操作になります。

## リファレンス

- [pkg.go.dev](https://pkg.go.dev/github.com/AreteAcademy/brevis/sdk) — 完全な API
- [`examples/`](https://github.com/AreteAcademy/brevis/tree/master/examples) — 実行可能な 12 個の例
- [`CHANGELOG.md`](https://github.com/AreteAcademy/brevis/blob/master/CHANGELOG.md) — バージョンごとの履歴
