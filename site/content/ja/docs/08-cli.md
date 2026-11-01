---
title: CLI
description: エンジンの 12 個のサブコマンドと、そのフラグ、引数、それぞれが必要とするもの。
group: リファレンス
order: 8
slug: cli
---

エンジンは複数のバイナリではなく、サブコマンドを持つ 1 つのバイナリです。API、スケジューラ、
ワーカーは同じシステムのロールであり、バイナリが 1 つなので、バージョン、イメージ、
ビルド経路もそれぞれ 1 つで済みます。

```
brevis [command] [flags]
```

| コマンド | データベース | `local` 以外での認証情報 | ロール |
|---|---|---|---|
| [`serve`](#serve) | **必要** | **必須** | API + インターフェース |
| [`scheduler`](#scheduler) | **必要** | **必須** | 両方のループ |
| [`migrate`](#migrate) | **必要** | **必須** | スキーマ |
| [`publish`](#publish) | **必要** | **必須** | ワークフローとスケジュールを保存する |
| [`backfill`](#backfill) | **必要** | **必須** | 範囲を再処理する |
| [`cancel`](#cancel) | **必要** | **必須** | run を停止する |
| [`run`](#run) | 不要 | — | 今すぐ、インスタンス自身で実行する |
| [`validate`](#validate) | 不要 | — | ワークフローの YAML を検証する |
| [`brand`](#brand) | 不要 | — | ブランディングの YAML を検証する |
| [`hash`](#hash) | 不要 | — | パスワードのハッシュを生成する |
| [`version`](#version) | 不要 | — | バージョン、コミット、ビルド |

最初の 6 つは設定を読み込み、`BREVIS_DATABASE_URL` がないと**起動時に失敗**します。残りの 5 つは
そうではありません。だから `validate` は、Postgres のない CI でも動作します。

この 6 つには認証情報のルールも適用されます。`BREVIS_ENV` が `local` 以外の場合、
3 つの認証用変数なしで起動すると**起動エラー**になります。

---

## serve

HTTP API とインターフェースを起動します。フラグはなく、すべて環境から取得します。

```bash
brevis serve
```

`SIGINT`/`SIGTERM` で graceful に停止するため、デプロイで処理中のリクエストが切断されることは
ありません。このプロセスは**スケジュールを具体化しません**。それには `brevis scheduler` を
並行して動かす必要があります。

`api` イメージの `CMD` です(distroless、シェルなし)。

---

## scheduler

両方のループです。スケジューラがスケジュールを run に具体化し、ディスパッチャがキューから取り出して
実行します。

```bash
brevis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| フラグ | 型 | デフォルト | |
|---|---|---|---|
| `--interval` | duration | `10s` | サイクルの間隔 |
| `--concurrency` | int | `5` | 同時に実行される **run** の数 |
| `--max-pods` | int | `5` | 同時に実行される**ステップ**の合計数 |

`worker` イメージの `CMD` です(シェル付きの alpine。`run:` のステップにシェルが必要なため)。

---

## alert

スケジューラが記録したアラートを配信します。`serve` と `scheduler` に並ぶ 3 つ目のプロセスです。

```bash
brevis alert --interval 1s --max-attempts 6
```

| フラグ | 型 | デフォルト | |
|---|---|---|---|
| `--interval` | duration | `1s` | 配信サイクルの間隔 |
| `--max-attempts` | int | `6` | アラートを未配信として記録するまでの試行回数 |

**これが存在するのは、アラートが書き込まれる場所のためであって、配信がプロセスに値するからでは
ありません。** スケジューラは、アラートを正当化する失敗と同じトランザクションでアラートを記録します。
run が試行回数を使い切ってアラートが存在するか、どちらも起きなかったかのどちらかです。そのうえで
誰かがそのテーブルを消化する必要があり、それは Slack の障害にも自分自身の再起動にも耐えなければ
なりません。これ以前は、Webhook が失敗するとログに 1 行残るだけで、アラートは単に消えていました。

チャンネル(`BREVIS_SLACK_WEBHOOK`)が設定されていないと、起動を拒否します。配信先のない
配信プロセスは、アウトボックスが埋まるのと同じ速さで「未配信」に消化してしまい、その行は
Slack に拒否された場合とまったく同じに見えるからです。

諦めたアラートは**保持されます**。「発生、未配信、4 回試行、Slack から 403」という行が重要なのは、
誰かが届かないメッセージを待っているケースだからです。

`worker` イメージの `CMD` です。

---

## report

実行内容の定期サマリを送信します。

```bash
brevis report --window 168h              # sends it
brevis report --window 168h --dry-run    # prints it, sends nothing
```

| フラグ | 型 | デフォルト | |
|---|---|---|---|
| `--window` | duration | `168h` | どこまで遡って調べるか |
| `--dry-run` | bool | `false` | 送信せずに出力する |

ループではなく**コマンド**であり、これがレポートとアラートの違いです。CronJob から実行されるため、
新しいループも、新しい状態も、リーダー選出も不要です。クラスタにはすでにスケジューラがあります。
`--dry-run` は、週次のメッセージを信頼できるようになるための手段であり、送られるはずだった内容を
読むことができます。

アラートのアウトボックスは**経由しません**。両者が共有するのは配信チャンネルだけです。アラートを
失うことは誰にも聞こえない障害であり、ある週のサマリを逃しても次の週のサマリが来るだけです。

メッセージには、run 数、成功数、失敗数、最も多く失敗したパイプライン、最も時間がかかったもの、
SDK が報告した行数とバイト数が含まれます。CPU やメモリは**含まれず**、ゼロとして出力することも
しません。エンジンはそれらを収集しておらず、メッセージにはそれらが実際にある場所が示されます。

空のウィンドウでは、100% と報告するのではなく、その旨を伝えます。何もないところから出した成功率は、
レポートが出力できる中で最も安心させる数字であり、最も真実でない数字です。空の週は、
たいていスケジューラが止まっていたことを意味します。

`deployments/kubernetes/report.yaml` が、これを週次で実行します。

---

## migrate

```bash
brevis migrate up      # applies what is missing
brevis migrate down    # reverts the last one
brevis migrate status  # shows the state
```

引数をちょうど 1 つ取ります。マイグレーションはバイナリに埋め込まれており、専用のサブコマンドで
適用されます。**`serve` がスキーマを変更することはありません**。

---

## validate

1 つ以上のワークフローファイルを検証します。ファイル**またはディレクトリ**を受け取ります。

```bash
brevis validate examples/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

データベースには触れず、サーバーも起動しないため、エディタでも CI でも実行できます。
ゼロ以外の終了コードで終了し、失敗の数を数えます。

---

## run

ワークフローを**今すぐ、インスタンス自身で**実行します。キューも、データベースも、
スケジューラも使いません。

```bash
brevis run examples/hello.yaml
brevis run wf.yaml --param load_full=true --retries 3 --timeout 5m
```

| フラグ | 型 | デフォルト | |
|---|---|---|---|
| `--param` | 繰り返し可 | — | 宣言済みパラメータの `key=value` |
| `--workdir` | string | ファイルのディレクトリ | ステップの作業ディレクトリ |
| `--retries` | int | `1` | ステップあたりの試行回数(`1` = リトライなし) |
| `--timeout` | duration | `0` | ステップごとのタイムアウト(`0` = 制限なし) |

`=` のない `--param` は、警告ではなく**エラー**です。値を忘れた `--param load_full` は
デフォルトで実行され、オペレータは値が適用されたと思い込むことになるからです。

:::warning 4 つの制限
`run` は `BREVIS_ENV=local` のときだけ動作します。`image:` のあるステップはインスタンス自身で
実行され、その旨が表示されます。Go のタスクレジストリはないため、未登録のタスクを指す `action:` は、
利用可能なものを示して失敗します。そして `run` は `BREVIS_HOSTS` を読まないため、`host:` のある
ステップはここではまったく実行できません。変数を設定していても、ホスト名を示して失敗します。
:::

---

## publish

ワークフローとそのスケジュールをデータベースに保存します。ファイルまたはディレクトリを受け取ります。

```bash
brevis publish examples/hello.yaml
brevis publish workflows/ --project acme --prune
```

| フラグ | 型 | デフォルト | |
|---|---|---|---|
| `--project` | string | `default` | プロジェクトの slug |
| `--prune` | bool | `false` | リストにないワークフローをプロジェクトから削除する |

`--prune` を付けても、削除されたワークフローの履歴は保持されます。

---

## backfill

公開済みのワークフローの過去の slot を具体化します。**キューに追加するだけで、実行は
しません。**

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

| フラグ | | |
|---|---|---|
| `--from` | **必須** | 開始日、`YYYY-MM-DD` |
| `--to` | **必須** | 終了日、`YYYY-MM-DD` |
| `--param` | | 範囲内の**すべての** slot に適用される |

`--to` はその日全体を含みます。内部的には、終了がその日付の `23:59:59` になります。

---

## cancel

まだ待機中、または実行中の run をキャンセルします。イベントフィードには、キャンセルしたのは
`cli` と表示されます。

```bash
brevis cancel 3f1c9a52-6d0e-4b7a-9c41-2b8e5d7f0a13
```

| 応答 | 何が起きたか |
|---|---|
| `canceled` | run はキューで待機中でした。キューから外れ、実行されません |
| `cancel requested` | run は実行中です。ディスパッチャが `--cancel-poll`(2 秒)以内にステップを停止します |
| `already ended: <status>` | run はすでに終了しています。何も変わりません |

キャンセルされた run はリトライされません。存在しない id はエラーです。

---

## gateway

gateway が `/data` にどのように現れるか。その設定が、意図的な操作として公開されます。トラフィックが
公開されることは決してなく、実行時に公開されることもありません。

```bash
gateway describe gateway.yaml > manifest.json   # the gateway's own binary
brevis gateway publish manifest.json
brevis gateway unpublish events_gateway
```

`publish` は何かを書き込む前にマニフェスト全体を検査し、最初の問題のストリームとロールを示します。
そして、その gateway の宛先を 1 つのトランザクションで置き換えます。`unpublish` は廃止された
gateway を削除し、その名前の gateway が公開されていなければエラーになります。

`brevis-sql-api` が データ → Endpoints にどのように現れるか。各エンドポイントが何を受け取り何を
読むかが、意図的な操作として公開されます。トラフィックが公開されることは決してありません。

```bash
brevis-sql-api describe --config api.yaml --out manifest.json   # the API's own binary
brevis endpoints publish manifest.json
brevis endpoints unpublish metrics-api
```

`publish` は何かを書き込む前にマニフェスト全体を検査し、その API のエンドポイントを 1 つの
トランザクションで置き換えます。認証情報のような形をした名前、デフォルト値、注記があれば、
そのすべてが拒否されます。

`brevis publish` のモードではなく独立したコマンドにしているのは、ワークフローと gateway では
集合のセマンティクスが異なるためです。`publish --prune` が gateway の行に及ぶことは
あってはなりません。

---

## prune

保持ポリシーを適用します。Brevis の中で何かを削除する**唯一の**ものであり、自動的に実行されることは
決してなく、オペレータがスケジュールします。

```bash
brevis prune --dry-run          # what would go, changing nothing
brevis prune
brevis prune --purge-after 0    # trim only, never delete a run
```

| フラグ | デフォルト | |
|---|---|---|
| `--trim-after` | `720h` | ログ、フェーズ、公開された出力を空にする |
| `--purge-after` | `8760h` | run を完全に削除する。`0` は削除しない |

---

## brand

サーバーを起動せずに、ブランディングファイルを検証します。

```bash
brevis brand brand.yaml
```

```
  ok    Brevis · Orchestration
        logo      /assets/logo.svg  (built-in symbol)
        accent    (the product's own)
        Powered by Brevis
```

起動時との挙動の違いが 1 つあります。**ファイルがないことはエラーです。** `serve` では、ファイルが
ないことは「デフォルトのアイデンティティを使う」という意味ですが、パスの検証を依頼した人は、
それが存在しないと教えてもらうことを期待するからです。

---

## hash

`BREVIS_AUTH_PASSWORD_HASH` の値を生成します。

```bash
brevis hash
```

パスワードは、引数からではなく、**エコーなし**でターミナルから読み取られます。引数は任意の
プロセスの `ps` に表示され、シェルの履歴にも残るからです。12 文字より短いパスワードは
拒否されます。

ハッシュだけが **stdout** に出力され、ラベルは **stderr** に出力されるため、
`brevis hash > hash.txt` で保存されるのは必要なものだけです。

---

## version

```bash
brevis version
```

```
brevis 0.11.2
  commit  bb832ff
  build   2026-09-05T12:00:00Z
  go      go1.25.7 darwin/arm64
```

`go build` で直接ビルドすると、`brevis dev` と表示され、コミットも日付もありません。誰かが
おかしな挙動を報告したとき、これをリリース成果物と見分けられることが重要です。
