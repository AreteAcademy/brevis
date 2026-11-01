# CLI

> 引擎的十二个子命令，包括标志、参数以及每个命令的前提条件。

*https://brevis.sh/zh/docs/cli/ · brevis.sh docs (zh-CN)*

---

引擎是一个带子命令的二进制文件，而不是多个二进制文件：API、调度器和 worker 是同一系统的不同角色，一个二进制文件意味着一个版本、一个镜像和一条构建路径。

```
brevis [command] [flags]
```

| 命令 | 数据库 | `local` 之外的凭据 | 角色 |
|---|---|---|---|
| [`serve`](#serve) | **是** | **必需** | API + 界面 |
| [`scheduler`](#scheduler) | **是** | **必需** | 两个循环 |
| [`migrate`](#migrate) | **是** | **必需** | schema |
| [`publish`](#publish) | **是** | **必需** | 保存 workflow 和调度 |
| [`backfill`](#backfill) | **是** | **必需** | 重新处理一个范围 |
| [`cancel`](#cancel) | **是** | **必需** | 停止一个 run |
| [`run`](#run) | 否 | — | 立即在实例本身上运行 |
| [`validate`](#validate) | 否 | — | 校验 workflow YAML |
| [`brand`](#brand) | 否 | — | 校验品牌 YAML |
| [`hash`](#hash) | 否 | — | 生成密码哈希 |
| [`version`](#version) | 否 | — | 版本、commit、构建 |

前六个会加载配置，缺少 `BREVIS_DATABASE_URL` 时**启动即失败**。其余五个不会，所以 `validate` 能在没有 Postgres 的 CI 中工作。

这前六个还遵循凭据规则：当 `BREVIS_ENV` 不是 `local` 时，缺少这三个认证变量就启动，会是**启动错误**。

---

## serve

启动 HTTP API 和界面。没有标志，一切都来自环境变量。

```bash
brevis serve
```

收到 `SIGINT`/`SIGTERM` 时会优雅关闭，因此部署不会切断正在处理的请求。这个进程**不会生成调度 slot**：这需要同时运行一个 `brevis scheduler`。

它是 `api` 镜像的 `CMD`（distroless，没有 shell）。

---

## scheduler

两个循环：调度器把调度生成为 run，分发器从队列中取出并执行。

```bash
brevis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| 标志 | 类型 | 默认值 | |
|---|---|---|---|
| `--interval` | duration | `10s` | 周期之间的间隔 |
| `--concurrency` | int | `5` | 同时运行的 **run** 数 |
| `--max-pods` | int | `5` | 同时运行的 **step** 总数 |

它是 `worker` 镜像的 `CMD`（带 shell 的 alpine，因为 `run:` step 需要 shell）。

---

## alert

投递调度器记录下来的告警。是 `serve` 和 `scheduler` 之外的第三个进程。

```bash
brevis alert --interval 1s --max-attempts 6
```

| 标志 | 类型 | 默认值 | |
|---|---|---|---|
| `--interval` | duration | `1s` | 投递周期之间的间隔 |
| `--max-attempts` | int | `6` | 告警被记录为未送达之前的尝试次数 |

**它的存在，是因为告警写在哪里，而不是因为投递本身值得一个进程。** 调度器把告警与促成它的失败记录在同一个事务中：要么 run 用尽了尝试次数并且告警存在，要么两者都没有发生。之后必须有东西来消费那张表，而且它要能经受住 Slack 故障以及自身的重启。在此之前，webhook 失败意味着只有一行日志，而告警就这样消失了。

没有配置渠道（`BREVIS_SLACK_WEBHOOK`）时，它拒绝启动。一个无处投递的投递进程，会把发件箱以与填充一样快的速度消耗成“未送达”，而这些行看起来与 Slack 拒收完全一样。

它放弃的告警会被**保留**：“已触发，未送达，4 次尝试，Slack 返回 403”才是重要的那一行，因为这正是有人在等待一条永远不会来的消息的情形。

它是 `worker` 镜像的 `CMD`。

---

## report

发送运行情况的定期汇总。

```bash
brevis report --window 168h              # sends it
brevis report --window 168h --dry-run    # prints it, sends nothing
```

| 标志 | 类型 | 默认值 | |
|---|---|---|---|
| `--window` | duration | `168h` | 回溯多久 |
| `--dry-run` | bool | `false` | 打印而不发送 |

它是一个**命令**，而不是循环，这正是汇总与告警的区别：它由 CronJob 运行，所以没有新的循环，没有新的状态，也不需要选主，因为集群本来就有调度器。`--dry-run` 是让人建立对每周消息的信任的方式：读一读它本来会说什么。

它**不**经过告警发件箱。二者只共用投递渠道，别无其他：丢失一条告警是一次无人知晓的故障，而错过某一周的汇总，只是下一周的汇总。

消息包含 run 数、成功数、失败数、失败最多的 pipeline、耗时最长的 pipeline，以及 SDK 上报的行数和字节数。它**不**包含 CPU 或内存，也不会为它们打印零：引擎不收集这些数据，消息会说明它们在哪里。

空的窗口会如实说明，而不是报告 100%。凭空算出的成功率是报告中最令人安心也最不真实的数字：空的一周通常意味着调度器宕机了。

`deployments/kubernetes/report.yaml` 每周运行一次。

---

## migrate

```bash
brevis migrate up      # applies what is missing
brevis migrate down    # reverts the last one
brevis migrate status  # shows the state
```

恰好接受一个参数。迁移内嵌在二进制文件中，并由专门的子命令应用：**`serve` 绝不会修改 schema**。

---

## validate

校验一个或多个 workflow 文件。接受文件**或目录**。

```bash
brevis validate examples/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

它不接触数据库，也不启动服务器，所以可以在编辑器和 CI 中运行。失败时以非零状态退出，并统计失败数。

---

## run

**立即在实例本身上**运行 workflow：没有队列，没有数据库，没有调度器。

```bash
brevis run examples/hello.yaml
brevis run wf.yaml --param load_full=true --retries 3 --timeout 5m
```

| 标志 | 类型 | 默认值 | |
|---|---|---|---|
| `--param` | 可重复 | — | 已声明参数的 `key=value` |
| `--workdir` | string | 文件所在的目录 | step 的工作目录 |
| `--retries` | int | `1` | 每个 step 的尝试次数（`1` = 不重试） |
| `--timeout` | duration | `0` | 每个 step 的超时（`0` = 不限制） |

不带 `=` 的 `--param` 是**错误**，而不是警告：`--param load_full` 忘了写值，就会以默认值运行，而操作者还以为这个值已经生效。

:::warning 四个限制
`run` 只在 `BREVIS_ENV=local` 时工作；带 `image:` 的 step 会在实例本身上运行，并会明确说明；没有 Go 任务注册表，所以对未注册任务的 `action:` 会失败，并列出可用的任务；`run` 也不读取 `BREVIS_HOSTS`，所以带 `host:` 的 step 根本无法在这里运行，即使设置了该变量，也会因主机名而失败。
:::

---

## publish

把 workflow 及其调度保存到数据库中。接受文件或目录。

```bash
brevis publish examples/hello.yaml
brevis publish workflows/ --project acme --prune
```

| 标志 | 类型 | 默认值 | |
|---|---|---|---|
| `--project` | string | `default` | 项目 slug |
| `--prune` | bool | `false` | 从项目中删除不在列表中的 workflow |

使用 `--prune` 时，被删除 workflow 的历史会被保留。

---

## backfill

为已发布 workflow 生成过去的 slot。它只**入队，不执行。**

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

| 标志 | | |
|---|---|---|
| `--from` | **必填** | 开始日期，`YYYY-MM-DD` |
| `--to` | **必填** | 结束日期，`YYYY-MM-DD` |
| `--param` | | 应用于范围内的**每一个** slot |

`--to` 包含整整一天：内部会把结束时间设为该日期的 `23:59:59`。

---

## cancel

取消仍在等待或正在运行的 run。事件流中会把 `cli` 显示为取消者。

```bash
brevis cancel 3f1c9a52-6d0e-4b7a-9c41-2b8e5d7f0a13
```

| 响应 | 发生了什么 |
|---|---|
| `canceled` | 该 run 正在队列中等待；它已离开队列，不会再运行 |
| `cancel requested` | 该 run 正在运行；它的分发器会在 `--cancel-poll`（2 秒）内停止这些 step |
| `already ended: <status>` | 该 run 已经结束；没有任何变化 |

被取消的 run 不会重试。不存在的 id 会报错。

---

## gateway

gateway 如何出现在 `/data` 中：它的配置，作为一次有意的操作发布，绝不是它的流量，也绝不在运行时发布。

```bash
gateway describe gateway.yaml > manifest.json   # the gateway's own binary
brevis gateway publish manifest.json
brevis gateway unpublish events_gateway
```

`publish` 会在写入任何东西之前整体检查 manifest，并指出第一个问题所在的 stream 和角色，然后在一个事务中替换该 gateway 的目标。`unpublish` 会移除已下线的 gateway；如果没有发布过该名称的 gateway，则会报错。

`brevis-sql-api` 如何出现在 Data → Endpoints 中：每个 endpoint 接受什么、读取什么，作为一次有意的操作发布，绝不是它的流量。

```bash
brevis-sql-api describe --config api.yaml --out manifest.json   # the API's own binary
brevis endpoints publish manifest.json
brevis endpoints unpublish metrics-api
```

`publish` 会在写入任何东西之前整体检查 manifest，并在一个事务中替换该 API 的 endpoint；形似凭据的名称、默认值或备注会导致整个发布被拒绝。

它是一个独立的命令，而不是 `brevis publish` 的一种模式，因为 workflow 和 gateway 的集合语义不同：`publish --prune` 绝不能触及 gateway 的行。

---

## prune

应用保留策略。它是 Brevis 中**唯一**会删除东西的功能，而且从不自行运行，需要由操作者来调度。

```bash
brevis prune --dry-run          # what would go, changing nothing
brevis prune
brevis prune --purge-after 0    # trim only, never delete a run
```

| 标志 | 默认值 | |
|---|---|---|
| `--trim-after` | `720h` | 清空日志、各阶段和已发布的输出 |
| `--purge-after` | `8760h` | 完全删除 run；`0` 表示永不删除 |

---

## brand

在不启动服务器的情况下校验品牌文件。

```bash
brevis brand brand.yaml
```

```
  ok    Brevis · Orchestration
        logo      /assets/logo.svg  (built-in symbol)
        accent    (the product's own)
        Powered by Brevis
```

与启动时有一处行为上的区别：**缺少文件是错误**。在 `serve` 中，文件不存在表示“使用默认标识”；而要求校验某个路径的人，期望被告知它不存在。

---

## hash

生成 `BREVIS_AUTH_PASSWORD_HASH` 的值。

```bash
brevis hash
```

密码从终端**无回显**读取，绝不通过参数传入：参数会出现在任何进程的 `ps` 中，也会留在 shell 历史里。它拒绝短于 12 个字符的密码。

哈希单独输出到 **stdout**，提示标签输出到 **stderr**，因此 `brevis hash > hash.txt` 只会保存真正重要的内容。

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

直接用 `go build` 构建时，它会打印 `brevis dev`，没有 commit 也没有日期；当有人报告异常行为时，把它与正式发布的制品区分开来很重要。
