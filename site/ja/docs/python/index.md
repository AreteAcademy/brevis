# Python

> pip install brevis — ステップ間のコンテキスト、run の時計、パイプライン独自のメトリクスを、依存関係ゼロで。

*https://brevis.sh/ja/docs/python/ · brevis.sh docs (ja)*

---

```bash
pip install brevis
```

```python
from brevis import context, metrics, run

bucket = context.get("extract.bucket")
since, until = run.window()          # the window this run covers

context.set(rows=48213, watermark="2026-09-07T03:00:00Z")
metrics.set("rows_loaded", 48213)    # reaches the engine's /metrics
```

**依存関係は一切ありません。** 環境変数を 1 つ読み、ファイルを 1 つ書き、stdout に 1 行出力するだけです。
Python 3.10 以降が必要です。

Go SDK の移植ではありません。ドライバも、ページネーションも、ingestion id もありません。
チームが pandas、Polars、dbt を使いながら、オーケストレータの存在意義であるものを得られるように
するために存在します。

## コンテキストを読む

キーは常に、それを公開したステップで修飾されます。

```python
context.get("extract.bucket")            # the value
context.get("extract.nope", default=0)   # absent is not an error
context.of("extract")                    # everything that step published
context.published()                      # what THIS step has published so far
```

ステップなしの `context.get("bucket")` は**拒否され**、それを公開したステップが示されます。ステップは
分離されており、`extract` と `transform` の両方が、どちらも失うことなく `bucket` を公開できます。
キー単体では、2 つが公開した瞬間にその分離が失われます。検索して 1 つを選ぶのは偶然による優先順位で、
黙って動かなくなるまでは動きます。

このステップが依存していないステップを読むとエラーになり、メッセージが修正方法を指し示します。

## 書き込む

```python
context.set(name="Daniel")
context.set(label="Name")
# both survive: {"name": "Daniel", "label": "Name"}
```

呼び出しは**マージ**されます。同じキーの 2 回の書き込みは最後のものを保持し、異なるキーは最初のものに
影響しません。**ステップの引数はありません。** プロセスが書き込める名前空間は自分のものだけで、
これによって分離がルールではなく構造的なものになります。

プロセスが終了するまでディスクには何も届かないので、強制終了したステップは何も公開しません。
それは正しい動作です。クラッシュしたステップの出力は、終わっていない仕事を記述しているからです。

## この run が読むべき時計

すべての run は**自動パラメータ**も持ちます。エンジンが自分で算出する値です。誰も宣言しておらず、
すべての run が持ち、パイプラインがそうでなければ `datetime.now()` で答える問いに答えます。

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

:::warning なぜ `datetime.now()` ではないのか
スケジュールされた run では、`run.now()` は **slot** です。`datetime.now()` を読んで 2 時間を引く
フェッチャーは、時間どおりに始まった run では正しいのですが、キューが遅らせた run では 40 分ずれて
います。その 40 分は**どの run にも属しません**。次の slot も自分の `now()` を読むからです。何も失敗せず、
欠落は数週間後に見つかります。
:::

| | |
|---|---|
| `run.now()` | 時計。slot、または run が始まった時刻 |
| `run.window()` | `(start, end)`(end は含まない)、または `None` |
| `run.auto()` | 後述の自動パラメータ |
| `run.context()` | id、試行回数、トリガー、パラメータ、auto |
| `run.params()` · `run.param(name)` | ワークフローで宣言されたパラメータ |
| `run.map_index()` · `run.map_value()` | `for_each:` におけるインスタンス |

`window()` は履歴ではなく **cron** から決まります。3 月の slot の backfill は、3 月が持っていた
ウィンドウを生成します。`[start, end)` を要求すれば、run がどれだけ遅れても、どれだけ頻繁に
リトライしても、重複も隙間も生じません。

ワークフローにスケジュールがない場合は、ゼロ時刻のペアではなく `None` を返します。エポックからの
クエリはすべてを選択してしまい、その失敗は黙って起きてはならないからです。上の `or` が
フォールバックのすべてです。

### 自動パラメータ

| フィールド | |
|---|---|
| `date` | `"2026-09-08"` — パーティション、フォルダ、`WHERE` に使う |
| `adjusted_at` | `now()` の代わりに読む時計 |
| `scheduled_at` | cron が要求した slot。手動 run では `None` |
| `started_at` | **この試行**が始まった時刻。リトライで動く |
| `delay_seconds` | この試行がどれだけ遅れたか。負にはならない |
| `interval_start` · `interval_end` | ウィンドウ(end は含まない) |
| `previous_error` | この run の前の run が成功しなかった |
| `previous_success_at` | 最後に**成功した** run の slot — `previous_error` を行動に結びつけるもの |

スクリプトを手動で実行すると、`run.now()` は壁時計**そのもの**で、`window()` は `None` なので、
ローカル開発のために書く分岐はありません。

## マップされたステップの 1 つのインスタンス

[`for_each:`](/ja/docs/workflows/index.md) の下では、エンジンは要素ごとに 1 つのプロセスを実行し、
それぞれにどの要素を受け取ったかを伝えます。

```python
partition = run.map_value()
if partition is None:
    raise SystemExit("this step is meant to run under `for_each:`")

load(f"/data/{partition}.csv")
```

JSON の文字列は**引用符なし**で届きます。`["2026-01"]` に対する `for_each` は、ステップに
`"2026-01"` ではなく `2026-01` を渡します。それ以外(オブジェクト、数値、リスト)は JSON として届くので、
`json.loads(run.map_value())` を使います。

マップされていないステップでは、どちらも `None` です。`-1` や `""` ではなく `None` なのは、
センチネル値は誰かがいずれ算術演算を行う数値であり、空文字列は、空文字列*である*要素と
区別がつかないからです。

マップされたステップは、下流に**コンテキストを公開しません**。4 つのインスタンスで 1 つのキーを
共有することはできないからです。何かを渡す必要があるステップは、ファイルに書くか、その後のステップが
着地したものを数えます。

## パイプラインだけが知るメトリクス

エンジンは、見えるものをすでに計測しています。所要時間、試行回数、キューの深さです。エンジンが
知る手段を持たないのは、何行が着地したか、ベンダーが何行を拒否したか、何バイトが破棄されたかです。
それを知っているのはステップであり、ライブラリがそれを運びます。

```python
from brevis import metrics

metrics.set("rows_loaded", 48213)          # a gauge: the last value wins
metrics.inc("vendor_rejected_total")       # a counter: it adds up
metrics.inc("bytes_discarded_total", 4096)
```

メトリクスは、スケジューラのポートにある**エンジンの** `/metrics` から、ワークフローとステップの
ラベル付きで出力されます。

```
brevis_step_rows_loaded{workflow="daily_sales",step="load"} 48213
brevis_step_vendor_rejected_total{workflow="daily_sales",step="load"} 5
```

| | |
|---|---|
| `metrics.set(name, value)` | ゲージ。ステップの終了後も、次の run が別の値を書くまで値は残ります |
| `metrics.inc(name, by=1)` | カウンタ。ステップをまたいで、run をまたいで加算されます |

### ポートを開かない理由

**ステップはスクレイプできない**からです。`:9090` はスケジューラと API のものであり、
安定したアドレスを持つ長寿命のプロセスで、15 秒から 60 秒ごとにスクレイプされます。ステップは専用の
pod で起動し、40 秒動いて終了します。開いたポートがスクレイプされることは決してないか、
運よく一度あるだけです。

これに対する既知の答えは 3 つあります。Pushgateway(運用するコンポーネントが増え、誰かが削除するまで
系列が残る)、OTLP コレクタ(コンポーネント**と**このライブラリへの依存の両方が増える。このライブラリには
依存がなく、今後も持たない)、あるいは**オーケストレータ**に運ばせることです。Brevis はすでに
すべてのステップの stdout を読んで `@brevis:` の行を探しており(SDK のフェーズがグラフに届くのは
そのためです)、メーターと Prometheus エンドポイントもすでに持っています。

そこで、ライブラリは **stdout に 1 行**書き、残りはエンジンが行います。メトリクスは、エンジンが
何を実行していたかを知っているため、ワークフローとステップのラベル付きで無料で届きます。Pushgateway
経由では、このライブラリが自分でラベルを付けなければならず、それを間違えるでしょう。

書き込みは `print(flush=True)` です。エンジンがそのパイプを**リアルタイムで**読むからです。
バッファされた行はプロセスの終了時に届きます。1 時間動くステップでは 1 時間遅れになり、
kill されたステップでは決して届きません。

### メトリクスが拒否するもの

**無効な名前は、はっきりと拒否されます。** Prometheus は英字、数字、アンダースコアを受け付け、
先頭の文字は数字にできません。拒否される名前は壊れたメトリクスを作るのではなく、Prometheus が
**パースを拒否する**出力を作り、**スクレイプ全体**が失われ、他のすべてのメトリクスも一緒に失われます。
そのため `rows-loaded` は、誰も書いていない名前で届くのではなく、間違いが起きたここで
`MetricError` を発生させます。

| 拒否されるもの | 理由 |
|---|---|
| `rows-loaded`、`rows loaded`、`2rows` | Prometheus がパースせず、スクレイプ全体が失われる |
| `metrics.set("ok", True)` | Python では `bool` は `int` であり、`1` を報告することは、それを知っているかどうかで意味が決まる数値になる。状態ではなく、件数や所要時間を報告する |
| `metrics.inc("x", -1)` | カウンタは増えるだけ。それに対するすべての `rate()` がそれを前提としており、破綻は数週間後にグラフに現れる |

スクリプトを手動で実行しても、その行は stdout に出力され、それ自体として読めます。何も収集せず、
何も失敗しません。ノート PC での `context.set()` と同じです。

## 拒否するもの

| | 理由 |
|---|---|
| 4096 バイト超 | プラットフォームは拒否せず**切り詰める**ため、大きすぎるオブジェクトは半分に切られて届き、下流では「何も公開されなかった」と読める |
| JSON ではない値 | `datetime` は、誰がシリアライズするかによって `"..."` になったり消えたりする。エラーはキーを示し、`.isoformat()` を提案する |
| 文字列ではないキー | `{1: "x"}` は `{"1": "x"}` として戻り、検索が外れる |

上限はプラットフォームの数値であり、私たちのものではありません。「タスク間のコンテキスト」を提供する
機能はどれも同じ失敗をします。コンテキストしか収まらない場所に、誰かが**データ**を置いてしまうのです。
4 キロバイトに入るのは、ウォーターマーク、件数、パーティションのリストです。参照を公開し、
行はあるべき場所に残してください。

:::danger これはシークレットストアではありません
公開されたものはすべて、その run を見られる人なら誰でも見えます。pod のステータスにも run の履歴にも
含まれます。署名付き URL ではなく、パスを公開してください。
:::

## 手動で実行する

Brevis の外には入力がなく、出力を読むものもありません。`get()` はデフォルト値を返し、`set()` は
受け取って破棄し、ライブラリはそれを一度だけ INFO で伝えます。**例外は発生しません**。手動で
実行できないスクリプトは開発できないからです。検証は引き続き実行されるので、ノート PC で動く値は
本番でも動きます。

## ステップのテスト

契約全体が 2 つの環境変数なので、モックするものは何もありません。

```python
def test_my_step(monkeypatch, tmp_path):
    monkeypatch.setenv("BREVIS_INPUT", '{"extract": {"rows": 42}}')
    monkeypatch.setenv("BREVIS_OUTPUT", str(tmp_path / "out"))
    assert context.get("extract.rows") == 42
```

## バージョン

ライブラリは、エンジンや Go SDK とは別に、独自にバージョン管理されます。

:::warning `0.2.0` は使わないでください
PyPI にホイールのみで公開されてしまいました。`0.2.1` は同じコードで、完全な状態です。
:::

`brevis.run` が読むものを得るには、**`v0.9.0` 以降**のエンジンが必要です。それより古いエンジンでは
すべてのフィールドが空で、`run.now()` は壁時計になります。エンジンの外での動作と同じです。

## 次のステップ

- [クライアントライブラリ](/ja/docs/libraries/index.md) — 契約とその他の言語
- [ステップ間のコンテキスト](/ja/docs/context/index.md) — 概念
- [パラメータ](/ja/docs/parameters/index.md) — 宣言するもの
