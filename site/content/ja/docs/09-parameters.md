---
title: パラメータ
description: ファイルを編集せずに、同じワークフローの 2 回の run の間で変えられるもの。
group: リファレンス
order: 9
slug: parameters
---

ワークフローは **run パラメータ**を宣言できます。ファイルを変えずに、トリガーごとに
変えられる値です。

## 宣言する

```yaml
params:
  - name: load_full
    type: boolean
    default: "false"

  - name: start_date
    type: string
    pattern: '^\d{4}-\d{2}-\d{2}$'

steps:
  - id: run
    run: dbt build --vars '{"load_full":"{{ .load_full }}"}' --select bronze+
```

| フィールド | | |
|---|---|---|
| `name` | **必須** | テンプレートと `--param` で使う名前 |
| `type` | | `string`、`boolean`、`integer`、または複数の値を持つ `list|<type>` |
| `default` | | トリガーが値を渡さないときに使われる値 |
| `pattern` | | 値が一致しなければならない正規表現 |
| `title` | | フォームに表示されるラベル(60 文字まで)。なければ name |
| `group` | | フォーム内でパラメータをまとめる(60 文字まで) |
| `warn` | | デフォルト以外の値が何を招くか。フィールドの下に表示される(200 文字まで) |
| `uses` | | パラメータが対象とするコネクタのキー(`bigquery`、`postgres`)。ロゴの表示に使われる |

**`default` のない**パラメータは必須です。それを省略したトリガーは、どのステップも実行する前に
失敗します。

`title`、`group`、`warn`、`uses` はフォームがパラメータを表示する方法であり、
**値として許されるものは何も変えません**。それぞれ 1 行で、`uses` は小文字、数字、`-` です。
それ以外のものは publish で拒否され、パラメータ名が示されます。

```yaml
params:
  - name: load_full
    type: boolean
    default: "false"
    title: Full load
    group: Load
    warn: reprocesses the whole table; it costs
    uses: bigquery
```

## 値のリスト

`list|<type>` は、多くの値を持つパラメータを宣言します。ロードするテーブル、再構築するレイヤー、
backfill するウィンドウなどです。

```yaml
params:
  - name: tables
    type: list|string
    default: "users,orders"

  - name: layers
    type: list|string
    enum: [bronze, silver, gold]     # an enum restricts each ITEM
    default: "bronze"

  - name: days
    type: list|integer
    default: "1,7,30"
```

要素には `string`、`integer`、`boolean` を指定できます。各アイテムは個別に検証されるため、
`list|integer` に `1,x,30` を渡すと、値全体ではなく**アイテム 2** を示して失敗します。

どこでも**カンマ区切りの 1 つの文字列**です。データベースでも、`BREVIS_RUN_PARAMS` でも、
コマンドでも同じです。そのため、ステップでは特別な処理がまったく要らずに動きます —

```yaml
run: dbt build --select {{ .tables }}      # dbt build --select users,orders
```

— そして、アイテムの中にカンマを含められない理由でもあります。空のアイテムと重複したアイテムも
拒否されます。重複があると、ステップがアイテムごとに行うことが黙って倍になってしまうからです。

### ステップ内で配列として

```go
for _, table := range p.Run.ParamList("tables") {
    load(table)
}
```

```python
from brevis import run

for table in run.param_list("tables"):
    load(table)
```

どちらも、誰も値を入れなかったパラメータに対しては、空文字列 1 つを持つリストではなく
`[]` を返します。「やることがない」ものを反復したら、何も起こらないべきだからです。アイテムは
文字列として返ります(`list|integer` も同様)。数値をどうするかは、ライブラリよりステップのほうがよく
知っているからです。

### インターフェースでは

`enum` を持つリストはチェックボックスとして描画されます。これは JavaScript を必要とせず、ブラウザは
チェックされたボックスごとに 1 つの値を送信します。`enum` がなければ、アイテムが削除可能なチップに
なるテキストフィールドになり、JavaScript をオフにすると `users,orders` を保持するフィールドの
ままになります。これはまさにサーバーが期待する形です。

### コマンドラインでは

```bash
brevis run wf.yaml --param tables=events,sessions --param days=1,30
```

:::note 古いエンジンでは読めません
型はワークフローとともに保存されます。この機能より前のエンジンは、`list|string` を未知の型として
publish 時に拒否します。これを使うワークフローを publish する前に、エンジンをアップグレードして
ください。
:::

## コマンドで使う

値は、ドット付きの二重波括弧によるテンプレートを通じて `run` に入ります。

```yaml
run: ./load.sh --since {{ .start_date }}
```

## コマンドラインで渡す

```bash
brevis run wf.yaml --param load_full=true
brevis run wf.yaml --param load_full=true --param start_date=2026-01-01
```

`--param` は繰り返し指定できます。`=` のない入力は**エラー**です。

```
erro: --param "load_full": use chave=valor
```

これは意図的です。値を忘れた `--param load_full` はデフォルトで実行され、オペレータは
意図したパラメータで backfill が行われたと思い込むことになるからです。

## backfill では

値は範囲内の**すべての** slot に適用されます。これが中心的なユースケースです。
「1 月全体を `load_full=true` で再処理する」。

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

## インターフェースでは

`params` を持つワークフローでは、シンプルなトリガーボタンの代わりに**フォーム**が表示されます。
フィールドは宣言から作られ、`pattern` は送信前に検証されます。

## ステップの環境では

run パラメータは、各ステップの**環境**にもプレフィックス付きで入ります。これは、
[SDK](/docs/sdk/) で書かれたフェッチャーが、引数として何も受け取らずにそれらを参照できるように
するためのものです。

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    if p.Run.Params["load_full"] == "true" {
        p.Source.URL += "&full=1"
    }
    return nil
},
```

## 自動パラメータ

すべての run は**自動パラメータ**も持ちます。エンジンが自分で算出する値です。誰も宣言しておらず、
すべての run が持ち、run の画面に表示されます。

重要なのは `adjusted_at` で、これが取り除くバグには名前を付ける価値があります。
フェッチャーが `now()` を読み、そのウィンドウ分を引いて、ベンダーに直近 2 時間を要求します。
時間どおりに始まった run ならそれで正しいのですが、キューが 40 分遅らせた run では 40 分ずれていて、
その 40 分は**どの run にも属しません**。次の slot も自分の `now()` を読むからです。何も失敗せず、
データが単に欠け、数週間後に見つかります。

そこでエンジンは、代わりに時計を渡します。`now()` を読むところで `adjusted_at` を読んでください。

| パラメータ | | |
|---|---|---|
| `adjusted_at` | 常にある | この run が読むべき時計。slot があればその slot、なければ開始時刻 |
| `date` | 常にある | `adjusted_at` を `YYYY-MM-DD`(UTC)にしたもの — パーティション、フォルダ、`WHERE` に使う |
| `delay_seconds` | 常にある | この試行が slot に対してどれだけ遅れたか。slot のない手動 run では `0` |
| `previous_error` | 常にある | この run の前の run が成功しなかった |
| `scheduled_at` | slot がある場合 | cron が要求した slot |
| `started_at` | | この試行が始まった時刻。リトライで動く |
| `interval_start` | slot がある場合 | **前の** slot |
| `interval_end` | slot がある場合 | この slot(**含まない**) |
| `previous_success_at` | | 最後に成功した run の slot |

run が slot を持つのは、スケジュールが作ったとき、backfill が作ったとき、または手動で**過去の slot に
対して**トリガーされたときです。トリガーの `slot` フィールド(`2026-10-27T04:00:00Z`)は、その slot を
その slot のパラメータで再び実行します。cron が発火しない slot は拒否され、その直前で最も近い slot が
示されます。

**ここでのタイムスタンプはすべて UTC** で、`Z` 付きの RFC 3339 で書かれ、run の画面も UTC で
表示するため、ページと `$BREVIS_AUTO_ADJUSTED_AT` が食い違うことはありません。`date` は
**UTC の日付**であり、slot が UTC の午前 0 時をまたぐワークフローではローカルの日付とは異なります。
`America/Sao_Paulo` の `0 22 * * *` では、`date` は翌日になります。Airflow の `ds` も同じ理由で
同じように振る舞います。slot は瞬間であり、瞬間が 1 つの日付を持つのはタイムゾーンを選んだ後に
限られるからです。

`interval_start` と `interval_end` は履歴ではなく **cron** から決まります。3 月の slot の
backfill は、このワークフローの run が今日たまたま示すウィンドウではなく、3 月が持っていた
ウィンドウを生成します。`[start, end)` を要求するパイプラインは、どれだけ遅れて実行されても、
どれだけ頻繁にリトライしても、重複も隙間も生じません。

これらは**スナップショット**で、run の開始時に計算されて run に保存されます。`previous_error` は
この run が始まった瞬間についての事実です。明日、前の run がリトライされて成功した後に再計算すると、
同じ名前で別の問いに答えることになります。

### 読み取り方

シェルのステップでは、値ごとに 1 つの変数があるので、パースは不要です。

```yaml
steps:
  - id: extract
    run: |
      curl "https://api.example.com/events?since=$BREVIS_AUTO_INTERVAL_START&until=$BREVIS_AUTO_INTERVAL_END" \
        > "/data/$BREVIS_AUTO_DATE.json"
```

全体のセットは、1 つの JSON オブジェクトとして `$BREVIS_AUTO_PARAMS` にもあり、一度でパースしたい
ステップのために用意されています。

Go では、[SDK](/docs/sdk/) が代わりに読み取ります。

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    start, end, ok := p.Run.Auto.Window()
    if !ok { // no schedule: fall back to a fixed window
        start, end = p.Run.Auto.Now().Add(-24*time.Hour), p.Run.Auto.Now()
    }
    p.Source.URL += "&from=" + start.Format(time.RFC3339) +
        "&to=" + end.Format(time.RFC3339)
    return nil
},
```

`Auto.Now()` は、`time.Now()` の代わりに読むべき時計です。エンジンの外、たとえば誰かが手で
実行したフェッチャーでは、それは実際の壁時計*そのもの*なので、ローカル開発のために特別な
場合分けをする必要はありません。

Python では、`pip install brevis` が同じ値を読み取ります。

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

`window()` は、ワークフローにスケジュールがない場合、ゼロ時刻のペアではなく `None` を返します。
エポックからのクエリはすべてを選択してしまい、その失敗は黙って起きてはならないからです。
上の `or` がフォールバックのすべてです。

### dlt では、読むものは何もない

[dlt](https://dlthub.com) は、宛先に書き込む状態の中に独自のカーソルを保持します。その状態は
**前方向にしか**進まないため、古い slot を再実行するまでは問題ありません。再実行すると、カーソルは
今日を指したままなので、backfill は今日のウィンドウを読んでしまい、これに対する dlt 自身の答えは
状態を捨てることです。

Brevis が渡すウィンドウは cron から決まるので、3 月の run は、何度リトライしても 3 月の
ウィンドウを生成します。dlt はそれを受け取ります。名前は dlt 自身のもので、Airflow を探しに行く
前に読み取られ、エンジンはスケジュールされたすべての run でそれらを設定します。

```python
@dlt.resource(primary_key="id")
def events(
    updated_at=dlt.sources.incremental(
        "updated_at",
        initial_value=datetime(1970, 1, 1, tzinfo=timezone.utc),
        allow_external_schedulers=True,   # this line is the whole integration
    )
):
    yield from api.events(since=updated_at.start_value, until=updated_at.end_value)
```

`allow_external_schedulers=True` がすべてです。`$DLT_INTERVAL_START` と `$DLT_INTERVAL_END` が
dlt の `initial_value` と `end_value` になり、両側とも半開区間です。ちょうど開始時刻にある行は
含まれ、ちょうど終了時刻にある行は含まれません。これによって、連続する slot の間に隙間も重複も
生じません。

`end_value` を持つインクリメンタルは、**永続化された状態に触れません**。ここが重要な点です。
1 つの slot の再実行は冪等であり、3 月の backfill が、夜間の run が依存するカーソルを動かすことは
ありません。

動作する前に知っておくべきことが 2 つあります。

- **カーソルは文字列ではなく datetime でなければなりません。** dlt は比較のために `text` のカーソルを
  変換してくれず、スケジューラへの参加自体を拒否し、その際に出る `JoinSchedulerError` は
  それを明確には伝えません。リソース上で `add_map` を使って変換するか、上のように
  `initial_value` に型を付けてください。
- **スケジュールのないワークフローは何も渡しません。** すると dlt は、静かにカーソルへフォールバック
  するのではなく `ExternalSchedulerNotAvailable` を発生させます。パイプラインを両方の方式で動かし
  たい場合は、`allow_external_schedulers` をオフにして、`run.window()` からウィンドウを自分で
  渡してください。

ステップに `tools: [dlt]` を宣言するのは別の話で、グラフにチップを描画するだけです。
ウィンドウはどちらにしても渡されます。

## スナップショット

パラメータは、実行時にワークフローから読まれるのではなく、**run の中に**保存されます。run は
トリガーされたときの入力を保持しているので、デフォルトが後で変わっても、1 月の実行のログには
引き続き 1 月の値が表示されます。

## 次のステップ

- [設定](/docs/configuration/) — プロセスの環境変数
- [SDK](/docs/sdk/) — フェッチャーが run のコンテキストを読む方法
- [ステップ間のコンテキスト](/docs/context/) — ステップが次のステップのために公開するもの
