---
title: 配置
description: 所有环境变量，包括默认值、格式，以及缺失时会发生什么。
group: 参考
order: 10
slug: configuration
---

所有配置都来自**环境变量**，在启动时读取一次。之后不会再查询环境：配置在运行中途发生变化的进程，其行为是没有人能够复现的。

## 必需

| 变量 | |
|---|---|
| `BREVIS_DATABASE_URL` | Postgres。缺失 = 使用数据库的五个子命令出现**启动错误** |

## 进程

| 变量 | 默认值 | |
|---|---|---|
| `BREVIS_ENV` | `local` | `local` 以文本格式记录日志并让界面保持开放；其他任何值都需要凭据，并以 JSON 记录日志 |
| `BREVIS_HTTP_ADDR` | `:8080` | 监听地址 |
| `BREVIS_LOG_LEVEL` | `info` | |
| `BREVIS_SHUTDOWN_TIMEOUT_SECONDS` | `15` | 整数；非数字的值会导致启动错误 |
| `BREVIS_BRAND_FILE` | `brand.yaml` | 品牌；缺失 = 默认 |
| `BREVIS_UI_URL` | — | 告警中 run 链接的基础地址 |
| `BREVIS_METRICS_ADDR` | — | Prometheus 指标端口；为空 = 不暴露。见[可观测性](/docs/observability/) |
| `BREVIS_SLACK_WEBHOOK` | — | 失败告警的目的地。为空 = 没有人会收到通知 |

## 认证

| 变量 | |
|---|---|
| `BREVIS_AUTH_USER` | 界面用户 |
| `BREVIS_AUTH_PASSWORD_HASH` | 由 `brevis hash` 生成的 `pbkdf2-sha256$…` 哈希 |
| `BREVIS_AUTH_SECRET` | 用于签署会话的 32 字节以上内容（`openssl rand -base64 48`） |

三者要么全部提供，要么全部不提供。

:::danger 只配置一半是启动错误
填写了用户名的人，会以为门已经关上了。界面可以触发 pipeline：`POST /workflows/<slug>/trigger` 会运行一个写入数据仓库的 `dbt build`。如果暴露在互联网上，它就是数据仓库的遥控器，而一条日志警告无法消除这种误以为，因为没有人会去读一个正常运行的进程的日志。
:::

在 `BREVIS_ENV=local` 之外，没有凭据就启动会被拒绝：

```
error: BREVIS_ENV=production requires a credential: set BREVIS_AUTH_USER,
BREVIS_AUTH_PASSWORD_HASH (generate one with `brevis hash`) and BREVIS_AUTH_SECRET
```

:::note 旧名称仍然有效
有四个变量在 2026-09-10 被重命名，当时最后几个葡萄牙语名称从项目面向操作者的一侧移除了：`BREVIS_AUTH_USUARIO`、`BREVIS_AUTH_SENHA_HASH`、`BREVIS_AUTH_SEGREDO` 和 `BREVIS_POD_MANTER_EM_FALHA`。设置了旧名称的安装环境仍可继续工作，并会记录一条指出新名称的警告。

先添加新名称，部署，然后再删除旧名称；两者同时设置时，新名称优先。旧名称会在下一个主版本中移除。

在 SDK 中，`BREVIS_SDK_LIMITE_INLINE` 按同样的规则变成了 `BREVIS_SDK_INLINE_LIMIT`。
:::

## 任务环境

| 变量 | 默认值 | |
|---|---|---|
| `BREVIS_TASK_ENV` | 空 | 每个 step 接收到的内容 |

任务**不会**继承编排器的环境。原因很具体：该进程带有 `BREVIS_DATABASE_URL`，其中包含 Postgres 的用户名和密码，而 workflow 是别人写的任意命令，继承就等于把数据库凭据交给每条 pipeline 的每个 step。

任务需要什么，需要显式声明：

```bash
BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE   # forwards these two from the process
BREVIS_TASK_ENV=STAGE=prod                # sets a literal
BREVIS_TASK_ENV='*'                       # forwards everything EXCEPT BREVIS_*
```

`PATH` 和 `HOME` 始终包含在内：没有 `PATH`，任何命令都无法解析，而错误只会是一句什么都解释不了的“not found”。

缺失的名称**不会**变成空字符串：`GOOGLE_PROJECT_ID=""` 会让 dbt 以比变量缺失更糟糕的信息失败。

## Kubernetes

这些是**安装**层面的决定，而不是 workflow 层面的。

| 变量 | 默认值 | 格式 |
|---|---|---|
| `BREVIS_PODS` | `auto` | `auto`、`on` 或 `off`；其他任何值都是启动错误 |
| `BREVIS_POD_NAMESPACE` | 集群的命名空间 | |
| `BREVIS_POD_SERVICE_ACCOUNT` | — | |
| `BREVIS_POD_PULL_SECRETS` | — | 以逗号分隔的列表 |
| `BREVIS_POD_ENV_FROM_SECRETS` | — | 以逗号分隔的列表 |
| `BREVIS_POD_ENV_FROM_CONFIGMAPS` | — | 以逗号分隔的列表 |
| `BREVIS_POD_ALLOWED_SECRETS` | — | 以逗号分隔的列表 |
| `BREVIS_POD_CREDENTIAL_PATH` | — | 挂载在 pod 中的路径 |
| `BREVIS_POD_CREDENTIAL_PVC` | — | 保存凭据的 PVC |
| `BREVIS_POD_NODE_SELECTOR` | — | `key=value,other=value` |
| `BREVIS_POD_TOLERATIONS` | — | `key=value:effect,…`；仅支持 `Equal` 运算符 |
| `BREVIS_POD_KEEP_ON_FAILURE` | `false` | `true` 会保留 pod 以便检查 |

在列表中，空条目会被忽略：`a,,b` 是笔误，而空的 secret 名称会导致服务器拒绝整个 pod。

## 测试

| 变量 | |
|---|---|
| `BREVIS_TEST_DATABASE_URL` | 集成测试使用的数据库；没有它时，这些测试会被跳过 |

## 健康检查端点

| | |
|---|---|
| `GET /health` | 存活探针：**不**查询数据库 |
| `GET /ready` | 就绪探针：会查询，并指出失败的依赖 |

这种分离是有意的：依赖外部服务的存活探针，会在数据库出现波动时让 Kubernetes **杀掉 pod**，而不是仅仅把它从负载均衡器中摘除。

## 完整示例

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
