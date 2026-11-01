# Go SDK

> 编写抓取器：从源中抽取、转换，并加载到目的地。

*https://brevis.sh/zh/docs/sdk/ · brevis.sh docs (zh-CN)*

---

当 step 的任务是**抓取数据并加载到某处**时，Go SDK 只用几行代码就能给你一个完整的抓取器。它是一个独立的模块，版本与引擎各自独立。

```bash
go get github.com/AreteAcademy/brevis/sdk@latest
```

需要 Go 1.23 或更新版本：SDK 以 `iter.Seq2` 流式传输行。

:::warning 不要使用 `v0.1.0`
它发布时的 `go.mod` 固定了一个不存在的 revision，而 Go 模块代理是不可变的。请从 `v0.1.1` 开始。
:::

## 三个步骤

```go
import (
	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to/bigquery"
)

rows, err := sdk.Extract(ctx, sdk.Source{
	From: from.HTTP{URL: "https://api.example.com/v1/events"},
})

rows = sdk.Transform(rows, sdk.Accept("id", "created_at", "amount"))

res, err := sdk.Load(ctx, rows, sdk.Target{
	To:      bigquery.Table{Dataset: "bronze", Name: "events"},
	Columns: []string{"id", "created_at", "amount"},
})
```

`Extract` 读取，`Transform` 重塑，`Load` 写入。每个函数都接收并返回一个序列：不会一次性把所有内容物化到内存中。

![SDK 的流程：sdk.Extract 通过 from.HTTP 从 HTTP API 读取，sdk.Transform 保留接受的字段，sdk.Load 把行写入 BigQuery 表。这三个调用都在你自己的 step 内部运行。](/assets/flow-sdk.svg)

## 驱动是一个值，而不是一项设置

`from.HTTP` 携带 HTTP 源所需的一切：URL、header、重试、分页，以及响应的含义。`from.Files` 携带路径和格式。两者都不必为对方的字段留出位置，因此不存在一个收集了四十个选项、而每个驱动只读其中六个的源结构体。

它还决定了**你编译什么**。Go 是按导入的包来裁剪依赖，而不是按使用的字段：

| 你导入什么 | 包数量 | AWS | Google |
|---|---|---|---|
| `sdk` | 190 | 否 | 否 |
| `sdk` + `from` | 194 | 否 | 否 |
| `sdk` + `from` + `to`（文件） | 195 | 否 | 否 |
| `sdk` + `to/bigquery` | 456 | 否 | **是** |
| `sdk` + `from` + `store/s3` | 265 | **是** | 否 |

一条完整的文件 pipeline（读取并写入）只需 195 个包，完全不需要任何云 SDK。**背后有厂商 SDK 的驱动放在它自己的包里**，这就是为什么 BigQuery 是 `to/bigquery`，而对象存储是 `store/s3` 和 `store/gcs`。

## 源与目的地

| 源 | 包 |
|---|---|
| HTTP | `from.HTTP` |
| 文件（本地、S3、GCS） | `from.Files` |
| Postgres | `from/postgres` |
| MySQL | `from/mysql` |
| 多个源 | `from.Many` |

| 目的地 | 包 |
|---|---|
| 文件 | `to.Files` |
| BigQuery | `to/bigquery` |
| Postgres | `to/postgres` |
| MySQL | `to/mysql` |
| Redshift | `to/redshift` |
| Pub/Sub | `to/pubsub` |

路径的 scheme 决定后端，而 `Store` 是**传入**的，而不是在驱动内部选择：

```go
from.Files{Path: "s3://bucket/day=1/*.ndjson", Store: s3.New(client)}
to.Files{Path: "gs://bucket/landing/", Store: gcs.New(client)}
```

这就是为什么只用本地文件的程序不会编译进哪怕一行 AWS 或 Google 的代码。

### 发布到 topic

`to/pubsub` 是唯一一个不是表的目的地，它的行为有一点不同，使用之前值得了解：**它不会给发送的内容添加任何东西。**

```go
Target: sdk.Target{
    To: pubsub.Topic{
        Project: "acme-prod",
        Name:    "orders",

        // Nil is the default, and it means NO attributes at all.
        Attributes: func(e sdk.Envelope) map[string]string {
            row, ok := e.Payload.(map[string]any)
            if !ok {
                return nil
            }
            return map[string]string{"orderId": fmt.Sprint(row["order_id"])}
        },
    },
},
```

订阅方收到的是你的载荷，逐字节不变，外加你明确指定的属性。仅此而已：没有 `ingestion_id`，没有 `provider`，也没有任何形式的信封。

这是一条规则，而不是极简主义。表是由写入它的 pipeline 创建的，所以 SDK 可以决定它的列是什么。**topic 则不然**：它先于你的 pipeline 存在，它的订阅方和订阅方的过滤器也都先于你的 pipeline 写成。自行添加的属性，等于 Brevis 在修改别人的契约，订阅方会在凌晨三点才发现。

由此引出三点：

- **它从不创建 topic。** 能创建 topic 的 pipeline 也能创建错误的 topic，而且与拼错的表名不同，没有人会注意到：消息发往了别处，本该收到的订阅方却毫无动静。
- **`Schema`、`Dedup` 和 `PartitionBy` 会被拒绝**，并指出具体的选项。它们是为表而存在的：`Schema` 是目的地创建表的方式，去重需要有一行可以替换，分区是表的概念。这里与之最接近的是 `OrderingKey`，但它是另一回事：它把必须按顺序*到达*的消息分为一组。
- **失败是部分的。** 发布不是事务性的，所以发布 48,000 条消息在 31,000 条处失败，就意味着已经*投递*了 31,000 条。结果会报告实际发出的数量，错误信息会说明重新运行就是重新投递：订阅方必须是幂等的，而且 Pub/Sub 本来就是至少一次投递。

可运行，针对模拟器：
[`examples/13-pubsub`](https://github.com/AreteAcademy/brevis/tree/master/examples/13-pubsub)。

## 完整的抓取器

`sdk.Run` 负责标志、`-dry-run`、日志、重试、分页、来源追踪、建表和退出码。文件里剩下的，只有该数据源特有的部分：

```go
package main

import (
	"time"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
)

func main() {
	sdk.Run(sdk.Pipeline{
		Source: sdk.Source{
			From: from.HTTP{
				URL:     "https://api.example.com/v1/events",
				Timeout: 15 * time.Second,
			},
			Guard:  sdk.RejectIf("error"),
			Expand: sdk.ArrayAt("results"),
		},
		Target: sdk.Target{
			Provider: "example",
			Entity:   "events",
			Key:      sdk.Key("id"),
			When:     sdk.Field("created_at"),
		},
	})
}
```

```bash
go run ./fetcher -dry-run   # extracts, counts rows and errors, writes nothing
go run ./fetcher
```

## run 上下文

当抓取器**作为 workflow 的 step** 运行时，引擎会向环境中注入它原本无从得知的信息：这是否是首次运行、它是以哪些参数被调度的、它是哪一个 run。SDK 在 `Pipeline.Run` 中读取这些信息：

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	if p.Run.Params["load_full"] == "true" {
		p.Source.From = from.HTTP{URL: base + "?full=1"}
	}
	return nil
},
```

手动运行时，`Run` 返回零值：读取它是可选的，忽略它没有任何代价。

在没有历史记录时，“这是首次运行吗？”的答案始终是**否**：在不确定的情况下创建表，比不创建更糟。

### 时钟

`p.Run.Auto` 是引擎对这个 run 所算出的信息，即[自动参数](/zh/docs/parameters/#auto-params)。`Auto.Now()` 是用来取代 `time.Now()` 的时钟：在调度的 run 上它就是 slot，所以 run 迟到时它不会移动，三个小时后重试时它也不会移动。

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	start, end, ok := p.Run.Auto.Window()
	if !ok { // no schedule: fall back to a fixed window
		start, end = p.Run.Auto.Now().Add(-24*time.Hour), p.Run.Auto.Now()
	}
	p.Source.From = from.HTTP{URL: base +
		"?from=" + start.Format(time.RFC3339) +
		"&to=" + end.Format(time.RFC3339)}
	return nil
},
```

手动运行时，`Auto.Now()` *就是*挂钟时间，`Window()` 返回 `false`，因此本地开发无需任何特殊处理。

## 不要与客户端库混淆

这个 SDK 是用 Go 编写的 ETL 机制：驱动、分页、来源追踪、建表。[客户端库](/zh/docs/libraries/index.md) 则是另一回事：它们是轻量级客户端，把 run 上下文和时钟交给 step，目前提供 Python，之后会有 Node.js 和 Rust。使用 pandas 或 dbt 的 step 需要的是后者，而不是这个。

## 构建 pipeline 的参数

`p.Run.Params` 在 pipeline 内部可读，但对于决定 pipeline **是什么**的值来说为时已晚，比如每个州一个源、每个租户一张表。`Pipeline.Flags` 是在 `Run` 内部解析的，同样为时已晚。

```go
func main() {
	sdk.Run(pipeline(sdk.ParamList("ufs")))
}
```

`sdk.Param` 和 `sdk.ParamList` 先读取引擎的环境变量，再读取 `-param name=value`：

```bash
./fetch-stations -param ufs=SP,RJ
```

环境变量优先：在引擎下它才是真正的值，留在 manifest 中的标志不能覆盖操作者输入的内容。没有引擎时，标志就是唯一可用的来源，这比每次运行都把 `BREVIS_RUN_PARAMS` 写成 JSON 要好。

## 落地布局

pipeline 可以落地与 gateway **相同的表**：相同的列，相同的 `brevis_ingestion_id`，因此在两者之间选择是部署层面的决定，而不是 schema 层面的决定。团队可以把两者放在一起读取，并且无需迁移就能从一种换到另一种。

```go
const table = "landing.orders"

sdk.Run(sdk.Pipeline{
    Source:    sdk.Source{From: from.HTTP(/* ... */)},
    Transform: []sdk.Transformer{sdk.Landing(table, sdk.LandingKey("id"))},
    Target: sdk.Target{
        To:     postgres.Table{DSN: dsn, Name: table, CreateTable: true},
        Schema: sdk.LandingSchema(sdk.LandingOptions{}),
    },
})
```

`sdk.LandingSchema` 有九列：布局拥有的八列，以及生产者拥有的一列。`sdk.Landing` 负责填充它们，并把整条记录放进 `data`。如果需要，可以把你自己的列追加到 schema 中，或者使用 `sdk.LandingControlColumns`，让记录的各个字段拥有自己的列。

### 两种形态：整条记录，或每个字段一列

上面的是**文档**形态：记录以 JSON 形式放进 `data`，无论记录包含什么，表都是同一张。加上 `sdk.LandingColumns()`，记录的每个字段就会有自己的一列：

```go
sdk.Landing(table, sdk.LandingKey("source_key"), sdk.LandingColumns())
```

这时的表是 `sdk.LandingControlColumns` 加上你的列，**而不是**带有 `data` 的 `sdk.LandingSchema`：

```go
Target{
    To: postgres.Table{DSN: dsn, Name: table, CreateTable: true},
    Schema: append(sdk.LandingControlColumns(sdk.LandingOptions{Keyed: true}),
        sdk.Column{Name: "source_key", Type: sdk.TypeString},
        sdk.Column{Name: "series", Type: sdk.TypeString},
        sdk.Column{Name: "data", Type: sdk.TypeString},
        sdk.Column{Name: "valor", Type: sdk.TypeString},
    ),
}
```

**这些列由你声明。** SDK 从不推断 schema，这个选项也不改变这一点：它决定的是*行*如何构建，而不是表是什么。gateway 会推断，因为它的生产者是随便发送手头数据的陌生人；而在这里，pipeline 的作者知道结构并把它写下来，写在会有人评审的 diff 里。

它与 gateway 以 [`shape: columns`](/zh/docs/ingestion-sinks/#two-shapes-one-contract) 落地的形态相同，走的是同一段代码：相同的列，相同的值，相同的 `brevis_ingestion_id`。

**字段名必须能够用作列名**：以字母或下划线开头，后面是字母、数字和下划线。这是 BigQuery 的规则，也是四个目的地中最严格的，所以在这里通过的名称在所有地方都能用。带有 `my-field` 的记录会在写入任何东西之前，按名称被拒绝。文档形态没有这条规则：连字符作为 JSON 键完全没有问题。

**默认不会展平。** 嵌套对象会变成以持有它的键命名的一个 `JSON` 列：`customer` 保存 `{"id": 7, "uf": "SP"}`，而不是 `customer_id` 和 `customer_uf`。数组同样是 `JSON`。如果希望*每个数组元素*一行，需要在源的 `Expand` 中使用 `sdk.ArrayAt`：它在 `Landing` 之前运行，是另一个名称不同的操作。[`BREVIS_NORMALIZE_DATA`](#one-column-per-nested-field) 会对整个部署改变前半部分的行为；无论如何，数组仍然是 `JSON`：

```go
Source: sdk.Source{From: from.HTTP(/* ... */), Expand: sdk.ArrayAt("results")},
```

**`null` 保持为 `NULL`**，绝不会变成 `""`。字段发送为 null 与字段发送为空是不同的事实，而列在事后无法区分它们。

**`data` 归你所有。** 在这种形态下，布局中没有同名的列，因此带有名为 `data` 的字段的生产者（Bacen 的序列恰好就是这样）会得到属于自己的一列，里面是他们自己的值。在 `document` 下，他们的字段只是布局的 JSON 中的一个键。

### `brevis_` 前缀可以由你修改

```
BREVIS_LANDING_PREFIX=acme
```

这样布局的八个列就变成 `acme_ingestion_id`、`acme_record_key` 等等。不设置时，它们保持一直以来的样子。

**请在创建表时就选定它，因为之后再修改并不是重命名。** 这个 SDK 从不删除任何列，所以切换前缀的 stream 并不是重命名八个列，而是新增八个、遗弃八个。此后的每一行，旧的那一组都是 NULL，而

```sql
qualify row_number() over (partition by brevis_record_key
                           order by brevis_received_at desc) = 1
```

则会在一个不再有任何东西写入的列上分区。行仍在到达，仪表盘却一片平坦，而没有任何日志记录这件事。

因此，已经带有某个前缀布局的表，会在抽取之前**拒绝**声明了另一个前缀的加载，并指出两者。请把它指向一张新表，或者先自己重命名这八个列。

**你输入的内容都会被规范化**，因此需要写对的只有一种写法，而不是四种：

| 你设置的值 | 你得到的结果 |
|---|---|
| 无，或空白 | `brevis_` |
| `_NAME` | `name_` |
| `_NAME_` | `name_` |
| `NAME` | `name_` |

去掉两端的 `_`，转为小写，再恰好加上一个。有两种值会被拒绝而不是猜测：只含下划线的值，因为它会留下 `ingestion_id`，这恰恰是 `sdk.IngestionID()` 为每个普通 pipeline 写入的那一列；以及无法作为列名开头的任何值。**带有不可用值的进程不会启动**，这是唯一诚实的结果：否则就会出现以你正在替换的前缀命名的表。

**它是按进程设置的，这是重点，而不是局限。** 同一个 gateway 中的所有 stream 共享它，所以两个 sink 之间不会产生分歧，声明表的 schema 与填充表的转换器之间也不会。

**保留规则随之移动。** 在 `acme_` 下，发送 `acme_region` 的生产者会被拒绝，因为能够伪造控制列的人会伪造一个看起来真实的列。而 `brevis_region` 则变成普通字段。

`data` 从不带前缀。它是生产者的列，不是布局的列。

### 每个嵌套字段一列

```
BREVIS_NORMALIZE_DATA=true
```

这样嵌套对象的字段会有自己的列，就像 pandas 的 `json_normalize` 那样，只展开一层：

```python
data = [
    {"id": 1, "name": {"first": "Coleen", "last": "Volk"}},
    {"name": {"given": "Mark", "family": "Regner"}},
    {"id": 2, "name": "Faye Raker"},
]
```

```
id  name_first  name_last  name_given  name_family  name
```

**请在第一张表之前就选定它，因为它不只是新增列。** 它会重命名列，而这个 SDK 从不删除任何列，所以旧名称会留在已有的行中，而之后的数据都落到新名称里，有人针对旧列写的查询仍会运行，却不再看到新行。会发生两种重命名：

- `userName` 变成 `username`，无论是否有嵌套
- 原本保存对象的 `name`，被 `name_first` 和 `name_last` 取代

已经带有任一形态的表，会在抽取之前**拒绝**开启此选项的加载，并指出这两个列。请把它指向一张新表，或者先自己重命名这些列。

**`name` 可以同时以展平和完整两种形式出现**，上面的例子就说明了这一点：第三条记录的 `name` 是字符串，所以表中既有 `name_first`、`name_last`，也有 `name`。一个字段在某条记录中是对象、在另一条记录中是标量时，两者都会产生。

**只展开一层，数组是值。** `{"a": {"b": {"c": 1}}}` 得到的是保存 `{"c": 1}` 的 `a_b`，是 `JSON` 列，而不是 `a_b_c`。`{"a": [1, 2]}` 得到 `a`，同样是 `JSON`。如果希望每个数组元素一行，仍然需要 `sdk.ArrayAt`。

**拼接并转为小写，不按驼峰拆分。** `userName` 变成 `username` 而不是 `user_name`，因为另一种解读对首字母缩写没有公认的答案，比如 `HTTPStatus`、`userID`，每个库的处理方式都不一样。

**冲突会被拒绝，而不是被解决。** `{"name_first": 1, "name": {"first": 2}}` 会让两个字段产生同一个列，而 Go 的 map 遍历顺序是随机的：让其中一个胜出，会使同一批数据在不同次运行中得到不同的表。拒绝信息会指出两者。

**保留前缀检查的是列，而不是字段。** 发送 `{"brevis": {"stream": "x"}}` 的生产者，否则会通过字段检查没有监视的入口生成 `brevis_stream`。它会被拒绝，并指出 `brevis.stream`，这样他们可以找到那个字段。

**它是按进程设置的**，与前缀一样，原因也相同：同一个 gateway 中的所有 stream 共享它，所以两个 sink 不会对它们都可能写入的表产生分歧。它只适用于 `columns` 形态：在 `document` 下，记录整个放进一个 `JSON` 列，没有可展平的目标。

### 本身已是 JSON 的文本

声明为 `sdk.TypeJSON` 的列接收的是 JSON **值**：对象、数组、数字。在 BigQuery 上，行是整体编码的，所以那里的 Go `string` 会变成 JSON 字符串字面量，列里保存的是你对象的文本，而不是对象本身：

```sql
SELECT JSON_TYPE(payload) ...;            -- "string", not "object"
SELECT JSON_VALUE(payload, '$.a') ...;    -- NULL
```

列的类型是对的，但其中的数据无法访问，这是三种结果里最糟糕的一种，因为没有任何东西失败。**BigQuery 目的地自 `v0.74.0` 起会拒绝这种情况**，并指出列名。

手里有对象时，直接传对象。如果你持有的已经是 JSON 文本，比如从另一个数据库读出的列，或者不想重新解析的响应体，就明确说明：

```go
row["payload"] = sdk.JSONText(body)
```

`encoding/json` 自带的 `json.RawMessage` 也可以，原因相同：两者写入的都是它们所持有的 JSON，而不是它的带引号字符串。

**Postgres 和 MySQL 不受影响，也不会被拒绝。** 它们的服务器会把字符串解析到 JSON 列中，一直如此。

### 当出现表中没有的字段时

生产者新增了一个字段，加载就会停止，并指出该字段：行带有一个没有任何声明的列，写入一个从未提及它的目的地会悄悄丢掉它。这种拒绝在默认情况下是正确的，但在落地表上，这是唯一一种不可能破坏读取方的变更，目的地可以选择接受它：

```go
postgres.Table{DSN: dsn, Name: table, Evolve: sdk.EvolveAdditiveFromPayload}
```

这样，批次会**补全**你的声明：它携带而声明中没有的字段会变成一列，类型为 `STRING`，除非该字段是对象或数组，那时为 `JSON`。这是与 gateway 落地 `shape: columns` 时相同的规则、相同的代码，所以两者产生同样的表。

另外还有两个值，它们的区别在于列从哪里来：

| `Evolve` | 新增 |
|---|---|
| `sdk.EvolveNone`（默认） | 什么都不加。任何差异都会被拒绝，并指出双方 |
| `sdk.EvolveAdditive` | 你的**声明**中有而表中没有的列 |
| `sdk.EvolveAdditiveFromPayload` | 上面那些，外加**批次**所携带的列 |

**永远只做 `ADD COLUMN`。** 从源中消失的列不再被写入，但会留在表里；不重命名，不删除，也绝不收窄任何类型。这不是缺陷：落地表就是历史，而唯一不可能破坏读取者的变更，就是新增一个可为空的列。

**它补全声明，而不是替代声明。** 在没有任何声明时，它会在抽取之前被拒绝，并指出表名：根据载荷创建整张表是另一种决定，SDK 不会做出这个决定。在落地布局上，危害是具体的：`brevis_received_at` 会成为文本而不是时间戳，而 `brevis_loaded_at` 根本不在行中传递，所以端到端延迟的度量会直接缺失。

**SDK 仍然不会推断。** 类型来自字段的形状，而不是它的值：`21129` 和 `8.89` 都声明为 `STRING`，所以哪天序列发布了整数，也不会有任何变化。根据第一批数据读出类型，正是这条规则要防止的失败。

**`null` 不决定任何东西，但它会保留在行中。** 这是两个不同的问题，用同一个答案回答它们，曾让 v0.77.0 付出代价。

null 没有形状，所以 **schema** 无从读取：以 `null` 到达的字段不会声明列，列从第一条携带*值*的记录出现，其形状就是该值的形状。反过来，null 会因为没有答案而变成 `STRING`，而其后的第一个数组会让目的地拒绝整个批次，并且没有从 `STRING` 到 `JSON` 的迁移可以撤销它。

**行**中携带生产者发送的内容，包括 null。必须如此：你在 `Target.Schema` 中*声明*的列如果到达时为空，就必须出现在行里，否则对照声明与行的检查，读到的是一条已经不再产生它的链路，并拒绝加载。这就是 v0.77.0 的做法，也是 0.78.0 撤销的内容。

其余的由目的地处理：在批次的每条记录中都为 null、且表中没有对应列的字段，不会被写到任何地方，也不会导致任何拒绝。null 和缺失的字段落地为同一个 `NULL`，因此没有任何东西丢失。

**形状发生变化的字段会被拒绝，而不是被吸收。** 如果 `valor` 起初以标量到达并生成了文本列，之后到达的对象就是种类的变化，批次会停止，信息中带有该列自己的注释，这样你能看到没有人声明过它。请重命名该列，或者自己迁移。

**在 BigQuery 上，同一名称的两种写法是同一列。** BigQuery 会折叠列名的大小写：一个批次中，一条记录带有 `nationalID`，另一条带有 `nationalId`，声明的是**一个**列，而不是两个，胜出的写法是排序后的第一个，即 `nationalID`，因为 `D` 排在 `d` 之前。不是最先到达的那个：重放同一批数据不得创建拼写不同的列。声明 `createdat` 而表中的列是 `createdAt`，也是同一列，因此既不新增也不拒绝。

**一条记录同时带有两种写法会被拒绝，并指出两者。** 目的地只有一个列，两个值中有一个会悄悄胜出，所以加载会停止：请在 `Transform` 中重命名其中一个，或者把两者映射到同一个字段。两条*记录*各带一种写法则没有问题：那是普通的 CDC 流量，正是这条规则要落地的内容。

**不会迁移任何东西。** 已经有两个仅大小写不同的列的表会保留两者；不重命名任何列，也不删除任何列。

**Postgres 和 MySQL 不折叠大小写。** 带引号的标识符在那里是不同的，所以两种写法是两个列，这是有意的，而指向 BigQuery 的同一个抓取器会把它们落到同一列。

**批次创建的每个列，都会在表中说明这一点。** 在 Postgres 和 MySQL 上是注释，在 BigQuery 上是字段描述，并带有日期：

```
brevis: added from a batch on 2026-09-29; the type is the landing rule
(scalar text, object and array json), not a decision
```

六个月后，这是唯一能回答“这个列是什么时候出现的、是谁决定的”的东西。到那时日志行早已轮转掉了。

**它不是一种模式。** `Target.Schema` 仍然是你在声明表是什么：这只是把一个已经存在的布局交给你，免得你自己输入。SDK 中没有任何东西因为你使用了它而表现不同，声明了自己 schema 的 pipeline 不受影响。

**表名只写一次。** 它要传给 `Landing`，因为它是 id 的第二个槽位，也要传给目的地，因为行要写到那里。**没有任何东西检查两者是否一致**：`Writer` 知道自己的表，但只通过 `Describe` 暴露它，而那是用于日志和错误的名称，基于它构建 id，会把每个已写入的 id 与一个日志字符串绑在一起。两者不一致时，行会正确落地，但 id 是为一张没有人写入的表生成的。它们看起来没问题，却不会与 gateway 的 id 匹配。

**有三列是 `NULL`，这是诚实的答案。** `brevis_stream` 和 `brevis_gateway` 命名的是 pipeline 所没有的东西，`brevis_gateway IS NULL` 就是区分 pipeline 的行与 gateway 的行的方式。`brevis_received_bytes` 同样是 `NULL`：gateway 统计的是线路上收到的内容（包括信封），而记录自身的 JSON 小了大约 30%。同一列有两种含义，会让对两条路径的行求和时，按各自占比而出现偏差。`length(data)` 能准确回答 pipeline 的版本。

**合并需要 `DedupKey`，没有它就根本没有合并。** 除非另行指定，每个驱动都按 `ingestion_id` 匹配，而这个布局的标识列是 `brevis_ingestion_id`：

```go
Target{
    To:       postgres.Table{DSN: dsn, Name: table, CreateTable: true},
    Schema:   sdk.LandingSchema(sdk.LandingOptions{UniqueID: true, Keyed: true}),
    Dedup:    sdk.DedupMerge,
    DedupKey: sdk.LandingColumnID,
}
```

`UniqueID` 会给 id 加上 UNIQUE 约束，Postgres 和 MySQL 在没有它时都拒绝合并，因为 `ON CONFLICT` 没有可匹配的对象。你的 `Schema` 中没有声明的 `DedupKey` 会在任何东西运行之前被拒绝，因为基于表中没有的列进行合并，什么都匹配不到，而什么都匹配不到的合并，看起来与匹配了所有应匹配内容的合并一模一样。

**因目的地而异，目前 MySQL 做不到。** BigQuery 没有唯一约束，会直接拒绝该声明。MySQL 的 `string` 是 `LONGTEXT`，而 MySQL 不允许对没有长度的 `LONGTEXT` 建立键，所以带合并的落地表在那里根本无法*创建*：

```
Error 1170 (42000): BLOB/TEXT column 'brevis_ingestion_id' used in key
specification without a key length
```

Postgres 可以工作，因为它的 `string` 是 `TEXT`。在 MySQL 上，请使用 `DedupNone` 落地，并在下游解析当前版本，或者用 `CreateSQL` 和指定长度的 id 列自己创建表。

**id 是内容寻址的。** 同一条记录经由任一路径都产生同一个 id，而变化后的记录产生新的 id，这正是重新运行成为无操作而不是产生重复的原因。

## 参考

- [pkg.go.dev](https://pkg.go.dev/github.com/AreteAcademy/brevis/sdk)：完整的 API
- [`examples/`](https://github.com/AreteAcademy/brevis/tree/master/examples)：十二个可运行的示例
- [`CHANGELOG.md`](https://github.com/AreteAcademy/brevis/blob/master/CHANGELOG.md)：逐版本的历史
