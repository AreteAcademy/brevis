---
title: 設定
description: すべての環境変数について、デフォルト、形式、未設定の場合に何が起きるか。
group: リファレンス
order: 10
slug: configuration
---

すべての設定は**環境**から取得され、起動時に一度だけ読み込まれます。その後に環境を参照する
ものはありません。実行中に設定が変わるプロセスは、誰にも再現できない挙動を示すプロセスです。

## 必須

| 変数 | |
|---|---|
| `BREVIS_DATABASE_URL` | Postgres。データベースを使う 5 つのサブコマンドでは、未設定は**起動エラー**です |

## プロセス

| 変数 | デフォルト | |
|---|---|---|
| `BREVIS_ENV` | `production` | `local` ではテキスト形式でログを出し、インターフェースを開放します。それ以外(未設定を含む)では認証情報が必須になり、JSON でログを出します。開発時は `BREVIS_ENV=local` を設定してください |
| `BREVIS_HTTP_ADDR` | `:8080` | リッスンするアドレス |
| `BREVIS_LOG_LEVEL` | `info` | |
| `BREVIS_SHUTDOWN_TIMEOUT_SECONDS` | `15` | 整数。数値でない値は起動エラー |
| `BREVIS_DB_MAX_CONNS` | ドライバーのデフォルト, `max(4, CPUs)` | 各プロセスの Postgres プールの上限。数値でない値や負の値は起動エラー |
| `BREVIS_CONSOLE_STATEMENT_TIMEOUT_SECONDS` | `0`(オフ) | コンソール(`brevis serve`)のみ。実行が長引いたステートメントを Postgres が中止し、暴走した一覧クエリが接続を握り続けないようにします。プールが起動パラメーターを受け付ける環境では `30` を推奨します。トランザクションモードの PgBouncer は接続を拒否するため、デフォルトではオフです。接続の起動パラメーターとして送られるので、トランザクションモードの PgBouncer では `ignore_startup_parameters = statement_timeout` が必要です |
| `BREVIS_RETENTION_TRIM_DAYS` | `0`(オフ) | オプトインの定期プルーン。`brevis scheduler` が実行し、この日数より古い run のログ、フェーズ、出力を空にします。`brevis prune --trim-after` と同じ効果です。`0` = scheduler はプルーンしません |
| `BREVIS_RETENTION_PURGE_DAYS` | `0`(なし) | 上記と併せて、この日数より古い run も削除します。trim の値以上である必要があります |
| `BREVIS_RETENTION_INTERVAL_HOURS` | `24` | 定期プルーンの実行間隔(起動時に 1 回、その後は N 時間ごと) |
| `BREVIS_BRAND_FILE` | `brand.yaml` | ブランディング。なければデフォルト |
| `BREVIS_UI_URL` | — | アラート内の run リンクのベース |
| `BREVIS_METRICS_ADDR` | — | Prometheus メトリクスのポート。空なら公開しない。[オブザーバビリティ](/docs/observability/)を参照 |
| `BREVIS_SLACK_WEBHOOK` | — | 失敗アラートの宛先。空なら誰にも通知されない |

## 認証

| 変数 | |
|---|---|
| `BREVIS_AUTH_USER` | インターフェースのユーザー |
| `BREVIS_AUTH_PASSWORD_HASH` | `brevis hash` が生成する `pbkdf2-sha256$…` のハッシュ |
| `BREVIS_AUTH_SECRET` | セッションの署名用の 32 バイト以上(`openssl rand -base64 48`) |

3 つは全部そろえるか、全部設定しないかのどちらかです。

| 変数 | デフォルト | |
|---|---|---|
| `BREVIS_AUTH_CLIENT_IP_HEADER` | — | プロキシがクライアントのアドレスを設定するヘッダー。例: `X-Forwarded-For`。**最後**のエントリ(プロキシが追加したもの)が使われます。空 = 接続のアドレス |

サインインはクライアントのアドレスごと(IPv6 は /64 単位)に制限されます。3 回失敗するとそのアドレスは 2 秒間ロックされ、最大 15 分まで倍々に延びます。**ingress やロードバランサーの背後では、この変数を設定してください**(`api.ingress.enabled` が有効なときは Helm chart が設定します。`auth.clientIPHeader` で上書きできます)。設定しないと全クライアントがプロキシのアドレスを共有するため、1 人がパスワードを試しただけで全員がロックアウトされます。その場合、`BREVIS_ENV=local` 以外ではロックは 30 秒が上限となり、起動時に警告がログに出ます。この変数は、ヘッダーが信頼できるプロキシによって書き込まれ、クライアントが偽造できない場合にのみ設定してください。パスワードの検証はアドレスごとに同時に 1 件しか実行されないため、同じ NAT の背後の 2 人が同じ秒にサインインすると、一時的に 429 が返ることがあります。

:::danger 中途半端な設定は起動エラーです
ユーザー名を入れた人は、扉が閉まっていると信じています。インターフェースはパイプラインを
トリガーします。`POST /workflows/<slug>/trigger` は、ウェアハウスに書き込む `dbt build` を
実行します。インターネットに開放されていれば、ウェアハウスのリモコンになってしまいます。
そして、ログの警告ではその思い込みは解けません。正常に動いているプロセスのログは誰も読まないからです。
:::

`BREVIS_ENV=local` 以外では、認証情報なしの起動は拒否されます。

```
error: BREVIS_ENV=production requires a credential: set BREVIS_AUTH_USER,
BREVIS_AUTH_PASSWORD_HASH (generate one with `brevis hash`) and BREVIS_AUTH_SECRET
```

:::note 古い名前も引き続き使えます
2026-09-10 に、最後のポルトガル語の名前がプロジェクトのオペレータ側から消えたとき、4 つの変数の
名前が変更されました。`BREVIS_AUTH_USUARIO`、`BREVIS_AUTH_SENHA_HASH`、`BREVIS_AUTH_SEGREDO`、
`BREVIS_POD_MANTER_EM_FALHA` です。古い名前を設定しているインストールは引き続き動作し、
新しい名前を示す警告をログに出します。

新しい名前を追加してデプロイし、その後で古い名前を削除してください。両方が設定されている場合は
新しい名前が優先されます。古い名前はメジャーバージョンで廃止されます。

SDK では、`BREVIS_SDK_LIMITE_INLINE` が同じルールで `BREVIS_SDK_INLINE_LIMIT` になりました。
:::

## タスクの環境

| 変数 | デフォルト | |
|---|---|---|
| `BREVIS_TASK_ENV` | 空 | 各ステップが受け取るもの |

タスクはオーケストレータの環境を**継承しません**。理由は具体的です。プロセスは Postgres のユーザー名と
パスワードを含む `BREVIS_DATABASE_URL` を持っており、ワークフローは他の誰かが書いた任意のコマンドです。
継承すると、すべてのパイプラインのすべてのステップにデータベースの認証情報を渡すことになります。

タスクが必要とするものは、宣言します。

```bash
BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE   # forwards these two from the process
BREVIS_TASK_ENV=STAGE=prod                # sets a literal
BREVIS_TASK_ENV='*'                       # forwards everything EXCEPT BREVIS_*
```

`PATH` と `HOME` は常に含まれます。`PATH` がなければどのコマンドも解決されず、何も説明しない
「not found」というエラーになるからです。

存在しない名前が空文字列になることは**ありません**。`GOOGLE_PROJECT_ID=""` では、dbt が変数の不在よりも
わかりにくいメッセージで失敗するからです。

## Kubernetes

ワークフローではなく、**インストール**の判断です。

| 変数 | デフォルト | 形式 |
|---|---|---|
| `BREVIS_PODS` | `auto` | `auto`、`on`、`off`。それ以外は起動エラー |
| `BREVIS_POD_NAMESPACE` | クラスタのもの | |
| `BREVIS_POD_SERVICE_ACCOUNT` | — | |
| `BREVIS_POD_PULL_SECRETS` | — | カンマ区切りのリスト |
| `BREVIS_POD_ENV_FROM_SECRETS` | — | カンマ区切りのリスト |
| `BREVIS_POD_ENV_FROM_CONFIGMAPS` | — | カンマ区切りのリスト |
| `BREVIS_POD_ALLOWED_SECRETS` | — | カンマ区切りのリスト |
| `BREVIS_POD_CREDENTIAL_PATH` | — | pod にマウントされるパス |
| `BREVIS_POD_CREDENTIAL_PVC` | — | 認証情報を保持する PVC |
| `BREVIS_POD_NODE_SELECTOR` | — | `key=value,other=value` |
| `BREVIS_POD_TOLERATIONS` | — | `key=value:effect,…`。`Equal` 演算子のみ |
| `BREVIS_POD_KEEP_ON_FAILURE` | `false` | `true` で調査用に pod を残す |

リスト内の空の要素は無視されます。`a,,b` はタイプミスであり、空の Secret 名があると、
サーバーが pod 全体を拒否してしまうからです。

## テスト

| 変数 | |
|---|---|
| `BREVIS_TEST_DATABASE_URL` | 結合テスト用のデータベース。なければスキップされる |

## ヘルスエンドポイント

| | |
|---|---|
| `GET /health` | liveness — データベースへのクエリは**行いません** |
| `GET /ready` | readiness — クエリを行い、失敗した依存先の名前を示します |

この分離は意図的です。外部サービスに依存する liveness プローブは、データベースが不安定になると、
ロードバランサから外すだけでは済まず、Kubernetes に **pod を kill** させてしまいます。

## 完全な例

```bash
# required
export BREVIS_DATABASE_URL='postgres://brevis:secret@db:5432/brevis?sslmode=require'

# production requires credentials
export BREVIS_ENV=production
export BREVIS_AUTH_USER=operator
export BREVIS_AUTH_PASSWORD_HASH="$(brevis hash < password.txt)"
export BREVIS_AUTH_SECRET="$(openssl rand -base64 48)"

# what the tasks need, and nothing more
export BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE,DBT_KEYFILE

# pods
export BREVIS_PODS=on
export BREVIS_POD_NAMESPACE=data
export BREVIS_POD_SERVICE_ACCOUNT=brevis-runner
export BREVIS_POD_ENV_FROM_SECRETS=bigquery-cred

# alerts
export BREVIS_SLACK_WEBHOOK='https://hooks.slack.com/services/...'
export BREVIS_UI_URL='https://brevis.example.com'
```
