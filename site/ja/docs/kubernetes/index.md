# Kubernetes

> エンジンのデプロイ方法、必要な権限、そしてデプロイ後に確認すること。

*https://brevis.sh/ja/docs/kubernetes/ · brevis.sh docs (ja)*

---

典型的なインストールには、同じバイナリの **2 つの Deployment**(API とスケジューラ)と、
Postgres があります。

## Helm でインストールする

チャートはリポジトリの `deployments/helm/brevis` にあります。必須の値は 4 つで、
それ以外にはすべて動作するデフォルトがあります。

```bash
helm install brevis ./deployments/helm/brevis \
  --namespace data --create-namespace \
  --set database.url="postgres://brevis:pw@postgres/brevis?sslmode=require" \
  --set auth.user=admin \
  --set auth.passwordHash="$(brevis hash)" \
  --set auth.secret="$(openssl rand -base64 48)"
```

これでインストールは完了です。マイグレーションがフックとして最初に実行され、2 つの Deployment が
それぞれ正しいイメージで起動し、スケジューラには pod を作成できる ServiceAccount が、
タスクの pod には何もできない ServiceAccount が与えられます。

インターフェースにアクセスするには:

```bash
helm upgrade brevis ./deployments/helm/brevis --reuse-values \
  --set api.ingress.enabled=true \
  --set api.ingress.host=brevis.example.com \
  --set api.ingress.tls.enabled=true \
  --set api.ingress.tls.secretName=brevis-tls
```

本番では、接続文字列をリリースに含めないでください。Secret に入れて
`--set database.existingSecret=brevis-db` を渡します。インラインで渡すと、リリースの保存された
マニフェストに入り、その名前空間で Secret を読める人なら誰でも読めてしまいます。

`helm show values ./deployments/helm/brevis` は、すべての値を、その理由とともに一覧表示します。

### チャートが拒否するもの

安全にできないものはすべて、レンダリング時に、何が壊れるかを伝えるメッセージとともに失敗します。
午前 3 時に、重複した行を生成した run の中で発覚するのではありません。

| 拒否されるもの | 理由 |
|---|---|
| `scheduler.replicas` の指定そのもの | 2 つあれば同じ slot を具体化してしまう。`FOR UPDATE SKIP LOCKED` は同じキューアイテムの取得を防ぐが、同じスケジュールされた run の作成を防ぐものはなく、`dbt build` が何も失敗しないまま同じウィンドウに対して 2 回実行される |
| `local` 以外での認証情報の欠落 | 認証情報なしではエンジンが起動を拒否するため、チャートは CrashLoopBackOff を渡すことになる |
| 32 バイト未満の `auth.secret` | セッション Cookie の署名に使われ、エンジンも同じ最小値を強制する |
| `host` のない Ingress | コントローラに届くすべてのリクエストに一致してしまう。共有クラスタでは、他人のホスト名に応答することになる |
| Webhook のない `alerts.enabled` | pod はアウトボックスを消化してどこにも配信しないことになり、動かないよりも悪い。失敗が通知済みに見えてしまう |
| チャートが定義していない値 | そうしないと `--set` のタイプミスは黙って何もしない |

`.github/scripts/helm-check.sh` は、レンダリング結果に対してそのすべてを検証します。さらに、
上記の不変条件も検証します。スケジューラが 1 つであること、`pods/log` があること、マイグレーションが
最初であること、メトリクスのポートが Service にないことです。

## Helm を使わない場合

7 つのマニフェストが `deployments/kubernetes/` にあり、名前空間とイメージタグを編集すれば
`kubectl apply -f` でそのまま適用できます。

```bash
kubectl apply -f deployments/kubernetes/rbac.yaml
kubectl apply -f deployments/kubernetes/api.yaml
kubectl apply -f deployments/kubernetes/scheduler.yaml
```

チャートと同じ判断が、理由をコメントに添えて含まれています。ほかに `alert.yaml`、`report.yaml`、
`publish-job.yaml` があります。以下のセクションで、それぞれの内容を順に説明します。

## 2 つのロール

| ロール | コマンド | イメージ | レプリカ数 |
|---|---|---|---|
| API + インターフェース | `serve` | `:0.11.2`(distroless) | 必要なだけ |
| スケジューラ + キュー | `scheduler` | `:0.11.2-worker`(alpine) | **1 つ** |

API が distroless なのは、HTTP を提供するだけで何も実行せず、シェルが不要だからです。
ワーカーが alpine なのは、`run:` のステップにシェルが必要だからです。

:::warning スケジューラのレプリカは 1 つ
同じスケジュールを具体化する 2 つのプロセスは、重複した run を作ります。可用性が必要なら、
`strategy: Recreate` とともに `replicas: 1` を使ってください。
:::

## マイグレーション

デプロイの前に Job として実行します。`serve` の起動時には決して実行しないでください。

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: brevis-migrate
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: migrate
          image: brevissh/brevis:0.40.0
          args: ["migrate", "up"]
          envFrom:
            - secretRef: {name: brevis-db}
```

## API の Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: brevis-api
spec:
  replicas: 2
  selector:
    matchLabels: {app: brevis-api}
  template:
    metadata:
      labels: {app: brevis-api}
    spec:
      serviceAccountName: brevis
      containers:
        - name: api
          image: brevissh/brevis:0.40.0
          args: ["serve"]
          ports: [{containerPort: 8080}]
          envFrom:
            - secretRef: {name: brevis-db}
            - secretRef: {name: brevis-auth}
          env:
            - name: BREVIS_ENV
              value: production
          livenessProbe:
            httpGet: {path: /health, port: 8080}
          readinessProbe:
            httpGet: {path: /ready, port: 8080}
          resources:
            requests: {cpu: 100m, memory: 128Mi}
            limits: {memory: 256Mi}
```

:::note 2 つのプローブが異なる理由
`/health` はデータベースへの**クエリを行わず**、`/ready` は行い、失敗した依存先の名前を示します。
liveness が Postgres に依存すると、データベースが不安定になったときに、API の pod をロードバランサから
外すだけでなく Kubernetes に **kill** させてしまい、システムがすでに負荷を受けているまさにそのときに、
回復が遅くなります。
:::

## スケジューラの Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: brevis-scheduler
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector:
    matchLabels: {app: brevis-scheduler}
  template:
    metadata:
      labels: {app: brevis-scheduler}
    spec:
      serviceAccountName: brevis
      containers:
        - name: scheduler
          image: brevissh/brevis:0.40.0-worker
          args: ["scheduler", "--interval", "10s", "--concurrency", "5", "--max-pods", "10"]
          envFrom:
            - secretRef: {name: brevis-db}
            - secretRef: {name: brevis-auth}
          env:
            - name: BREVIS_ENV
              value: production
            - name: BREVIS_PODS
              value: "on"
            - name: BREVIS_POD_NAMESPACE
              value: data
            - name: BREVIS_POD_SERVICE_ACCOUNT
              value: brevis-runner
            - name: BREVIS_POD_ENV_FROM_SECRETS
              value: bigquery-cred
```

`BREVIS_PODS=on` は、クラスタを**必須**とします。サービスアカウントがマウントされていなければ、
黙ってローカル実行にフォールバックするのではなく、起動が失敗します。

## 権限

スケジューラは、実行用の名前空間で pod を作成、監視、削除します。それ以外は何もしません。

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: brevis-runner
  namespace: data
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["create", "get", "list", "watch", "delete"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
```

`pods/log` は、ステップの出力をインターフェースまで追跡できるようにするものです。
これがなくても run は動作しますが、ログは空のままです。

## デプロイ後

```bash
kubectl -n data get pods -l app=brevis-api
kubectl -n data exec deploy/brevis-api -- brevis version
curl -fsS https://brevis.example.com/ready
```

インターフェースから単純なワークフローをトリガーし、ステップの pod が現れて消えることを確認します。

```bash
kubectl -n data get pods -w
```

## ステップが Pending のままのとき

ほとんどの場合、次の 3 つのどれかです。

| 原因 | 確認方法 |
|---|---|
| toleration のない taint | `kubectl describe pod` に `node(s) had untolerated taint` と表示される |
| ノードのない `nodeSelector` | `describe` に `didn't match Pod's node affinity` と表示される |
| リソース不足 | `describe` に `Insufficient cpu` または `memory` と表示される |

失敗したステップの pod が消えるのを見るのではなく、調べるには:

```bash
BREVIS_POD_KEEP_ON_FAILURE=true
```

本番ではオフにしてください。停止した pod はクォータを消費します。

## 次のステップ

- [ステップごとの pod](/ja/docs/pod-per-step/index.md) — 実行モデル
- [設定](/ja/docs/configuration/index.md) — すべての `BREVIS_POD_*` 変数
- [オブザーバビリティ](/ja/docs/observability/index.md) — 何をスクレイプするか、どのプロセスからか
