# Kubernetes

> 如何部署引擎、需要哪些权限，以及部署之后应该检查什么。

*https://brevis.sh/zh/docs/kubernetes/ · brevis.sh docs (zh-CN)*

---

典型的安装包含同一个二进制文件的**两个 Deployment**（API 和调度器），外加一个 Postgres。

## 使用 Helm 安装

chart 位于仓库的 `deployments/helm/brevis` 中。有四个值是必填的，其余都有可用的默认值：

```bash
helm install brevis ./deployments/helm/brevis \
  --namespace data --create-namespace \
  --set database.url="postgres://brevis:pw@postgres/brevis?sslmode=require" \
  --set auth.user=admin \
  --set auth.passwordHash="$(brevis hash)" \
  --set auth.secret="$(openssl rand -base64 48)"
```

这就是全部安装过程：迁移先以 hook 的形式运行，两个 Deployment 各自使用正确的镜像启动，调度器获得一个可以创建 pod 的 ServiceAccount，而任务 pod 获得的 ServiceAccount 什么都不能做。

要访问界面：

```bash
helm upgrade brevis ./deployments/helm/brevis --reuse-values \
  --set api.ingress.enabled=true \
  --set api.ingress.host=brevis.example.com \
  --set api.ingress.tls.enabled=true \
  --set api.ingress.tls.secretName=brevis-tls
```

在生产环境中，请不要把连接字符串放进 release：把它放进 Secret，并传入 `--set database.existingSecret=brevis-db`。如果以内联方式传入，它会进入 release 保存的 manifest，而任何能读取该命名空间中 Secret 的人都可以读到它。

`helm show values ./deployments/helm/brevis` 会列出每个值，并在旁边给出原因。

### chart 会拒绝什么

任何无法保证安全的配置都会在渲染时失败，并给出说明哪里会出问题的信息，而不是在凌晨三点、在一个产生了重复行的 run 里才暴露：

| 被拒绝的 | 原因 |
|---|---|
| 任何 `scheduler.replicas` | 两个调度器会生成相同的 slot。`FOR UPDATE SKIP LOCKED` 能阻止它们认领同一个队列条目，却没有任何东西能阻止它们创建同一个调度 run，于是 `dbt build` 会对同一个窗口运行两次，而没有任何东西失败 |
| 在 `local` 之外缺少凭据 | 引擎没有凭据就拒绝启动，所以 chart 给你的会是 CrashLoopBackOff |
| 小于 32 字节的 `auth.secret` | 它用于签署会话 cookie，引擎强制执行同样的最小长度 |
| 没有 `host` 的 Ingress | 它会匹配到达控制器的每一个请求，在共享集群中，会替别人的主机名作答 |
| 启用了 `alerts.enabled` 却没有 webhook | pod 会消费发件箱却无处投递，这比不运行更糟：失败看起来像是已经通知过了 |
| chart 没有定义的值 | `--set` 中的拼写错误否则会悄悄地什么都不做 |

`.github/scripts/helm-check.sh` 会在渲染结果上断言以上所有内容，以及上述的不变量：只有一个调度器，存在 `pods/log`，迁移最先运行，指标端口不在 Service 上。

## 不使用 Helm

`deployments/kubernetes/` 中有七个 manifest，在你编辑命名空间和镜像标签之后即可直接 `kubectl apply -f`：

```bash
kubectl apply -f deployments/kubernetes/rbac.yaml
kubectl apply -f deployments/kubernetes/api.yaml
kubectl apply -f deployments/kubernetes/scheduler.yaml
```

它们包含与 chart 相同的决策，并在注释中给出了理由，此外还有 `alert.yaml`、`report.yaml` 和 `publish-job.yaml`。下面的各节会逐一介绍它们的内容。

## 两个角色

| 角色 | 命令 | 镜像 | 副本数 |
|---|---|---|---|
| API + 界面 | `serve` | `:0.11.2`（distroless） | 想要多少都行 |
| 调度器 + 队列 | `scheduler` | `:0.11.2-worker`（alpine） | **一个** |

API 使用 distroless，因为它只提供 HTTP 服务：它不执行任何东西，所以不需要 shell。worker 使用 alpine，因为 `run:` step 需要 shell。

:::warning 只有一个调度器副本
两个进程生成同一个调度，会创建重复的 run。如果需要可用性，请使用 `replicas: 1` 和 `strategy: Recreate`。
:::

## 迁移

请在部署之前以 Job 的形式运行迁移，绝不要在 `serve` 启动时运行：

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
          image: areteacademy/brevis:0.40.0
          args: ["migrate", "up"]
          envFrom:
            - secretRef: {name: brevis-db}
```

## API Deployment

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
          image: areteacademy/brevis:0.40.0
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

:::note 为什么两个探针不同
`/health` **不**查询数据库；`/ready` 会查询，并指出失败的依赖。如果存活探针依赖 Postgres，数据库一出现波动，Kubernetes 就会**杀掉** API pod，而不是仅仅把它们从负载均衡器中摘除，恢复会在系统已经承受压力的时候变得更慢。
:::

## 调度器 Deployment

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
          image: areteacademy/brevis:0.40.0-worker
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

`BREVIS_PODS=on` **要求**存在集群：没有挂载 service account 时，启动会失败，而不是悄悄回退为本地执行。

## 权限

调度器在执行命名空间中创建、监视和删除 pod，除此之外别无其他：

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

`pods/log` 使得可以在界面中跟踪 step 的输出。没有它，run 能正常工作，但日志会是空的。

## 部署之后

```bash
kubectl -n data get pods -l app=brevis-api
kubectl -n data exec deploy/brevis-api -- brevis version
curl -fsS https://brevis.example.com/ready
```

在界面中触发一个简单的 workflow，确认 step 的 pod 出现然后消失：

```bash
kubectl -n data get pods -w
```

## 当 step 一直处于 Pending

几乎总是下面三种原因之一：

| 原因 | 如何确认 |
|---|---|
| 有污点而没有容忍 | `kubectl describe pod` 显示 `node(s) had untolerated taint` |
| `nodeSelector` 没有匹配的节点 | `describe` 显示 `didn't match Pod's node affinity` |
| 资源不足 | `describe` 显示 `Insufficient cpu` 或 `memory` |

要检查失败 step 的 pod，而不是眼看着它消失：

```bash
BREVIS_POD_KEEP_ON_FAILURE=true
```

在生产环境中请保持关闭：已停止的 pod 会占用配额。

## 下一步

- [每个 step 一个 pod](/zh/docs/pod-per-step/index.md)：执行模型
- [配置](/zh/docs/configuration/index.md)：所有 `BREVIS_POD_*` 变量
- [可观测性](/zh/docs/observability/index.md)：抓取什么，以及从哪个进程抓取
