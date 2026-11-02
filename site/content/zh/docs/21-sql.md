---
title: SQL
description: 普通的 .sql 文件按依赖顺序变成表和视图，既可作为 workflow 的 step，也可独立使用。
group: 用 SQL 建模
order: 21
slug: sql
---

没有模板：模型就是一个始终保持为合法 SQL 的文件，顺序来自对 SQL 本身的解析。Postgres 和 BigQuery，通过同一套一致性测试。

![SQL 的流程：brevis-sql 读取 raw.orders，这是由 SDK 或 Gateway 落地的表。模型 staging.stg_orders 成为它之上的视图，marts.orders 则是该视图之上的表；顺序来自对 SQL 的解析。同样的模型可以在 Postgres 或 BigQuery 上构建。](/assets/flow-sql.svg)

## 六条命令

| 命令 | 作用 |
|---|---|
| `brevis-sql compile` | 解析每个模型并解析每条边，**不连接任何东西** |
| `brevis-sql graph` | 打印推断出的边，这样错误的边能被看到，而不是被事后发现 |
| `brevis-sql build` | 按依赖顺序创建或替换每个模型 |
| `brevis-sql test` | 运行每个模型的测试；每个测试都是一条必须找不到任何行的 SELECT |
| `brevis-sql serve` | 通过 HTTP 回答只读的预览和查询，这样 Console 就不需要数据仓库的凭据 |
| `brevis-sql generation bump` | 记录某张表已被重建，使 `brevis-sql-api` 针对它缓存的答案过期，`build --generations-from` 会为它构建的每个模型这样做 |

`--select orders+` 会把其中任何一条命令缩小到一个模型及其所有下游。


## `brevis-sql serve`：无需凭据即可读取数据仓库

Console 的数据和 SQL 界面显示落地了什么，以及其中有什么。两者都不直接访问数据仓库：它们询问这个与之并排运行的服务，由它持有引擎刻意不持有的凭据。

```bash
brevis-sql serve --connections brevis.yaml --addr 127.0.0.1:8088
```

上限不是预算：`--max-bytes` 限制一次查询，而 `--budget` 限制每个连接每小时的总和，并按报价检查，所以拒绝发生在花钱之前。它只限制会报告字节数的数据仓库，即 BigQuery 而不是 Postgres；启动横幅会说明当前生效的是哪种姿态，包括没有任何限制。

**只有文件中声明的内容才可访问**，并且默认情况下它所做的任何事都不是写入：每条语句在运行前都会被分类，会扫描超过 `--max-bytes` 的读取会在产生任何成本之前被报价并拒绝，数据仓库本身也会被要求拒绝写入。审计行带有语句的哈希，绝不带有语句本身。

**它不再需要自己的 service account。** `brevis-sql build` 会创建表，所以它的角色可以写入，而拒绝任何此类角色的服务，意味着要多创建、保管和轮换一个账号。角色不再是守住防线的唯一手段：Postgres 在以 `READ ONLY` 打开的事务中运行每条语句，`SELECT nextval('s')` 这种任何分类器都看不到内部的写入，会被 Postgres 自己拒绝；BigQuery 则在为其报价的 dry run 中报告语句的类型，所以 `CREATE TABLE` 或 `EXPORT DATA` 会依据数据仓库自己的判定被拒绝。两者都无法提供的数据仓库，仍然会拒绝可以写入的凭据。

在 `BREVIS_ENV=local` 之外需要 token，没有它服务不会启动。

### 有人可能会修改的开发数据仓库

```bash
BREVIS_SQL_SERVE_WRITES=warehouse_dev
```

它**指定连接的名称**。一个服务可以访问多个数据仓库，解锁你正在开发的那个，不能解锁它旁边的那个。在被指定名称的连接上，所有层都会让开，这个服务的能力恰好等于它的凭据：行数上限限制的是一个界面，字节上限限制的是金钱，而两者都不限制一条语句会改变什么。

无论在哪里设置它都需要 token，`BREVIS_ENV=local` 也不例外。启动横幅会列出每个已解锁的连接，Console 会在编辑器上方显示一条横幅，指标中还会出现 `brevis_sql_writable_connection`。**在生产环境中请不要设置它**；这个仓库中没有任何 manifest 设置它。

token 可以**命名**：`BREVIS_SQL_SERVE_TOKEN_CONSOLE=…` 声明了一个名为 `console` 的调用方，该持有者产生的每条审计行都带有 `"caller":"console"`。身份就是凭据本身，而不是调用方自己填写的 header，所以日志不可能说出该 token 持有者没有持有的东西。被拒绝的 token 也会留下一行，而且绝不会记录出示的内容。

## 模型是一个始终保持为合法 SQL 的文件

配置是文件开头的块注释，所以这个文件可以在编辑器中带高亮地打开，可以在控制台中手动运行，也可以用 `psql -f` 执行。

```sql
/* brevis
materialized: table
tests:
  - not_null: [order_id]
  - unique: [order_id]
  - relationships: {column: customer_id, to: staging.stg_customers, field: customer_id}
*/
select * from staging.stg_orders
```

**没有模板。** `models/<schema>/<name>.sql` 就是 `<schema>.<name>`，依赖来自对 SQL 的解析，而不是你必须编写的函数调用。`brevis-sql graph` 会打印推断出的内容；头部的 `depends_on:` 是针对罕见遗漏的显式覆盖。

## 从 compile 到 test

两个模型：一个清洗 `raw.orders` 的视图，和一个依赖它的表。

`models/staging/stg_orders.sql`：

```sql
/* brevis
materialized: view
*/
select order_id, customer_id, amount, ordered_at
from raw.orders
where amount is not null
```

`models/marts/orders.sql`：

```sql
/* brevis
materialized: table
tests:
  - not_null: [customer_id]
  - unique: [customer_id]
*/
select customer_id, count(*) as orders, sum(amount) as revenue
from staging.stg_orders
group by customer_id
```

`compile` 和 `graph` 不连接任何东西：

```bash
docker run --rm -v ./project:/project:ro brevissh/brevis-sql:0.6.0 \
  compile --project /project
```

```text
2 models, 2 to build, 0 function(s)
  staging.stg_orders                 view  0 test(s)
  marts.orders                       table 2 test(s)
```

```bash
docker run --rm -v ./project:/project:ro brevissh/brevis-sql:0.6.0 \
  graph --project /project
```

```text
staging.stg_orders
  └─ raw.orders (source)
marts.orders
  ├─ staging.stg_orders
```

针对 Postgres 的 `build` 和 `test`：

```bash
docker run --rm -v ./project:/project:ro \
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db brevissh/brevis-sql:0.6.0 \
  build --project /project --dsn-from BREVIS_SQL_DSN
```

```text
  staging.stg_orders                 view  new   4ms
@brevis:{"type":"landed","target":"postgres://postgres/staging/stg_orders"}
  marts.orders                       table new   4ms
@brevis:{"type":"landed","target":"postgres://postgres/marts/orders","rows":2}
2 models built on postgres
```

```bash
docker run --rm -v ./project:/project:ro \
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db brevissh/brevis-sql:0.6.0 \
  test --project /project --dsn-from BREVIS_SQL_DSN
```

```text
2 tests passed on postgres
```

这是来自已发布镜像的真实输出：compile 和 graph 不连接任何东西。针对 Postgres 时，build 和 test 分别以“2 models built”和“2 tests passed”结束。

### 当测试失败时

如果 `raw.orders` 中有一个订单没有客户，`build` 会通过，而 `not_null` 测试不会。失败信息会指出模型、列、行数以及**查询语句**，你可以把它粘贴到控制台中不断缩小范围：

```text
FAIL  marts.orders                   not_null         customer_id          1 row(s)
      SELECT COUNT(*) FROM (SELECT customer_id FROM marts.orders WHERE customer_id IS NULL) AS v
brevis-sql: 1 of 2 tests failed
```

进程以退出码 1 结束，workflow 的 step 也随之失败。

## 增量模型及其 lookback

`materialized: incremental` 需要 `unique_key` 和 `watermark`。每次 build 只读取
大于目标表中已有最大 watermark 的行，并按键用 MERGE 合并。这个过滤条件是严格的，
因此与 watermark 相等但在 build 之后才提交的行，或者带着更早时间戳迟到的行，
在执行 `--full-refresh` 之前都不会被看到。可选的 `lookback` 可以解决这个问题：

```sql
/* brevis
materialized: incremental
unique_key: [order_id]
watermark: updated_at
lookback: 2h
*/
```

取值是一个整数加上 `s`、`m`、`h` 或 `d`；其他写法都会被拒绝，并附上模型名称。
过滤条件变为 `updated_at >= max - 2h`，重复的行由 MERGE 吸收。设置 lookback 时，
watermark 必须是 TIMESTAMP 列。不设置 `lookback` 时，SQL 保持不变。

## 测试是返回违规行的 SELECT

`not_null`、`unique`、`accepted_values`、`relationships`。零行就是通过，没有其他需要解读的东西。

## 它会说明自己写了什么

每个物化的模型都会打印 Brevis 引擎本来就会读取的 `@brevis:` 行，所以它会与其他所有内容一起出现在 `/data` 中（见上面 `build` 的输出）。视图报告的是没有行数，而不是零：它不保存任何行，而那里的零与一张真正为空的表无法区分。

## 两个数据仓库，一套测试

PostgreSQL 和 BigQuery（`--dialect bigquery`），两者运行同一套测试，这套测试是在只有一种实现时编写的，所以第二种实现不会悄悄偏离。

BigQuery 使用 Application Default Credentials 进行认证：在 workload identity 上，那就是 pod 自己的身份，而 `--dsn-from` 指定保存项目 id 的变量。

## 13 MB

Distroless，nonroot，没有 shell。官方的 BigQuery 客户端在一个空二进制文件中就有 31 MB 和 526 个包，所以这里改为通过 REST 端点与 BigQuery 通信，大约 200 行，并为每一种可能悄悄出错的情形各写了一个测试。仓库中有一道关卡会按名称拒绝那个客户端，所以这个决定能经受常规依赖升级的考验。

## 已对照 dbt 验证

jaffle-shop，由这个镜像在 dbt 所用的同一份种子数据上构建：

|  | 结果 |
|---|---|
| 每个模型的行数和每行的 md5 | **完全一致，13 个中的 13 个** |
| 每个模型的列和类型 | **完全一致，13 个中的 13 个** |
| 13 个模型，首次构建 | 1.56 秒 |

## 它不做什么

**不兼容 dbt**，这是一项决定，而不是缺陷。真实 dbt 项目中大约三分之一的模型只用到 `ref`、`source` 和 `config`；其余的用到宏，而包本质上就是模板。没有人能预测的半吊子兼容，比没有兼容更糟。

## 接下来去哪里

| 如果你想 | 前往 |
|---|---|
| 看 SQL 在四个组成部分中的位置 | [生态](/docs/ecosystem/) |
| 把 brevis-sql 作为定时 step 运行 | [Core](/docs/core/) |
| 落地它将要建模的数据 | [Go SDK](/docs/sdk/) 或[摄取](/docs/ingestion/) |
