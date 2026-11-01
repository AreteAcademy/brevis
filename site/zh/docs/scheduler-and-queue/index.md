# 调度器与队列

> 两个相互独立的循环：谁创建 run，谁执行 run，以及为什么要把它们分开。

*https://brevis.sh/zh/docs/scheduler-and-queue/ · brevis.sh docs (zh-CN)*

---

brevis.sh 把**创建** run 与**执行** run 分开。两个循环一起运行，彼此互不依赖。

```
scheduler                          queue
─────────                          ─────
reads the schedule
materialises the slot
creates the Run  ─────────────►  (pending in the database)
                                   claims it
                                   walks the graph
                                   executes each step
                                   records state and log
```

## 为什么要分开

一个既负责调度又负责执行的单一循环有个糟糕的特性：执行一旦卡住，调度也随之停止。接下来十二个小时的 slot 根本不会存在，直到有人去找一直没有到达的数据时才会发现。

分开之后：

- **调度器**可以宕机，而队列继续消费已创建的 run；
- **队列**可以宕机，而 slot 仍会继续生成，等它恢复后再执行；
- **重新处理不依赖时钟**：`backfill` 通过与 cron 相同的路径创建过去的 run。

## 进程

两个循环位于同一条命令中：

```bash
brevis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| 标志 | 默认值 | |
|---|---|---|
| `--interval` | `10s` | 调度器周期之间的间隔 |
| `--concurrency` | `5` | 同时运行的 **run** 数 |
| `--max-pods` | `5` | 同时运行的 **step** 总数 |

`--concurrency` 与 `--max-pods` 刻意统计不同的东西：如果只限制 run，五个 run 各有三个并行 step，就是十五个 pod。

:::note `serve` 不生成调度 slot
API 进程也会启动同一个调度器，但**不带循环**：它只用来处理来自界面的手动触发，这样“调度器创建 run”这条规则就只有唯一的归属。要让调度生效，必须同时运行一个 `brevis scheduler`。
:::

## 定义快照

调度器创建 run 时，会把 **workflow 定义保存在 run 内部**。执行时读取的是这份快照，而不是磁盘上的 YAML。

原因很具体：在触发与执行之间，可能有人编辑了文件并完成了部署。没有快照的话，05:00 用一份定义创建的 run，会在 05:02 用另一份定义执行，而日志无从解释这种差异。

## 重试

失败的 run 会尝试**三次**，延迟**翻倍**：按默认值，三次尝试分别发生在 **0 秒、30 秒和 1 分 30 秒**。

```bash
brevis scheduler --max-attempts 3 --retry-backoff 30s --retry-backoff-max 1h
```

| 标志 | 默认值 | |
|---|---|---|
| `--max-attempts` | `3` | 每个 run 的尝试次数，包含第一次 |
| `--retry-backoff` | `30s` | 第一次延迟，之后每次尝试翻倍 |
| `--retry-backoff-max` | `1h` | 该延迟的上限 |

进程在启动时会打印这个时间表，因此无需根据两个数字自行推算：

```
scheduler and dispatcher are up  max_attempts=3 retry_backoff=30s retry_at="0s, 30s, 1m30s"
```

:::tip
**有速率限制的供应商需要更长的间隔。** 这类 pipeline 失败的头号原因是上游的暂时性故障：API 返回 200 却说“你已达到请求上限”，或者数据仓库的配额出现波动。这些问题大约需要一分钟才能恢复，而在此之前就结束的重试等于没有重试。`--retry-backoff 1m` 会把尝试安排在 0 秒、1 分钟和 3 分钟。
:::

重试是按 **run** 进行的，并且只重新运行失败的 step：失败的 `dbt_build` 不会重新运行它上面已经成功的抓取。

尝试次数是持久化的，不是进程状态。重启 worker 不会抹去已经掌握的关于该 run 的信息。

**run** 的尝试序号会写进 pod 名称。没有它，重试会从零开始重启 run，与上一次尝试同名的 pod 相遇，永远卡在 `Pending`。

## Backfill

为已发布 workflow 生成过去的 slot：

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

```
  31 backfill run(s) queued for daily (2026-01-01 to 2026-01-31)
  run `brevis scheduler` to execute them
```

它只**入队，不执行**。执行是 `scheduler` 的工作，同样是这种分离。`--to` 包含整整一天。

## 由数据落地触发

workflow 可以因为数据到达而运行，而不是因为时钟到点：

```yaml
trigger:
  on_landed:
    - bigquery://acme-prod/bronze/orders
    - bigquery://acme-prod/bronze/*
  debounce: 5m
```

每个 step 都已经在承载各阶段的管道上声明了它写入了什么，`/data` 正是由此填充的。workflow 订阅其中一个名称，当有数据落到该名称上时启动。

末尾的 `/*` 表示订阅整个 dataset。它是为 `auto_table` 而存在的，后者为每条路由创建一张表：对于没有人声明过的表，唯一能指名的就是 dataset。它匹配 `bronze/orders`，不匹配 `bronze_raw/orders`。

### 时间窗口，以及为什么十次落地只算一个 run

`debounce` 会把一阵集中到达的落地合并起来。五分钟内落地的十张表只会启动**一个** run，而不是十个，并且 run 会说明是哪几张：

```
BREVIS_AUTO_LANDED=bigquery://acme-prod/bronze/orders,bigquery://acme-prod/bronze/items
```

完整列表也以 JSON 形式放在 `BREVIS_AUTO_PARAMS` 中；当目标名称里可能含有逗号时，应读取它。

窗口是从**引擎记录该次落地的时间**开始计算的，而不是从 step 声称落地发生的时间。如果 step 报告的是其查询开始的那一刻，那么它报告的既真实又陈旧，而一个偏移的时钟就会把一阵集中到达的落地拆成两个 run。

省略 `debounce` 时，每次落地都会启动一个 run。

### 它不会做什么

**workflow 绝不会被自己的写入启动。** 如果某次落地的写入方就是订阅它的 workflow，引擎会丢弃这次落地并在日志中说明原因，否则读写同一张表的 workflow 会无限地触发自己。

它是在**触发时**被拒绝的，而不是在检查文件时，这是一个限制，而不是疏忽：`brevis validate` 读取的是文件，而没有任何文件声明某个 step 写入了什么。数据仓库中的目标是由连接决定的，不是由 YAML 决定的。所以 `validate` 会放行一个在运行时每次都会被拒绝的 workflow，原因会记录在日志中。

**第一个周期不会启动任何东西。** 触发器从它生效的那一刻开始计数，与调度相同；否则发布一个触发器就会为目录历史中的每一次落地各启动一个 run。

## 告警

```bash
export BREVIS_SLACK_WEBHOOK='https://hooks.slack.com/services/...'
export BREVIS_UI_URL='https://brevis.example.com'
```

没有 webhook 时，进程会在启动时警告失败将不会被上报。一个悄无声息地失败的安装，往往是由客户而不是团队发现的。

`BREVIS_UI_URL` 用来在告警中生成指向 run 的链接；没有它，告警会说明什么失败了，却要读者自己手动去找那个 run。

## 下一步

- [每个 step 一个 pod](/zh/docs/pod-per-step/index.md)：每个 step 实际在哪里运行
- [CLI](/zh/docs/cli/index.md)：`scheduler` 和 `backfill` 的所有标志
