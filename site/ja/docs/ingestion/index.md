# 取り込み

> データを着地させる HTTP エンドポイント — 検証、識別、バッチ化、配信。専用のイメージ、専用のバージョン。

*https://brevis.sh/ja/docs/ingestion/ · brevis.sh docs (ja)*

---

取り込み用の gateway は、**データを着地させる HTTP エンドポイント**です。クライアントが `POST` し、
`202` を受け取り、届いたものがトピックのレコード、テーブルの行、またはバケットのファイルになります。

![Gateway のフロー: /v1/clicks への POST がデコードされ、ingestion_id が付与され、すぐに 202 で応答され、バッチに追加されます。ワーカーがバッチを sink(ここでは files)に配信します。Gateway にデータベースはありません。](/assets/flow-gateway.svg)

これは**別のバイナリ、別の Go モジュール、別のバージョン、別のイメージ**です。エンジンは
オーケストレーションを行い、顧客データには決して触れません。gateway はそれ以外のことをしません。
採番が独立しているのは意図的で、エンジンは 0.17、gateway は 0.27 であり、それが事実だからです。

次のセクションの `gateway.yaml` と、`keys_from` が要求するキーを使うと:

```bash
docker run -p 8080:8080 -e BREVIS_INGEST_KEYS=dev-key \
  -v ./gateway.yaml:/etc/brevis/gateway.yaml:ro \
  areteacademy/brevis-gateway:0.32.0
```

```text
2026/10/09 01:03:01 drain budget 30s; set terminationGracePeriodSeconds >= 40
2026/10/09 01:03:01 connections idle out after 1m30s and are recycled after 1m0s
2026/10/09 01:03:01 listening on :8080
2026/10/09 01:03:01 metrics on :9090/metrics
```

`POST` はすぐに `202` で返ります。

```bash
curl -X POST localhost:8080/v1/clicks -H 'Authorization: Bearer dev-key' \
  -d '{"event_id":"e-1","occurred_at":"2026-10-08T12:00:00Z","page":"/pricing"}'
```

```text
{"accepted":1,"rejected":null}
```

Google Cloud の認証情報がなければ、バッチはリトライされ、dead letter に送られます。
[sink が拒否したとき](#when-the-sink-refuses)を参照してください。

`-slim` イメージが含むのは `auto_table`、`files`、`postgres` の sink だけです。この例の `pubsub` と
その他は完全なイメージにあります。含まれていない sink を指定した slim イメージは、起動時に理由を
示して拒否されます。

## ファイルが契約

フック以外に書くコードはありません。ストリーム、パス、フォーマット、識別、リトライ、宛先、
dead letter のすべてが YAML にあり、守れないフィールドはすべて拒否されます。

```yaml
name: events_gateway

listen:
  addr: :8080
  max_body: 1MiB
  idle_timeout: 90s                  # the default; it cannot be turned off
  max_conn_age: 60s                  # off unless set — see below
  auth:
    type: bearer
    keys_from: BREVIS_INGEST_KEYS   # the NAME of the variable, never the keys

metrics:
  addr: :9090                        # its own port, never the ingest one

streams:
  - name: clicks
    path: /v1/clicks
    format: json                     # json | array | ndjson — declared, never sniffed

    identity:
      provider: web
      entity: click
      source_key: event_id
      record_ts: occurred_at

    buffer:
      flush: {every: 1s, records: 500}

    sink:
      type: pubsub
      project: acme-prod
      topic: clicks

    dead_letter:
      type: files
      path: /var/dead-letter/
```

`format` は宣言するもので、推測されることはありません。推測すると、1000 件のバッチが、
配列を保持する 1 つの行になってしまいます。

## `ingestion_id`、これが要点

すべてのレコードは `ingestion_id` を付けて出ていきます。`provider|entity|source_key|record_ts` に対する
**固定の UUID v5** です。

これは、バッチのフェッチャーが同じレコードに対して計算するものと**同じ id** です。gateway が着地させた行と、
[SDK](/ja/docs/sdk/index.md) のパイプラインが着地させた行は*同じ 1 行*になり、両者の間で突き合わせは不要です。

実用上の帰結が 2 つあります。

- **リトライされた `POST` は同じレコードです。** クライアントは心配せずに再送でき、`merge` を持つ宛先は
  2 行目を書かずにリトライを吸収します。
- **計算式は固定されています。** 4 つのフィールドはすべて必須で、1 つでも省くと、弱い id ではなく
  *別の* id が生成されます。ファイルは、不完全な設定を受け付けるのではなく拒否します。

取り込みツールはバイトを運ぶのは非常にうまいのですが、レコードが*何であるか*については意見を持ちません。
その意見を加えるのが gateway です。

## リクエストは sink を待たない

デコードし、フックを実行し、識別を計算し、バッファに追加して、返ります。**それだけです。**
満杯のバッチはワーカープールに渡され、publish、`COPY`、リトライ、dead letter はすべてそこで行われます。

これは見かけだけの話ではありません。これが存在する前は、たまたまバッチを満たしたリクエストが配信を
インラインで実行していたため、`flush.records` 件に 1 人の呼び出し元が往復の全コストを負担し、sink が
落ちているときはリトライの全期間を負担しました。どの呼び出し元が不運だったかで形が決まる p99 は、
誰も行動の根拠にできません。

```yaml
buffer:
  flush:
    every: 1s         # age
    records: 500      # count
    size: 8MiB        # bytes — whichever is crossed FIRST sends the batch
    max_age: 30s      # and this one sends it REGARDLESS
  workers: 4          # batches delivered at once
  queue: 64           # full batches that may wait for a worker
  max_records: 10000  # events held in memory before the gateway says no
  max_bytes: 256MiB   # the same ceiling in bytes
```

### フラッシュの時計はデプロイに属する

`buffer.flush.every` は**すべてのレプリカ**にある `time.AfterFunc` です。そのため、テーブルが受け取る
ロードジョブはトラフィックではなくレプリカ数の関数になります。

```
jobs / day / table  =  replicas × 86400 / every
```

BigQuery では、テーブルあたり 1 日 1,500 に対して、**60 秒で 2 レプリカでも、引き上げられない
クォータの 192%** になります。HPA が何かをする前の話です。しかも方向は直感の逆です。gateway は
HTTP の同時実行数のためにスケールアウトしますが、sink は集約を求めます。

```yaml
buffer:
  flush:
    every: 60s
    max_age: 300s
    claim: true
```

`claim` を使うと、レプリカはタイマーでフラッシュする前に、ストリームとウィンドウに対する
set-if-absent を取得します。タイマーは**ウィンドウの境界**で起き、すべてのレプリカが同じキーを要求します。
レプリカ数にかかわらず、ウィンドウごとにフラッシュは 1 回です。しかも自己調整します。レプリカが
待つのは、実際に別のレプリカと競合しているときだけなので、スケールインすれば、設定を誰も編集しなくても
低いレイテンシが戻ります。

2 レプリカと 1 つの Redis、5 秒のウィンドウで計測したところ、**8 回のロードジョブが 6 回になり、
ウィンドウごとに 1 回、3/3 で交互**でした。2 回あったウィンドウはありません。

**共有バックエンドが必要です。** `memory` では、すべてのレプリカが自分の claim に勝ち、何も調整されません。
また **`max_age` が必須**で、これがないと設定は拒否されます。claim は `every` を目標に変え、
上限のない目標は、何にも抑えられないレイテンシだからです。

**メタストアに到達できなくてもフラッシュします。** 調整を失うとロードジョブが増えますが、フラッシュを
拒否するとデータを抱え込みます。実行中に Redis を kill して確認しました。40 件中 40 件のイベントが
着地し、保留されたものはありませんでした。

**`outcome="unreachable"` は、アラートを設定する価値のある唯一のものです。** バックエンドに問い合わせられず、
claim がフェイルオープンしました。調整はオフで、ロードジョブはまたレプリカ数に比例して増えています。
すべてのフラッシュが依然としてフラッシュに見えるため、静かに進行します。

そして claim が与えるのは**排他性であり、公平性ではありません**。先に要求した者が勝ちます。実際の
2 レプリカでの実行では、均等な分割ではなく 4/1 でした。不運なほうが飢餓状態にならないようにするのが
`max_age` です。

`claim: true` と `metastore: memory` の組み合わせでは、gateway は**起動時に一度だけ**警告します。
すべてのレプリカが自分の claim に勝つので、すべて正しく見えて、何も調整されないからです。

### `every` は目標、`max_age` は約束

上の 3 つのトリガーは、バッチがいつ出ていこうと**試みる**かを決めます。`max_age` は、バッチが
**どうあっても**いつ出ていくかを決めます。

現時点では、目標を遅らせるものがないため、この違いは機能していません。フラッシュの時計がプロセス単位で
なくなると重要になります。`buffer.flush.every` は各レプリカの `time.AfterFunc` なので、テーブルが受け取る
ロードジョブの数はトラフィックではなく**レプリカ数**の関数、つまり `replicas × 86400 / every` です。
BigQuery では、テーブルあたり 1 日 1,500 に対して、**60 秒で 2 レプリカでもクォータの 192%** です。

レプリカが他のレプリカにウィンドウを譲れるようになると、それはバッファを満たし続けます。
順番を待ってデータを無期限に抱え込むことを防ぐのが `max_age` です。

これはドレインも決めます。`SIGTERM` 時にバッファが**抱えている**可能性のあるものがそれで制限され、
gateway が起動時に出力する `terminationGracePeriodSeconds` はそこから導かれます。

目標より**小さい**上限は、ロード時に拒否されます。目標が一切適用されないことを意味するからです。

### サイズのトリガーがある理由

他の 2 つは**件数**であり、件数では 2 KB のイベント 500 件と 2 MB のイベント 500 件を区別できません。
1 MB と 1 GB が、ファイル上では同じ数字です。`max_records: 10000` も同じ盲点を持ちます。20 MB なのか
20 GB なのか、YAML からは何もわかりません。

プロデューサーが id の代わりにドキュメント全体を送り始めると pod が落ちますが、これが存在するまでは
**その制限を表現できるフィールドが 1 つもありませんでした**。

サイズは**届いたもの**で測ります。イベントがメモリ上で占める重さ(`map[string]any` は JSON の数倍)でも、
それを膨らませるフックの後でもありません。近似ですが、それで正しいのです。デコード時にコストがかからず、
ペイロードに連動して動き、防ぎたいのは 10 倍に膨らんだレコードだからです。

**BigQuery では `size` は目標ではなく上限です。** その宛先は、テーブルあたり 1 日 1,500 回のロードジョブを
許可しており、それが `every` に 60 秒の下限がある理由です。しかしその下限が管理するのは**タイマー**です。
数秒ごとに発火するサイズは、その下限をそのまま通り抜けて、同じクォータを使います。到着レートはロード時に
わからないため、それを拒否するものはありません。代わりに、可視性があります。

```
brevis_gateway_flushes_total{stream,trigger}   trigger = time | records | size
```

BigQuery のストリームで `trigger="size"` が増えていれば、バッチ化しているのはウィンドウではないと
告げています。

そのカウンタはストリーム単位ですが、クォータは**テーブル**単位です。`auto_table` では 1 つのルートが多数の
テーブルになり、94 個のテーブルが 1 つの数字を報告しても、どれについても何もわかりません。
`BREVIS_INGESTION_METRICS` の裏には、それに名前を付けるものがあります。

```
brevis_gateway_table_flushes_total{stream,table,trigger}
```

テーブルの 1 回のフラッシュはそのテーブルの 1 回のロードジョブなので、BigQuery が許す 1,500 のうち
1,200 でアラートを出すのは、この系列です。無条件のもう半分は起動時の 1 行で、イベントが届く前に、
`flush.every` がテーブルあたり 1 日にいくらかかるかを示します。

**上限があり、撃ちっぱなしではありません。** バッチごとの goroutine では、sink の障害がメモリの
無制限な増加になってしまいます。キューとバッファがどちらも満杯のとき、行き場のないイベントに `202` を
返すのではなく、**`Retry-After` 付きの `503`** を返します。受け付けたのに配信されないイベントは、
このサービスが決して持たないために存在する唯一の結果です。

そしてその `503` は、ほとんどのサービスが主張できない形で**リトライしても安全**です。`ingestion_id` は
イベント自身の固定された関数なので、同じボディを再送すれば*同じレコード*だからです。

## 接続がどのレプリカに負荷がかかるかを決める

Kubernetes の `ClusterIP` Service の背後では、kube-proxy はリクエストごとではなく **TCP 接続ごとに
一度**バックエンドを選びます。ほとんどのクライアントは接続を維持して再利用するため、少数の接続を持つ
プロデューサーは、何台が動いていても、すべてを同じ 1、2 台のレプリカに送り、HPA が追加した
レプリカは、誰もダイヤルしないので何も受け取りません。

```yaml
listen:
  idle_timeout: 90s     # closes a connection nobody came back to
  max_conn_age: 60s     # after this, the next response asks the client to redial
```

**`max_conn_age` はデフォルトでオフ**で、これは意図的です。クライアントが 1 つの接続をどれだけ長く
保てるかはポリシーであり、それは、年齢ごとに接続あたり 1 回のクラスタ内ハンドシェイクと引き換えに、
負荷を分散させるものだからです。リクエスト自体は常に処理されます。ヘッダーがレスポンスに付くだけで、
リトライされるものも、失われるバッチもありません。

**`idle_timeout` にはデフォルトがあり、オフにできません。** なしで提供すると、放棄されたクライアントごとに
ファイルディスクリプタが、プロセスの再起動まで残ります。長くしたければ長い値を設定してください。
そうすれば、それは誰かが選んだ数字になります。

2 つは独立しています。接続は先に達した境界に出会い、接続が年齢に達するのは使われ続けることによってだけです。
それはまさに、年齢による再分散が対象とする長寿命のプロデューサーです。

## sink が拒否したとき

バッチはリトライされます。デフォルトでは約 7 秒の間に 4 回、ジッター付きで倍々にしていき、その後
**dead letter** に送られ、すべてのレコードに理由が付きます。

```json
{
  "event_id": "dl-9",
  "ingestion_id": "84aaee4b-66af-5cc0-b97b-6856dec95b25",
  "_dead_letter_reason": "rpc error: code = NotFound desc = Topic not found",
  "_dead_letter_sink": "pubsub:acme-prod/clicks",
  "_dead_letter_at": "2026-09-25T02:02:32Z"
}
```

理由はログだけでなく**レコード上**にあります。後でそのファイルを見つけた人が持っているのはイベントで
あってログではなく、*なぜこれがここにあるのか*が最初の疑問だからです。

**`dead_letter` のないストリームは、ロード時に拒否されます。** デフォルトを沈黙にすると、
誰も判断しない場所に判断を置くことになり、ログの 1 行だけで済まされる拒否されたバッチは、
黙ってデータを失っています。

## フックは Go で、コンパイルして組み込む

```go
hooks := gateway.NewHooks()
hooks.MustRegister("enrich_clicks", enrichClicks)
gateway.Main(hooks, gateway.WithSinks(sinks))

func enrichClicks(e map[string]any) (map[string]any, error) {
    host, _ := e["host"].(string)
    e["tenant"] = strings.Split(host, ".")[0]
    return e, nil     // returning nil DROPS the event, on purpose
}
```

YAML はフックの名前を指定するだけで、フックそのものは含みません。選ぶ前に計測しました。Go の関数は
**93 ns/op**、Starlark は 959、yaegi は 1,281 で、バイナリには何も追加されません。そして
`plugin.Open` は選択肢にすらなりません。ここのすべての成果物がビルドされる `CGO_ENABLED=0` では、
`plugin: not implemented` を返すからです。

そのコストは、誰かが読む場所で言っておきます。**フックの追加は設定変更ではなく、再ビルドとデプロイです。**
フックをバイナリを出荷する人たちが書いているうちは正しいトレードオフですが、顧客がリリースなしで
フックを変更しなければならなくなる日には、間違ったものになります。

フックのエラーは、*そのイベント*を dead letter の経路に送り、隣のバッチを失敗させることは決してありません。
panic も同様です。イベントごとにリカバーされます。不正な形式のレコード 1 つで、他のすべてのストリームを
提供しているプロセスを落としてはならないからです。

## イベントが大きすぎるとき

```yaml
oversize:
  larger_than: 256KiB
  archive: {type: files, path: gs://acme-oversize/clicks/}
  hook: strip_heavy_clicks
```

イベントは `archive` に**丸ごと**書き込まれ、縮小版が、元を指すポインタを持ってストリームに続きます。

```json
{
  "event_id": "big-1",
  "ingestion_id": "bd8ac083-9796-50f5-bab6-77a4e2e57ad0",
  "_oversize": true,
  "_oversize_archive": "files:gs://acme-oversize/clicks/",
  "_oversize_bytes": 2149
}
```

これは **Claim Check** パターンで、単純な `413` より優れています。`413` はイベントを失い、サイズ超過の
ペイロードはたいてい最も興味深いものだからです。ドキュメント全体が添付されたリクエストであり、
デバッグする価値のあるケースです。

`larger_than` は、フックの後の 1 つの**イベント**を測ります。`listen.max_body` は別のもので、
**リクエスト**全体の上限であり、1 バイトもパースされる前に `413` で拒否します。

## 何を数えるか

**専用のポート**で 11 個の Prometheus 系列を公開します。取り込み用のポートでは決してなく、このルールは
エンジンよりもここのほうが重要です。取り込みポートは構造上公開されており、クライアントが `POST` する
場所だからです。そこに `/metrics` があると、パスを見つけた人にすべてのストリーム名、パス、宛先が
公開されてしまいます。両方に同じアドレスを指定すると、ロード時に拒否されます。

```
brevis_gateway_events_received_total{stream,format}     counter
brevis_gateway_events_rejected_total{stream,reason}     counter  per event, after decode
brevis_gateway_requests_refused_total{stream,reason}    counter  per request: unauthorized|body_too_large|malformed|empty|saturated
brevis_gateway_batches_total{stream,sink,outcome}       counter  delivered|retried|buried
brevis_gateway_reconciled_total{stream,sink}            counter  landed after the send window; also delivered
brevis_gateway_flushes_total{stream,trigger}            counter  time|records|size
brevis_gateway_flush_windows_total{stream,outcome}      counter  won|yielded|ceiling|unreachable
brevis_gateway_ingested_bytes_total{stream,table}        counter  opt-in
brevis_gateway_ingested_events_total{stream,table}      counter  opt-in
brevis_gateway_table_flushes_total{stream,table,trigger} counter  opt-in
process_start_time_seconds                              gauge    unprefixed, on purpose
brevis_gateway_saturated_total{stream}                  counter  the 503s
brevis_gateway_delivery_seconds{stream,sink}            histogram
brevis_gateway_buffer_records{stream}                   gauge
brevis_gateway_queue_batches{stream}                    gauge
```

`process_start_time_seconds` には `brevis_` の**プレフィックスが付いていません**。これは意図的です。
コレクタはまさにその名前を探すからです。プロセスがいつ起動したかを示し、これがないと、開始時刻の調整を行う
コレクタ(OpenTelemetry の Prometheus レシーバーで、Google Managed Prometheus はこれをもとにしています)は、
すべての累積系列を**最初のスクレイプ**に固定し、そのスクレイプの値をベースラインとして消費します。
結果として、すべてのカウンタが低く読まれ、差分は正しいのに合計が間違います。

最後の 3 つは、最初のインシデントの後まで誰も欲しがらないものです。`saturated_total` は、クライアントが
下がるよう伝えられたことを示す唯一の数字で、`buffer.max_records` に対する `buffer_records` は、それを
**予測する**唯一のものです。

`reason` は閉じた集合であり、エラーテキストではありません。エラー文字列にはテーブル名、列、時には行が
含まれ、不正な形式のクライアントが 1 つあるだけで、リクエストごとに新しい系列を作ってしまうからです。
テキストは dead letter の、レコード上に残ります。

## `200` ではなく `202`

gateway はイベントを**受け付けました**。`durability: memory`(実装されている唯一のティア)では、
それが誠実に主張できるすべてです。`200` に値するティアは、すでにそれを書き留めたものですが、
そのティアは存在しません。

`disk` は、受け付けてメモリとして扱うのではなく、**名前を挙げて**拒否されます。`disk` と書く人は
クラッシュしてもイベントが残ると信じており、届けることなく同意するのは、そのフィールドが防ぐために
存在する失敗そのものだからです。

シャットダウン時にバッファにあるものは、破棄されず配信されます。満杯のバッチと同じ経路を通り、
リトライされ、葬られます。

## やらないこと

- **Console には表示されません。** エンジンのインターフェースとデータベースを共有しないため、
  `/ingestion` ページはなく、`auto_table` が行うスキーマの進化もそこには現れません。それは次の
  ステップで、まだ書かれていません。
- **`table` を持つストリームはスキーマを進化させません。** テーブルを指定した場合、契約はテーブルのもの
  です。テーブルにないフィールドは、誰かがレビューしたテーブルを変更するのではなく、修正方法を示す
  メッセージとともに拒否されます。列を自分で増やすのは `auto_table` のすることで、そこが両者の違いです。
- **`table` を持つストリームは、テーブルもインデックスも作りません。** テーブルを作るサービスは、
  タイプミスを、誰も読まない 2 つ目のテーブルに変えてしまいます。作成が `auto_table` の下でだけ行われる
  のはそのためで、そこでは `naming` が存在できる名前を制限し、`listen.auth` が必須です。
- **公開されているイメージにフックはありません。** コンパイル済みフックの設計における誠実な成果物は
  それです。`hook:` を宣言しないストリームを提供します。

## 1 つのルート、N 個のテーブル

`auto_table` は、各イベントを**エンベロープ**が指定するテーブルにルーティングし、そのテーブルが
なければ作成し、新しいフィールドが現れたら列を追加します。

```json
{"table_name": "app_orders", "operation": "INSERT", "unique_key": "id",
 "data": {"id": "A-1", "total": 150, "customer": {"id": 7, "uf": "SP"}}}
```

制御フィールドは最上位に、レコードは `data` の中にあり、すべてのテーブルに 7 つの `brevis_*` 追跡列が
付きます。1 つのルート、N 個のテーブル、宣言するものは何もありません。

これは[取り込みの sink](/ja/docs/ingestion-sinks/#one-route-n-tables-nothing-declared)にあります。

## 次に進む先

| 目的 | 参照先 |
|---|---|
| 宛先と、それぞれの書き込み方 | [取り込みの sink](/ja/docs/ingestion-sinks/index.md) |
| 反対側から見た `ingestion_id` | [Go SDK](/ja/docs/sdk/index.md) |
| ダッシュボードとアラート | [オブザーバビリティ](/ja/docs/observability/index.md) |
