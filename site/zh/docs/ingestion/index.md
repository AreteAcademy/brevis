# 摄取

> 一个落地数据的 HTTP 端点：校验、标识、分批、投递。有自己的镜像，有自己的版本。

*https://brevis.sh/zh/docs/ingestion/ · brevis.sh docs (zh-CN)*

---

摄取 gateway 是一个**落地数据的 HTTP 端点**。客户端 `POST`，得到 `202`，到达的内容就变成 topic 上的一条记录、表中的一行，或 bucket 中的一个文件。

![Gateway 的流程：对 /v1/clicks 的 POST 被解码，获得其 ingestion_id，立即返回 202 并追加到一个批次；worker 把批次投递给 sink，这里是 files。Gateway 没有数据库。](/assets/flow-gateway.svg)

它是**另一个二进制文件、另一个 Go 模块、另一个版本和另一个镜像**。引擎负责编排，从不接触客户数据；gateway 则只做这一件事。版本编号刻意保持独立：引擎在 0.17，gateway 在 0.27，这才是真实的说法。

使用下一节中的 `gateway.yaml`，以及它在 `keys_from` 中要求的密钥：

```bash
docker run -p 8080:8080 -e BREVIS_INGEST_KEYS=dev-key \
  -v ./gateway.yaml:/etc/brevis/gateway.yaml:ro \
  areteacademy/brevis-gateway:0.32.0
```

```text
2026/10/09 01:03:01 drain budget 30s; set terminationGracePeriodSeconds >= 40
2026/10/09 01:03:01 connections idle out after 1m30s and are recycled after 1m0s
2026/10/09 01:03:01 listening on :8080
2026/10/09 01:03:01 metrics on :9090/metrics
```

`POST` 会立即返回 `202`：

```bash
curl -X POST localhost:8080/v1/clicks -H 'Authorization: Bearer dev-key' \
  -d '{"event_id":"e-1","occurred_at":"2026-10-08T12:00:00Z","page":"/pricing"}'
```

```text
{"accepted":1,"rejected":null}
```

没有 Google Cloud 凭据时，批次会被重试，并进入 dead letter，见[当 sink 拒绝时](#when-the-sink-refuses)。

`-slim` 镜像只包含 `auto_table`、`files` 和 `postgres` 这几个 sink；这个示例中的 `pubsub` 和其他 sink 都在完整镜像里。带有它所不包含的 sink 的精简镜像，会在启动时被拒绝，并给出原因。

## 文件就是契约

除了 hook 之外，不需要编写任何代码。stream、路径、格式、标识、重试、目的地和 dead letter，全部都在 YAML 中，而每个无法兑现的字段都会被拒绝。

```yaml
name: events_gateway

listen:
  addr: :8080
  max_body: 1MiB
  idle_timeout: 90s                  # the default; it cannot be turned off
  max_conn_age: 60s                  # off unless set — see below
  auth:
    type: bearer
    keys_from: BREVIS_INGEST_KEYS   # the NAME of the variable, never the keys

metrics:
  addr: :9090                        # its own port, never the ingest one

streams:
  - name: clicks
    path: /v1/clicks
    format: json                     # json | array | ndjson — declared, never sniffed

    identity:
      provider: web
      entity: click
      source_key: event_id
      record_ts: occurred_at

    buffer:
      flush: {every: 1s, records: 500}

    sink:
      type: pubsub
      project: acme-prod
      topic: clicks

    dead_letter:
      type: files
      path: /var/dead-letter/
```

`format` 是声明出来的，绝不靠猜测：靠猜测，就会让一千条的批次变成包含一个数组的一行。

## `ingestion_id`，这才是重点

每条记录离开时都带有一个 `ingestion_id`：对 `provider|entity|source_key|record_ts` 计算的**固定 UUID v5**。

它与批量抓取器为同一条记录计算出的是**同一个 id**。gateway 落地的行与 [SDK](/zh/docs/sdk/index.md) pipeline 落地的行是*同一行*，两者之间无需对账。

两个实际的后果：

- **重试的 `POST` 是同一条记录。** 客户端可以放心地再次发送；带有 `merge` 的目的地会吸收这次重试，而不是写入第二行。
- **公式是固定的。** 四个字段全部必填，漏掉一个会产生一个*不同*的 id，而不是一个更弱的 id。文件会拒绝不完整的配置，而不是接受它。

摄取工具搬运字节的能力很强，但对记录*是什么*没有任何看法。这种看法正是 gateway 增加的东西。

## 请求从不等待 sink

它解码，运行 hook，计算标识，追加到缓冲区，然后返回。**仅此而已。** 已满的批次交给 worker 池，发布、`COPY`、重试和 dead letter 都在那里完成。

这不是表面功夫。在此之前，恰好填满批次的那个请求会在原地执行投递，于是每 `flush.records` 个调用者中就有一个承担完整的往返开销，而当 sink 宕机时，还要承担完整的重试窗口。由哪个调用者倒霉所决定的 p99，不是任何人能据以行动的 p99。

```yaml
buffer:
  flush:
    every: 1s         # age
    records: 500      # count
    size: 8MiB        # bytes — whichever is crossed FIRST sends the batch
    max_age: 30s      # and this one sends it REGARDLESS
  workers: 4          # batches delivered at once
  queue: 64           # full batches that may wait for a worker
  max_records: 10000  # events held in memory before the gateway says no
  max_bytes: 256MiB   # the same ceiling in bytes
```

### flush 时钟属于部署

`buffer.flush.every` 是**每个副本**中的一个 `time.AfterFunc`。因此，一张表收到的加载作业数量取决于副本数，而不是流量：

```
jobs / day / table  =  replicas × 86400 / every
```

在 BigQuery 上，面对每张表每天 1,500 次的配额，**两个副本在 60 秒的间隔下就已经达到了无法提高的配额的 192%**，这还是在 HPA 做出任何动作之前。而且方向与直觉相反：gateway 为了 HTTP 并发而横向扩展，sink 却希望集中。

```yaml
buffer:
  flush:
    every: 60s
    max_age: 300s
    claim: true
```

启用 `claim` 后，副本在按计时器 flush 之前，会对该 stream 和该窗口做一次“不存在则设置”；计时器在**窗口边界**唤醒，所以每个副本请求的是同一个键。无论副本数量多少，每个窗口只 flush 一次。它还能自我调节：只有在确实有另一个副本竞争时，副本才会等待，所以缩容后无需任何人编辑配置，较低的延迟就会恢复。

用两个副本和一个 Redis、5 秒窗口实测：**8 个加载作业变成了 6 个，每个窗口一个，以 3/3 交替**，没有任何窗口出现两个。

**它需要共享后端。** 使用 `memory` 时，每个副本都会赢得自己的 claim，没有任何东西进行协调。并且它**要求 `max_age`**，没有它配置会被拒绝：claim 把 `every` 变成了一个目标，而没有上限的目标，就是没有任何东西约束的延迟。

**无法访问的 metastore 也会照常 flush。** 失去协调的代价是加载作业变多；拒绝 flush 则会扣住数据。已通过在运行中途杀掉 Redis 验证：40 个事件全部落地，没有任何事件被扣住。

**`outcome="unreachable"` 是值得告警的那一个。** 无法向后端发出请求，所以 claim 以失败放行的方式运行：协调已关闭，加载作业再次随副本数成倍增加，而且是悄无声息地，因为每次 flush 看起来仍然像一次 flush。

而且 claim 提供的是**互斥，而不是公平**：谁先请求谁赢。真实的双副本运行中测得的是 4/1，而不是平均分配。`max_age` 能防止倒霉的那个副本被饿死。

当 `claim: true` 且 `metastore: memory` 时，gateway 会在启动时**警告一次**：每个副本都会赢得自己的 claim，所以一切看起来都正常，却没有任何东西在协调。

### `every` 是目标；`max_age` 是承诺

上面的三个触发条件决定批次何时**尝试**离开。`max_age` 决定它何时**无论如何**都要离开。

目前这个区别是无效的，因为没有任何东西推迟目标。一旦 flush 时钟不再是按进程的，它就开始重要了：`buffer.flush.every` 是每个副本中的一个 `time.AfterFunc`，所以一张表收到的加载作业数量取决于**副本数**，而不是流量，即 `replicas × 86400 / every`。在 BigQuery 上，面对每张表每天 1,500 次，**两个副本在 60 秒的间隔下就已经是配额的 192%**。

一旦副本可以把窗口让给另一个副本，它就会继续填充，而 `max_age` 能防止任何一方为了等待轮到自己而无限期地持有数据。

它还决定了排空：缓冲区在 `SIGTERM` 时可能**持有**的数据量由它限定，gateway 在启动时打印的 `terminationGracePeriodSeconds` 也是由它推导出来的。

**低于**目标的上限在加载时会被拒绝，因为那意味着目标永远不会生效。

### 为什么有 size 触发条件

另外两个是**计数**，而计数无法区分 500 个 2 KB 的事件与 500 个 2 MB 的事件：1 MB 与 1 GB，在文件里是同一个数字。`max_records: 10000` 有同样的盲点：是 20 MB 还是 20 GB，YAML 中没有任何东西能说明。

开始发送整份文档而不是它的 id 的生产者，会把 pod 拖垮，而在这个触发条件出现之前，**没有任何字段能表达这个限制**。

大小是按**到达**的内容来度量的，而不是按事件在内存中的占用（`map[string]any` 是其 JSON 的数倍），也不是在膨胀它的 hook 之后度量。这是一个近似值，而且是正确的近似：解码时它是免费的，它随载荷变化，而它所防范的，是一条增大了十倍的记录。

**在 BigQuery 上，`size` 是上限，不是目标。** 该目的地允许每张表每天 1,500 次加载作业，这就是 `every` 有 60 秒下限的原因。但下限约束的是**计时器**：每隔几秒就触发一次的 size 会直接越过它，消耗同样的配额。没有任何东西会在加载时拒绝这一点，因为到达速率在那时是无法得知的。取而代之的是可见性：

```
brevis_gateway_flushes_total{stream,trigger}   trigger = time | records | size
```

BigQuery stream 上的 `trigger="size"` 不断攀升，是在告诉你，进行分批的并不是窗口。

这个计数器是按 stream 的，而配额是按**表**的：使用 `auto_table` 时，一条路由会变成多张表，94 张表共同报告一个数字，无法说明其中任何一张的情况。在 `BREVIS_INGESTION_METRICS` 之后有一个会指出表名的计数器：

```
brevis_gateway_table_flushes_total{stream,table,trigger}
```

对一张表的一次 flush 就是对它的一次加载作业，所以这是在达到 BigQuery 允许的 1,500 次中的 1,200 次时应当告警的序列。无条件提供的另一半，是启动时的一行输出，它会在任何事件到达之前，说明 `flush.every` 对每张表每天意味着多少次。

**它是有界的，而不是发后即忘。** 每个批次一个 goroutine，会把 sink 的故障变成无限增长的内存。当队列和缓冲区都满了，回应是带 `Retry-After` 的 **`503`**，而不是对一个无处可去的事件返回 `202`。已被接受却从未投递的事件，正是这个服务存在的意义所要避免的结果。

而且这个 `503` **可以安全重试**，几乎没有哪个服务能做到这一点：`ingestion_id` 是事件本身的固定函数，所以再次发送同样的请求体就是*同一条记录*。

## 连接决定哪个副本得到负载

在 Kubernetes 的 `ClusterIP` Service 之后，kube-proxy 是**每个 TCP 连接**选择一次后端，而不是每个请求。大多数客户端会保持连接并复用，所以只有少数几个连接的生产者，无论运行多少个副本，都会把所有流量发到同样的一两个副本上，而 HPA 新增的副本什么都收不到，因为没有人去连它。

```yaml
listen:
  idle_timeout: 90s     # closes a connection nobody came back to
  max_conn_age: 60s     # after this, the next response asks the client to redial
```

**`max_conn_age` 默认关闭**，这是有意为之：客户端可以保持一个连接多久是一项策略，它用每个连接每个存续周期一次集群内握手的代价，换来均匀分布的负载。请求本身总是会得到处理：header 加在响应上，不会重试任何东西，也不会丢失任何批次。

**`idle_timeout` 有默认值，且不能关闭。** 没有它，每个被遗弃的客户端都会占用一个文件描述符，直到进程重启。如果你想要，可以设置得很长；那时它就是某人选定的数字。

这两者相互独立：连接遇到先达到的那个限制，而连接只有被使用才能达到存续时间，这恰恰是存续时间要重新平衡的长连接生产者。

## 当 sink 拒绝时

批次会被重试，默认在大约七秒内尝试四次，带抖动地翻倍，然后进入 **dead letter**，每条记录上都带有原因：

```json
{
  "event_id": "dl-9",
  "ingestion_id": "84aaee4b-66af-5cc0-b97b-6856dec95b25",
  "_dead_letter_reason": "rpc error: code = NotFound desc = Topic not found",
  "_dead_letter_sink": "pubsub:acme-prod/clicks",
  "_dead_letter_at": "2026-09-25T02:02:32Z"
}
```

原因**随记录一起传递**，而不只是写在日志里，因为之后找到那个文件的人拿到的是事件而不是日志，而*为什么这个在这里*是他们的第一个问题。

**没有 `dead_letter` 的 stream 在加载时会被拒绝。** 默认静默，等于把决定放在了没有人做决定的地方，而只留一行日志的被拒绝批次，就是在悄悄丢失数据。

## hook 是 Go，编译进去

```go
hooks := gateway.NewHooks()
hooks.MustRegister("enrich_clicks", enrichClicks)
gateway.Main(hooks, gateway.WithSinks(sinks))

func enrichClicks(e map[string]any) (map[string]any, error) {
    host, _ := e["host"].(string)
    e["tenant"] = strings.Split(host, ".")[0]
    return e, nil     // returning nil DROPS the event, on purpose
}
```

YAML 指定一个 hook 的名称，但并不携带它。选择之前做过测量：一个 Go 函数的开销是 **93 ns/op**，而 Starlark 是 959，yaegi 是 1,281，且不会给二进制文件增加任何东西；`plugin.Open` 则根本不是选项，因为在 `CGO_ENABLED=0` 下（这里所有制品都是这样构建的），它会返回 `plugin: not implemented`。

代价是什么，写在会有人读到的地方：**增加一个 hook 需要重新构建和部署，而不是修改配置。** 当 hook 由发布二进制文件的人来写时，这是正确的取舍；而在某天客户需要不经过发布就修改一个 hook 时，这就是错误的取舍。

hook 中的错误会让*那个*事件走 dead letter 的路径，绝不会让旁边的批次失败。panic 也一样：它按事件被恢复，因为一条格式错误的记录不能拖垮一个为所有其他 stream 提供服务的进程。

## 当事件过大时

```yaml
oversize:
  larger_than: 256KiB
  archive: {type: files, path: gs://acme-oversize/clicks/}
  hook: strip_heavy_clicks
```

事件会被**完整**写入 `archive`，而一个精简版本带着指回它的指针继续进入 stream：

```json
{
  "event_id": "big-1",
  "ingestion_id": "bd8ac083-9796-50f5-bab6-77a4e2e57ad0",
  "_oversize": true,
  "_oversize_archive": "files:gs://acme-oversize/clicks/",
  "_oversize_bytes": 2149
}
```

这是 **Claim Check** 模式，它胜过简单的 `413`，因为 `413` 会丢失事件，而过大的载荷通常是某人手里最有价值的那个：它是附带了整份文档的请求，正是值得调试的情形。

`larger_than` 度量的是 hook 之后的一个**事件**。`listen.max_body` 则是另一回事：它限制整个**请求**，并在解析任何一个字节之前就以 `413` 拒绝。

## 它统计什么

十一个 Prometheus 序列，在**它自己的端口**上，绝不是摄取端口。这条规则在这里比在引擎中更重要：摄取端口按其构造就是公开的，客户端就是向它 `POST`，所以在上面放一个 `/metrics`，会把每个 stream 的名称、路径和目的地都公布给任何找到这个路径的人。为两者指定同一个地址，在加载时会被拒绝。

```
brevis_gateway_events_received_total{stream,format}     counter
brevis_gateway_events_rejected_total{stream,reason}     counter  per event, after decode
brevis_gateway_requests_refused_total{stream,reason}    counter  per request: unauthorized|body_too_large|malformed|empty|saturated
brevis_gateway_batches_total{stream,sink,outcome}       counter  delivered|retried|buried
brevis_gateway_reconciled_total{stream,sink}            counter  landed after the send window; also delivered
brevis_gateway_flushes_total{stream,trigger}            counter  time|records|size
brevis_gateway_flush_windows_total{stream,outcome}      counter  won|yielded|ceiling|unreachable
brevis_gateway_ingested_bytes_total{stream,table}        counter  opt-in
brevis_gateway_ingested_events_total{stream,table}      counter  opt-in
brevis_gateway_table_flushes_total{stream,table,trigger} counter  opt-in
process_start_time_seconds                              gauge    unprefixed, on purpose
brevis_gateway_saturated_total{stream}                  counter  the 503s
brevis_gateway_delivery_seconds{stream,sink}            histogram
brevis_gateway_buffer_records{stream}                   gauge
brevis_gateway_queue_batches{stream}                    gauge
```

`process_start_time_seconds` **没有**加 `brevis_` 前缀，这是有意为之：采集器查找的就是这个名称。它说明进程何时启动；没有它，执行起始时间调整的采集器（比如 OpenTelemetry Prometheus receiver，Google Managed Prometheus 就建立在它之上）会把每个累计序列锚定在它的**第一次抓取**，并把那次抓取的值当作基线。结果是每个计数器读数偏低，增量是对的，总量却是错的。

最后三个是在第一次事故之后才会有人想要的。`saturated_total` 是唯一能说明客户端曾被告知退避的数字；`buffer_records` 与 `buffer.max_records` 的比值，则是唯一能**预测**它的数字。

`reason` 是封闭集合，绝不是错误文本：错误字符串带有表名、列名，有时还带有一行数据，一个格式错误的客户端会让每个请求都生成一个新的序列。文本留在 dead letter 中，保存在记录上。

## `202`，而不是 `200`

gateway 已经**接受**了这些事件。在 `durability: memory`（唯一已实现的级别）下，这就是它能够如实声称的全部。配得上 `200` 的级别是已经把它们写下来的级别，而它并不存在。

`disk` 会**按名称**被拒绝，而不是被接受后当作 memory 处理：写下 `disk` 的人相信自己的事件能在崩溃后幸存，而答应了却不兑现，恰恰是这个字段要防止的失败。

缓冲区中在关闭时持有的内容会被投递，而不是被丢弃，走的是与已满批次相同的路径，同样会重试和被埋入 dead letter。

## 它不做什么

- **它不会出现在 Console 中。** 它与引擎的界面不共用任何数据库，所以没有 `/ingestion` 页面，`auto_table` 执行的 schema 演进也不会在那里显示。这是下一步，尚未编写。
- **带 `table` 的 stream 不会演进 schema。** 当你指定了表名，契约就是表的契约：它没有的字段会被拒绝，并附上修复它的信息，而不是修改一张有人评审过的表。自行增加列是 `auto_table` 做的事，这正是两者的区别。
- **带 `table` 的 stream 不会创建表，也不会创建索引。** 会创建表的服务，会把一个拼写错误变成没有人在读的第二张表，这就是为什么创建只发生在 `auto_table` 下，在那里 `naming` 限定了哪些名称可以存在，并且 `listen.auth` 是强制的。
- **已发布的镜像没有 hook**，这对于编译式 hook 的设计来说是诚实的制品：它为没有声明 `hook:` 的 stream 提供服务。

## 一条路由，N 张表

`auto_table` 把每个事件路由到**信封**所指定的表，如果该表不存在就创建它，当出现新字段时就增加一列：

```json
{"table_name": "app_orders", "operation": "INSERT", "unique_key": "id",
 "data": {"id": "A-1", "total": 150, "customer": {"id": 7, "uf": "SP"}}}
```

控制字段在顶层，记录在 `data` 内部，每张表都有七个 `brevis_*` 跟踪列。一条路由，N 张表，无需声明任何东西。

它位于[摄取 sink](/zh/docs/ingestion-sinks/#one-route-n-tables-nothing-declared) 中。

## 接下来去哪里

| 如果你想 | 前往 |
|---|---|
| 各个目的地以及它们各自如何写入 | [摄取 sink](/zh/docs/ingestion-sinks/index.md) |
| 从另一侧看 `ingestion_id` | [Go SDK](/zh/docs/sdk/index.md) |
| 仪表盘和告警 | [可观测性](/zh/docs/observability/index.md) |
