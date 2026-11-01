---
title: 摄取 sink
description: 六个目的地、各数据库如何写入一条记录，以及为什么镜像只包含你用到的部分。
group: 摄取
order: 19
slug: ingestion-sinks
---

六个目的地，而且这份清单刻意写得明确：对于正在选择的人来说，*计划中*的连接器和*能运行*的连接器是两回事。

| sink | 状态 | 一次写入是什么 | 验证对象 |
|---|---|---|---|
| `pubsub` | **可运行** | 每个批次一次发布，属性和排序键来自事件自身的字段 | 模拟器 |
| `postgres` | **可运行** | `COPY FROM STDIN`，或在一个事务中进行暂存 `INSERT` | 真实的 Postgres |
| `mysql` | **可运行** | 事务中的多行 `INSERT`，或 `INSERT IGNORE` | 真实的 MySQL |
| `files` | **可运行** | 目录、`gs://` 或 `s3://` 中的 NDJSON 对象 | 文件系统和 S3 |
| `bigquery` | **可运行** | 加载行，或暂存后按 `ingestion_id` 做 `MERGE` | **仅验证配置：不存在模拟器** |
| `redshift` | **可运行** | 先把批次写到 S3，然后 `COPY`，再 `MERGE` | **仅验证配置：不存在模拟器** |

**六个中有两个没有经过端到端验证**，表格如实说明了这一点，而不是让“可运行”这个词去承载一个没有人检查过的说法。BigQuery 和 Redshift 没有本地模拟器，所以测试的是配置、拒绝逻辑以及底层的驱动，而驱动已由 SDK 自己的测试套件覆盖。两者的首次生产使用才是真正的证明，应当选用一个低风险的 stream。

未知的 `type:` 会在**启动时被拒绝**，并指出*这个二进制文件*包含哪些。在自己没有的 sink 上启动的 gateway，会因为没有人看得见的原因而丢弃事件。

`gs://` 和 `s3://` 不是独立的连接器：`files` 能读取这三种路径形式，所以 dead letter 在笔记本电脑上是一个文件夹，在生产环境中是一个 bucket，而这里无需任何改动。

## 各自如何写入

在四个写入表的目的地上，`write` 是**必填的，且没有默认值**，因为这两种模式确实不同，只有表的所有者才知道该用哪一种：

| | `append` | `merge` |
|---|---|---|
| **postgres** | `COPY FROM STDIN` | `INSERT … ON CONFLICT (ingestion_id) DO NOTHING` |
| **mysql** | 每个块一次多行 `INSERT` | `INSERT IGNORE` |
| **bigquery** | 加载行（超过内联上限时通过 GCS） | `MERGE … WHEN NOT MATCHED THEN INSERT` |
| **redshift** | 从 S3 `COPY` | `COPY` 到暂存表，然后 `MERGE` |

`merge` 在四者中的含义**完全相同**：幂等插入，且**先到者生效**。重复投递会被忽略，而不是被应用：这对事件来说是正确的，因为事件只发生一次且不会改变；而对承载可变状态的行来说则是错误的。

Postgres 和 MySQL 的 `merge` **要求在 `ingestion_id` 上有 `UNIQUE` 索引**，没有时会拒绝，而不是悄悄追加：

```sql
CREATE UNIQUE INDEX CONCURRENTLY ON landing.orders (ingestion_id);
```

没有这个索引，`ON CONFLICT` 就无从匹配，每次重复投递都会造成重复，而表的所有者要求的恰恰相反。这种拒绝不会丢失批次：它会被重试，然后连同原因和 `CREATE INDEX` 语句一起埋入记录上的 dead letter，在有人创建索引期间，没有人会丢失事件。

**`upsert` 会按名称被拒绝。** `ON CONFLICT DO UPDATE`，即*最后*一次投递生效，是一种真实的模式，但**尚未实现**。接受这个词却表现得像 `merge`，恰恰是这个字段要防止的失败，所以配置会直接说明这一点。

## 每个 sink 各自独有的特点

```yaml
# Postgres: schema-qualified name, and the table has to exist.
sink: {type: postgres, dsn_from: BREVIS_DSN, table: landing.clicks, write: merge}

# MySQL: no COPY, so throughput is an order below Postgres on the same
# hardware. Flush wider here.
sink: {type: mysql, dsn_from: BREVIS_DSN, table: landing.clicks, write: merge}

# BigQuery: no dsn_from. It authenticates with the pod's own credentials, which
# is what a workload identity is for. The name has no dots — the project and
# the dataset are their own fields.
sink: {type: bigquery, project: acme-prod, dataset: landing, table: clicks, write: merge}

# Redshift: two hops, not one. It is columnar, a row-by-row INSERT pays the cost
# of a block, and the only workable load is COPY from S3. So every batch becomes
# an object in the staging prefix and then a COPY — a stream flushing every
# second writes 86,400 objects a day. Flush much wider here.
sink:
  type: redshift
  dsn_from: BREVIS_RS_DSN
  table: landing.clicks
  staging: s3://acme-staging/gateway/
  iam_role: arn:aws:iam::123456789012:role/redshift-copy
  write: merge
```

`dsn_from` 指定保存连接字符串的**环境变量**的名称，绝不是连接字符串本身：DSN 带有密码，而那个文件在 git 中。这与 workflow 中 `secrets:` 所做的区分相同。

`iam_role` 是一个角色，驱动**不接受密钥**：`COPY` 的 URL 中的密钥最终会出现在集群的查询日志里，而有很多人会读那份日志。

## 没有列声明

gateway 不发送列清单。pipeline 声明自己的列，是因为它的行具有它所掌控的单一形状；而 gateway 的批次是*N* 个客户端在一个 flush 窗口内 post 的任何东西，形状允许不同。

驱动在接触服务器**之前**，从**表本身**解析出列清单，并与批次所携带的内容取交集。表中没有的字段会被拒绝，并附上修复它的信息，而不是在 `COPY` 中途以 `column "x" of relation "y" does not exist` 失败；某个事件省略的字段则写为 `NULL`。

## 导入列表就是选择

sink 和 hook 一样是**编译进去**的，二进制文件会注册它所拥有的 sink：

```go
func main() {
    sinks := gateway.NewSinks()
    sinks.MustRegister(postgres.Sink, postgres.New)
    sinks.MustRegister(files.Sink, files.New)
    gateway.Main(nil, gateway.WithSinks(sinks))
}
```

Go 链接器会裁剪掉没有被引用的东西，所以从未导入 BigQuery 驱动的二进制文件，不会携带 BigQuery，也不会携带 Arrow 或 Storage Write API。`database/sql` 一直就是这样工作的。

它的价值正如听起来那样：

| 构建 | 包数量 | 二进制文件 |
|---|---|---|
| 全部六个 sink、两个对象存储、两个 metastore | 约 900 | 55.0 MB |
| `postgres` + 本地 `files` | 约 240 | **10.2 MB** |

对象存储是**第二个**注册表，因为 scheme 不是目的地：`files` 是一个 sink，它可以写入目录、`gs://` 和 `s3://`，而只写本地的构建不应该为此携带 AWS SDK。

### 两个已发布的镜像

| 镜像 | 包含 | 拉取大小（amd64） |
|---|---|---|
| `areteacademy/brevis-gateway:0.32.0` | 六个 sink、S3、GCS、Redis、memcached | 19.1 MB |
| `areteacademy/brevis-gateway:0.32.0-slim` | `postgres`、本地 `files`、`auto_table` | **5.2 MB** |

两者来自同一棵树的同一次构建，所以这两个标签始终是同一个 commit。没有 `latest-slim`，因为 `latest` 本来就是一个没有人应该部署的标签。

**想要不同的组合？** `cmd/gateway-slim` 只有十五行，import 块就是它的全部配置。复制它，修改 import，然后构建。想要 hook 的人本来就在编译自己的二进制文件，所以选择 sink 不会让他们多付出任何代价。

### 拒绝信息会说明你手里拿的是哪个构建

```
sink type "bigquery" is not one this binary carries (it has: files, postgres).
Sinks are compiled in, so this is a build that left it out rather than a
destination that does not exist.
```

固定的清单会让那个人去查找一个并非自己犯下的配置错误。对象存储也是如此：在没有 S3 后端的二进制文件中使用 `s3://` 的 dead letter，会在**启动时**被拒绝，并指出 scheme，而不是等到它第一次必须埋入批次时才被发现。

## 一条路由，N 张表，无需声明任何东西

```yaml
sink:
  type: auto_table
  shape: columns
  naming: {pattern: '^(app|svc)_[a-z0-9_]{2,40}$', allow: [app_, svc_], max_new_per_hour: 20}
  metastore: {type: redis, addr_from: BREVIS_METASTORE_ADDR, ttl: 60s}
  into: {type: bigquery, project: acme-prod, dataset: landing, write: merge}
```

`auto_table` **只路由，不写入**：写入的是 `into`，它不带 `table`，表来自每个事件。带有 `table` 的 `into` 会在启动时被拒绝，因为路由器之下的固定表会悄悄胜出，每个事件都会落到它里面。

### 信封

请求体是一个**信封**：控制字段在顶层，记录在 `data` 内部。

```json
{
  "table_name": "app_orders",
  "description": "App orders",
  "operation": "INSERT",
  "unique_key": "id",
  "data": {
    "id": "A-1",
    "total": 150,
    "customer": {"id": 7, "uf": "SP"},
    "items": [{"sku": "X", "qty": 2}]
  }
}
```

| 字段 | | 含义 |
|---|---|---|
| `table_name` | **必填** | 这条数据落到哪里 |
| `data` | **必填** | 记录，而且只有记录 |
| `unique_key` | 默认为 `id` | `data` 的哪个字段标识这条记录，**在 `merge` 下必填** |
| `operation` | 默认为 `INSERT` | `INSERT`、`UPDATE` 或 `DELETE` |
| `description` | 可选 | **接受但尚未使用**，见下文 |

`table_name` 与 `total` 和 `customer` 并排，是**传输**的字段假扮成了**记录**的字段。把它们分开，是每一种 CDC 格式的构造方式，也使得保留一个前缀成为可能，因为现在有了一个只有生产者才会写入的地方。

### 键属于 `merge`，而不属于信封

`unique_key` 是可选的，是否**必填**取决于写入模式，因为这两种模式要的东西不同：

| | |
|---|---|
| `write: merge` | **必填。** 这种模式的存在，就是为了每条记录只保留一行，而 `brevis_record_key` 就是“每条记录”的含义：解析当前版本的 `qualify` 就是按它分区的。装满了不指明任何记录的行的 merge 表，是没有人能够解析的表。 |
| `write: append` | **可选。** append 表是日志，而日志条目不一定要*关于*某条记录：一行审计、一个 webhook、一个指标样本。要求这里必须有 `id`，会迫使生产者编造一个，这比 `NULL` 更糟，因为它看起来是真的。 |

在 `append` 下，该列创建为可空，没有键的行带有 `NULL`，而不是 `""`，那是另一个事实。而且 `brevis_ingestion_id` 仍然存在：没有键时，**内容就是键**，所以同一份文档发送两次，就是同一个 id。

**指定一个不在 `data` 中的字段，在两种模式下都是错误。** “你没有说”和“你说了某个不存在的东西”是不同的错误，而只有第一种能通过：

```json
{"accepted":0,"rejected":["event 0: \"unique_key\" names \"pedido_id\" as this
 record's identity and \"data\".\"pedido_id\" is missing or empty. Drop
 \"unique_key\" to fall back to \"id\", or send the field"]}
```

**`description` 会被读取，但目前不会去往任何地方。** 它是为 Console 的摄取页面而放进契约的，而那个页面尚未编写。在这里说明这一点，是因为被悄悄接受的字段，会让人相信它正在被存储。

### 八个固定列，外加记录携带的任何内容

| 列 | | |
|---|---|---|
| `brevis_ingestion_id` | `STRING` | 标识，也是 merge 键 |
| `brevis_record_key` | `STRING` | `data[unique_key]`：这条数据关于哪条**记录**；在 `append` 下为 `NULL` |
| `brevis_operation` | `STRING` | `INSERT`、`UPDATE` 或 `DELETE` |
| `brevis_received_at` | `TIMESTAMP` | 到达时间，按**我们的**时钟：分区列 |
| `brevis_loaded_at` | `TIMESTAMP` | 写入时间，由**目的地**打戳 |
| `brevis_stream` | `STRING` | 由哪条路由写入 |
| `brevis_gateway` | `STRING` | 哪个部署 |
| `brevis_received_bytes` | `INT64` | 事件**到达**时有多大，包含信封 |

这八列属于 **SDK**，而不属于这个 sink：`sdk.LandingSchema` 声明它们，`sdk.LandingID` 生成标识，所以 pipeline 可以用相同的 id 落地同一张表。只有一份定义，因为两份会产生偏差。见 [SDK 的落地布局](/docs/sdk/#落地布局)。

**`brevis_` 是保留的。** 带有任何该前缀的键的 `data` 会**按事件**被拒绝，否则生产者就能伪造控制字段，而伪造的 `brevis_received_at` 比没有更糟，因为它看起来是真的。

**`brevis_loaded_at` 是数据库的 `DEFAULT`**，不是 gateway 发送的值。gateway 知道的是**分发**时间，目的地知道的是**写入**时间，两列之间的差距就是每一行真实的端到端延迟，无需任何埋点。

**`brevis_received_at` 是分区列，绝不是客户端的时钟**：生产者的时钟可能是错的或缺失的，而由客户端控制的分区列，意味着客户端可以写入 2035 年。未分区的落地表，是只增不减的查询账单。聚簇按 `brevis_record_key`，因为读取某一条记录的历史，正是人们对这类表的常规用法。

一份声明服务于所有目的地，因为 SDK 的 DDL 生成器会转换每种类型：`STRING` 在 Postgres 上变成 `TEXT`，在 MySQL 上变成 `LONGTEXT`，在 BigQuery 上是 `STRING`；`JSON` 依次变成 `JSONB`、`JSON` 和 `JSON`。

### 无需度量基础设施即可掌握数据量

`brevis_received_bytes` 是**免费的**：gateway 在解码时本来就会统计这些字节，用来驱动 `buffer.flush.size`，而在此之前这个数字就在那里消失了。单独度量记录，每个事件需要一次 `json.Marshal`，即 2.0 µs，而解析本身花费 3.1 µs，**热路径上多出 67%**，只是为了精化一个其作用在于趋势和归因的数字。

它回答的是指标**在结构上无法回答**的问题。每个 `brevis_gateway_*` 序列都是按 *stream* 的，并且刻意不带 `table` 标签：使用 `auto_table` 时，一条路由会变成 N 张表，而这个标签由生产者来选择。所以“哪张表在增长、从什么时候开始”，在任何地方都没有答案。

```sql
select date(brevis_received_at) day,
       count(*) events,
       sum(brevis_received_bytes)/1e9 gb
from app_orders
group by 1 order by 1 desc
```

没有这个列，同一个问题意味着扫描整个 JSON 列，即 `sum(length(to_json_string(data)))`，扫描的字节数大约是**五十倍**，每次有人问都是如此。这一列在第一次查询时就回本了。

**它是入口量，不是存储量。** 目的地保存的是带类型且压缩过的行：400 字节的 JSON 在磁盘上可能只有 80 字节。对它求和得到的是*到达*了多少，绝不是为保存它而计费的量。

并且**生产者无法写入它**。gateway 在 hook 之后打上这个值，并覆盖信封中携带的任何内容：发送了该字段的客户端，报告的是它自己的数据量，对它求和就失去了意义。

### 数据量序列是选择性开启的

```bash
BREVIS_INGESTION_METRICS=true
```

```
brevis_gateway_ingested_bytes_total{stream,table}
brevis_gateway_ingested_events_total{stream,table}
```

**默认关闭**，而默认值就是重点：`table` 是由*生产者*选择的标签。这个文件中的其他一切，都以运维人员在 YAML 中写下的内容作为标签，所以序列数量在进程启动之前就已知。没有人应该因为升级而发现指标账单。

无法解析的值读作**关闭**，绝不是失败：这是可观测性，这里的一个拼写错误，不应成为摄取端点无法启动的原因。

无论哪种情况，这个列都在。指标买到的是*现在*；这个列买到的是*从什么时候开始*以及*由谁*。

### 两种形态，一份契约

`shape` 决定**记录**为行贡献什么。无论哪种，生产者的契约都是一样的：信封从不改变。改变的是表的样子，而那是运维人员的决定，不是生产者的决定。

```yaml
shape: document   # the default
shape: columns
```

使用 **`document`** 时，`data` 整体放进一个 `JSON` 列：

```
 brevis_ingestion_id | brevis_record_key | … | data
 d7179bfe-…          | A-1               |   | {"id": "A-1", "total": 150, "customer": {…}}
```

创建之后永远不会有任何 DDL。新字段就是新的键：没有 schema 变更配额，没有重新打开写入流，副本之间没有竞争，而且它在到达的当天就可以查询。这是市场最终的落点：Fivetran、Airbyte 和 Snowpipe 都落地到一个不会失败的原始层，然后在下游建模。

使用 **`columns`** 时，记录的每个字段都有自己的一列：

```
 id   | total | customer              | items
 A-1  | 150   | {"id": 7, "uf": "SP"} | [{"qty": 2, "sku": "X"}]
```

**标量变成 `STRING`，对象和数组变成 `JSON`，任何地方都没有推断。** `150` 以数字到达，变成了字符串 `150`。这是一种选择，而不是局限：今天以整数到达、明天以小数到达的字段，会在没有人写任何东西的情况下改变列的类型，而不再适配的那一行会进入 dead letter。为其指定类型是**提升**的途径，写在 YAML 中，并经过 diff 评审。

**`null` 完全不会产生列。** 它没有形状，所以无从读取：该字段会从第一个携带*值*的事件开始出现在表中，其形状就是该值的形状。发送 `null` 的事件同样落地为 `NULL`，这正是它的含义；而在列已存在的地方（因为你声明了它，或者另一个事件创建了它），`null` 会被写入其中，而不是被遗漏。

**并且 `JSON` 列接收的是 JSON 值，绝不是它的字符串。** 听起来像细节，却是可查询的表与看起来没问题的表之间的区别：

```sql
SELECT JSON_TYPE(customer) FROM bronze.orders;     -- object
SELECT JSON_VALUE(customer, '$.uf') ...;           -- SP
```

如果 `JSON_TYPE` 回答 `string` 而 `JSON_VALUE` 回答 NULL，说明对象被编码了两次：列里保存的是对象的文本，而不是对象。**0.15.0** 及之前的 gateway 版本在 BigQuery 上，在**两种**形态下都是这样；一行命令的检查与修复方法见 [gateway 变更日志](https://github.com/AreteAcademy/brevis/blob/master/gateway/CHANGELOG.md)。Postgres 和 MySQL 从未受到影响：它们的服务器会解析文本。

字段名必须符合 BigQuery 的规则，这是四者中最严格的。Postgres 几乎接受任何带引号的名称，而这正是陷阱：表在那里创建出来了，却在有人把 stream 指向 BigQuery 的那天出问题。

两种形态都有对应的 SDK 等价物，而且是同一段代码，而不是一段相匹配的代码：`sdk.Landing(table, sdk.LandingKey("id"))` 落地 `document`，加上 [`sdk.LandingColumns()`](/docs/sdk/#两种形态整条记录或每个字段一列) 则落地 `columns`。相同的列，相同的值，相同的 `brevis_ingestion_id`，因此 stream 可以在 gateway 与 pipeline 之间切换而无需迁移，两者也可以当作一张表来读取。

pipeline 也可以用同样的方式增加同样的列：在目的地上设置 [`Evolve: sdk.EvolveAdditiveFromPayload`](/docs/sdk/#当出现表中没有的字段时)，批次携带的字段就会按这条规则、通过这段代码变成一列。


**前缀是可配置的，而且是整个进程的一项决定。** `BREVIS_LANDING_PREFIX=acme` 会让它们变成 `acme_ingestion_id` 等等，适用于这个 gateway 中的每个 stream，所以两个 sink 不会对它们都可能写入的表产生分歧。上面的保留规则随之移动：在 `acme_` 下，发送 `acme_region` 的生产者会被拒绝，而 `brevis_region` 是普通字段。

**请在创建第一张表之前选定它。** 之后再修改会新增八列并遗弃八列，因为没有任何东西会删除列，所以已经带有某个前缀布局的表，会拒绝声明了另一个前缀的加载。规范化的方式以及出路见 [SDK 页面](/docs/sdk/#brevis-前缀可以由你修改)。

**嵌套对象的字段可以拥有自己的列。** 在 gateway 的进程上设置 `BREVIS_NORMALIZE_DATA=true` 会展开一层，`{"name": {"first": "x"}}` 落地为 `name_first`，并且每个 stream 共享它，所以两个 sink 不会对它们都可能写入的表产生分歧。它会重命名什么，请见 [SDK 页面](/docs/sdk/#每个嵌套字段一列)，这是开启之前需要阅读的那一半。

**在 BigQuery 上，`nationalID` 和 `nationalId` 是同一列。** BigQuery 会折叠列名的大小写，所以一次 flush 中，一个 `DELETE` 带有一种写法而一个 `INSERT` 带有另一种写法，会创建**一个**列（排序后的第一个），而不是让 `CREATE` 失败。单个事件同时携带两者会被拒绝，并指出两个字段。Postgres 和 MySQL 把它们保留为两列，这对它们来说是正确的。哪种写法胜出以及哪些内容不会迁移，见 [SDK 页面](/docs/sdk/)。

### `UPDATE` 和 `DELETE` 是被记录的，而不是被应用的

`brevis_operation` 是一列。落地表是**历史**：原地应用的 `UPDATE` 会丢失之前的版本，而你需要那个版本的那天，就是出问题的那天。解析“每条记录的当前版本”是下游模型的工作：

```sql
qualify row_number() over (partition by brevis_record_key
                           order by brevis_received_at desc) = 1
   and brevis_operation <> 'DELETE'
```

Debezium、Fivetran 和 Airbyte 就是这样做的，这意味着 CDC 在写入路径上**没有任何**成本：没有 upsert 模式，没有锁，没有删除。

### 标识里没有时钟

```
brevis_ingestion_id = uuid5(auto_table | table | data[unique_key] | sha256(canonical(data)))
```

公式里没有 `occurred_at`，这是有意的：到达时间成了**我们**的责任，而我们的时钟是 `time.Now()`，每次投递都不同。标识里有了它，每次重复投递都会成为新事件，`merge` 也就什么都吸收不了了。

```
same record, same content, twice  →  same id, merge absorbs
same record, content changed      →  a different id, both versions land
```

注意事项已有说明：`data` 逐字节完全相同的两个真正不同的事件会合并为一个。对 CDC 来说这是**正确的**：对同一条记录用相同的值做两次相同的更新，是同一个事实。

指纹是对**规范**形式的文档计算的：每一层的键都排序，数组保持顺序，所有内容都带引号。Go 会随机化 map 的遍历顺序，所以朴素的哈希会在同一事件的两次投递之间产生不同的结果，而这正是它要防止的事。

### 表会自行增加列

`data` 携带表中没有的字段时，会让该列出现。**只增不减**：不删除任何东西，不收窄任何东西。

在 BigQuery 上，这在 **0.13.0** 才成为现实。在此之前，`auto_table` 在每个目的地上都声明 `Evolve: additive`，而只有 Postgres 和 MySQL 驱动会读取这个声明，所以在那里形状在创建时就冻结了，而没有任何东西说明这一点。

> **从 0.11.0 或 0.12.0 升级时**，在 `0.11.0` 之前创建的表需要一次性迁移，因为第八个固定列进入了声明，而没有进入表：
>
> ```sql
> ALTER TABLE `<dataset>.<table>` ADD COLUMN IF NOT EXISTS brevis_received_bytes INT64
> ```
>
> Postgres 和 MySQL 从未受到影响。

在与 `ALTER` 的竞争中落败的批次会失败，而管道的常规重试会解决它：等待 DDL 的批次与等待 worker 的批次是同一回事，所以没有第二个缓冲区。跨副本时，metastore 为每张表、每种形态保持一个一秒钟的*去抖*，这样十个副本看到同一个新字段，就不会变成对 BigQuery 每张表每十秒五次元数据操作配额的十次 `ALTER`。

**是去抖，而不是锁**：没有释放任何东西，也没有续租。落败者是被*重试*，而不是被阻塞，持有去抖的副本死掉，只会让其他副本多等一秒。这是业界的做法：Delta Lake 和 Iceberg 乐观提交并重试，Kafka Connect 从分区顺序中获得串行化，Fivetran 保持每张表只有一个写入者。

### 名称就是攻击面

`auto_table` 把载荷中的字符串变成 DDL，所以 `naming` 下的每个字段都是一项拒绝规则：

- **`pattern`** 默认为 `^[a-z][a-z0-9_]{2,48}$`，即 Postgres、MySQL、BigQuery 和 Redshift 不加引号都接受的名称的交集。通过的名称无需在任何地方加引号，也不可能携带注入。
- **`allow`** 按前缀进一步收窄。
- **`max_new_per_hour`**（默认 20）限制滚动一小时内的**创建**数量，绝不限制写入：已存在的表绝不会被限速。在 `metastore: memory` 下，额度保存在进程中，四个副本会放行四倍；在 `redis` 或 `memcached` 下，它们共享一个计数器。

规则之外的名称会**按事件**被拒绝，并在响应中说明，而同一请求中每个格式正确的事件仍然会落地：

```json
{"accepted": 0, "rejected": ["event 0: the table name \"orders\" does not match ^(app|svc)_[a-z0-9_]{2,40}$"]}
```

是按事件而不是按批次，这是有意的。如果在写入时才拒绝，整个批次都会失败：一个生产者的一个格式错误的事件，会把同一个 flush 窗口中所有其他生产者的事件一并埋掉，而它们都没有收到通知。

**在没有 `listen.auth` 的端点上，`auto_table` 会被拒绝**，包括在 `BREVIS_ENV=local` 下。能够指定表名的生产者就能创建表，所以即使在普通 stream 不需要认证的地方，它也需要认证。NetworkPolicy 无法替代它：那只限制谁能访问端口，而不限制他们请求哪个表名。

### metastore 是缓存

它的存在是为了让每个事件的写入不必对应每个事件的查询，它保存三样东西：哪些表存在、DDL 的*去抖*，以及 `max_new_per_hour` 背后的计数器。

它是缓存，**不是**事实来源。表是否存在由目的地裁决，而 *N* 个副本竞相创建同一张表是常态：`AlreadyExists` 就是成功，这只是减少了竞争。TTL 是它可能**出错**的时长。

| 后端 | 何时使用 |
|---|---|
| `memory` | 默认值，不需要任何东西。只有**一个**副本时，这是正确的答案 |
| `redis` | 需要 `addr_from`。`SETNX` 是 claim，`INCR` 是计数器 |
| `memcached` | 需要 `addr_from`。`Add` 是 claim，过期以**秒**为粒度 |

**对于带路由键的 stream，Redis 更快**，这一差异值得用数字而不是形容词来说明。触发按表进行时，一个窗口会为每张表请求一次 claim：Redis 用一次流水线往返回答其中的 94 个，而其他所有后端并行回答它们，上限为 32。针对一个相距 25 毫秒的后端实测，94 个 claim：

| 后端 | 逐个进行 | 按实际发布的方式 |
|---|---:|---:|
| `redis` | 2.589 s | **29.8 ms** |
| `memcached` | 2.577 s | **89.4 ms** |

三波对应一次往返。两者都在 claim 所允许的 500 毫秒之内，所以 memcached 是较慢的选择，而不是被拒绝的选择。

有多个副本时，`memory` 让每个副本各有一份：去抖什么都抑制不了，`max_new_per_hour` 限制的是一个进程。这是另外两个后端存在的唯一原因。

**宕机的 metastore 不能把写入一并拖垮。** 出错的 `Get` 视为未命中，出错的 claim 视为已获胜，出错的计数器则放宽限制。能够让摄取停止的缓存，比没有缓存更糟。

**而且“宕机”包括“缓慢”。** claim 是在持有缓冲区锁时运行的，所以一个需要两秒钟才回应的后端，意味着该 stream 有两秒钟什么都不接受。每次调用都以 **500 毫秒**为上限，超过后以失败放行。Redis 把它当作调用方的 context 来遵守；`gomemcache` 完全不接受 context，所以 memcached 把它当作套接字截止时间来遵守，这是以两种方式达到的同一个上限，因为这两个客户端中有一个无法被告知停止。

**`ttl: 0` 表示永不过期**，什么都不写则仍使用一分钟的默认值，有三种状态，而不是两种。

时钟**不是**从错误条目中恢复的方式。目的地拒绝的写入会调用 `invalidate`，它会立即丢弃该条目，让管道自己的重试在冷缓存上再来一遍：被手动删除的表只会造成**一次失败的尝试**，而不是一分钟。

过期是有代价的。当 `sinkFor` 不*知道*某张表时，会计入 `naming.max_new_per_hour`，所以一次未命中会为一张已经存在数周的表计费，而在一张表最后一个事件之后一分钟重启的 pod，从它所付费的共享后端那里什么都得不到。对于长期存在的表，答案是 `ttl: 0`。

> 在 `0.12.0` 和 `0.13.1` 上，这种写法**无法解析**：YAML 把单独的零读作 int，`0.13.2` 是第一个两种写法都接受的版本。在这些版本上，请写 `ttl: 0s`。

`addr_from` 指定保存地址的**环境变量**的名称，绝不是地址本身：它带有密码的情况相当常见。

### BigQuery 有 flush 下限

**BigQuery 允许每张表每天 1,500 次加载作业。** 默认的一秒窗口是 86,400 次，是配额的 57 倍，大约二十五分钟就用完了。这对 Pub/Sub 不是问题，而这个数字会从 Pub/Sub 的配置*带到*这里。

所以，写入 BigQuery 的 stream（无论直接写入还是通过路由器），窗口小于 60 秒时会在**加载时被拒绝**。真正的答案是 Storage Write API，但它尚未编写；在此之前，配置会拒绝它无法兑现的窗口，而不是让第一次部署去发现问题。

`auto_table` 无法路由到 **Redshift**：该驱动不创建表，所以每个新名称都会在加载时失败。会在启动时按名称被拒绝。

## 尚不存在的内容

`sqlite`、`dynamodb`、`kinesis`、`sqs`、`sns`、`kafka` 和 `rabbitmq` 都在计划中，尚未编写任何代码。（`redis` 和 `memcached` 作为 **metastore** 存在，那是另一回事：关于表的已知信息的缓存，绝不是目的地。）本页顶部的表格说明了每一个的状态，并且在状态变化时更新，不会提前更新。
