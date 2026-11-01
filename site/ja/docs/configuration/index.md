# 設定

> すべての環境変数について、デフォルト、形式、未設定の場合に何が起きるか。

*https://brevis.sh/ja/docs/configuration/ · brevis.sh docs (ja)*

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
| `BREVIS_ENV` | `local` | `local` ではテキスト形式でログを出し、インターフェースを開放します。それ以外では認証情報が必須になり、JSON でログを出します |
| `BREVIS_HTTP_ADDR` | `:8080` | リッスンするアドレス |
| `BREVIS_LOG_LEVEL` | `info` | |
| `BREVIS_SHUTDOWN_TIMEOUT_SECONDS` | `15` | 整数。数値でない値は起動エラー |
| `BREVIS_BRAND_FILE` | `brand.yaml` | ブランディング。なければデフォルト |
| `BREVIS_UI_URL` | — | アラート内の run リンクのベース |
| `BREVIS_METRICS_ADDR` | — | Prometheus メトリクスのポート。空なら公開しない。[オブザーバビリティ](/ja/docs/observability/index.md)を参照 |
| `BREVIS_SLACK_WEBHOOK` | — | 失敗アラートの宛先。空なら誰にも通知されない |

## 認証

| 変数 | |
|---|---|
| `BREVIS_AUTH_USER` | インターフェースのユーザー |
| `BREVIS_AUTH_PASSWORD_HASH` | `brevis hash` が生成する `pbkdf2-sha256$…` のハッシュ |
| `BREVIS_AUTH_SECRET` | セッションの署名用の 32 バイト以上(`openssl rand -base64 48`) |

3 つは全部そろえるか、全部設定しないかのどちらかです。

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
