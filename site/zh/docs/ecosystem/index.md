# 生态

> brevis.sh 的四个组成部分、各自的职责，以及数据经过的路径。

*https://brevis.sh/zh/docs/ecosystem/ · brevis.sh docs (zh-CN)*

---

Core 负责调度、排队和监控，从不接触数据。真正接触数据的是：负责抓取的 SDK、负责接收的 Gateway，以及在数据落地之后对其建模的 SQL。

## 四个组成部分

| 组成部分 | 作用 | 获取方式 | 指南 |
|---|---|---|---|
| **Core** | 调度、排队，并为每个 step 启动一个 pod。从不接触数据。 | `brevissh/brevis` | [Core](/zh/docs/core/index.md) |
| **SDK** | 一个 Go 库，在你的 step 内部完成抽取、整形和加载。 | `go get github.com/AreteAcademy/brevis/sdk` | [Go SDK](/zh/docs/sdk/index.md) |
| **Gateway** | 接收 HTTP POST，返回 202，并分批写入。自身没有数据库。 | `brevissh/brevis-gateway` | [摄取](/zh/docs/ingestion/index.md) |
| **SQL** | 普通的 .sql 文件按依赖顺序变成表和视图。 | `brevissh/brevis-sql` | [SQL](/zh/docs/sql/index.md) |

## 控制平面与数据平面

**控制平面**就是 Core：它决定每个 step 何时运行，把队列和状态保存在 Postgres 中，并为每个 step 启动一个 pod。数据从不经过它。

**数据平面**是另外三个组成部分。数据来自 API、数据库或文件，通过两种方式之一到达目的地：由 **SDK 抓取**（在你自己的 step 内部），或者由 webhook 和应用通过 HTTP **推送到 Gateway**。目的地可以是表、topic，或 bucket 中的文件。数据落地之后，**SQL** 就在它所在的数据库里直接对表建模。

### 周边

另有三个配套部分：

- **[Console](/zh/docs/console/index.md)** — Core 之上的 Web 界面
- **[agent](/zh/docs/pod-per-step/#where-each-step-runs)** — 在你已有的集群中运行 step
- **[`brevis` for Python](/zh/docs/python/index.md)** — 为 Python step 提供运行上下文

## 只使用其中一个组成部分

可以。每个组成部分都有自己的模块、版本和镜像：SDK 是一个可以在任何程序中运行的 Go 库，Gateway 只需一个 YAML 文件即可独立启动，无需数据库，brevis-sql 则作为容器运行。你需要时，Core 可以编排其他几个部分，但它不是其中任何一个的前提条件。

## 同一行数据

SDK 抓取的订单与到达 Gateway 的订单，会以相同的 brevis_ingestion_id 落到同一张表里。在两者之间选择，变成了部署层面的决定，而不是 schema 层面的决定。

通过 SDK，使用 `sdk.Landing`（详见[落地布局](/zh/docs/sdk/#the-landing-layout)）：

```go
sdk.Execute(ctx, &sdk.Pipeline{
	Source:    sdk.Source{From: from.Files{Path: "order.json"}},
	Transform: []sdk.Transformer{sdk.Landing("app_orders", sdk.LandingKey("id"))},
	Target: sdk.Target{
		To:     postgres.Table{DSN: dsn, Name: "app_orders"},
		Schema: sdk.LandingSchema(sdk.LandingOptions{}),
	},
}, nil)
```

通过 Gateway，向 `/v1/tables` 发送 POST（详见[一条路由，N 张表](/zh/docs/ingestion/#one-route-n-tables)）：

```bash
curl -X POST localhost:8080/v1/tables -H 'Authorization: Bearer dev-key' \
    -d '{"table_name":"app_orders","unique_key":"id",
         "data":{"id":"A-1","customer":"acme.example.com","total":150}}'
```

响应：

```text
{"accepted":1,"rejected":null}  202
```

落到 `app_orders` 中的内容：

| 列 | 通过 SDK | 通过 Gateway |
|---|---|---|
| `brevis_ingestion_id` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` |
| `brevis_record_key` | `A-1` | `A-1` |
| `brevis_operation` | `INSERT` | `INSERT` |
| `brevis_gateway` | `NULL` | `acme_gateway` |
| `data` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` |

真实输出：两条路径都使用已发布的镜像和 SDK，在同一个本地 Postgres 上运行。唯一的区别是写入者不同：SDK 写入的那一行里，brevis_gateway 为空。
