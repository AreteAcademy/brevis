# 参数

> 同一个 workflow 的两次 run 之间可以变化的内容，无需编辑文件。

*https://brevis.sh/zh/docs/parameters/ · brevis.sh docs (zh-CN)*

---

workflow 可以声明 **run 参数**：在两次触发之间变化、而文件本身不变的值。

## 声明

```yaml
params:
  - name: load_full
    type: boolean
    default: "false"

  - name: start_date
    type: string
    pattern: '^\d{4}-\d{2}-\d{2}$'

steps:
  - id: run
    run: dbt build --vars '{"load_full":"{{ .load_full }}"}' --select bronze+
```

| 字段 | | |
|---|---|---|
| `name` | **必填** | 在模板和 `--param` 中使用的名称 |
| `type` | | `string`、`boolean`、`integer`，或用于多个值的 `list|<type>` |
| `default` | | 触发时未提供值时使用 |
| `pattern` | | 值必须匹配的正则表达式 |
| `title` | | 表单中显示的标签，最多 60 个字符；省略时使用名称 |
| `group` | | 在表单中把参数归为一组，最多 60 个字符 |
| `warn` | | 取默认值以外的值会带来什么代价，显示在字段下方，最多 200 个字符 |
| `uses` | | 该参数所涉及的连接器的键（`bigquery`、`postgres`），用于显示其 logo |

**没有 `default`** 的参数是必填的：省略它的触发会在执行任何 step 之前失败。

`title`、`group`、`warn` 和 `uses` 决定表单如何呈现该参数，**其中任何一个都不会改变值的取值范围**。每个都只能是一行；`uses` 只能是小写字母、数字和 `-`。其余内容会被 publish 拒绝，并指出是哪个参数。

```yaml
params:
  - name: load_full
    type: boolean
    default: "false"
    title: Full load
    group: Load
    warn: reprocesses the whole table; it costs
    uses: bigquery
```

## 值列表

`list|<type>` 声明一个携带多个值的参数，比如要加载的表、要重建的层、要 backfill 的窗口：

```yaml
params:
  - name: tables
    type: list|string
    default: "users,orders"

  - name: layers
    type: list|string
    enum: [bronze, silver, gold]     # an enum restricts each ITEM
    default: "bronze"

  - name: days
    type: list|integer
    default: "1,7,30"
```

元素类型可以是 `string`、`integer` 或 `boolean`。每一项单独校验，因此给 `list|integer` 传入 `1,x,30` 时，失败信息会指出**第 2 项**，而不是整个值。

它在所有地方都是**一个以逗号分隔的字符串**：在数据库中、在 `BREVIS_RUN_PARAMS` 中、在命令中。正因如此，它在 step 中无需任何特殊处理就能工作：

```yaml
run: dbt build --select {{ .tables }}      # dbt build --select users,orders
```

这也是逗号不能出现在某一项内部的原因。空项和重复项同样会被拒绝：重复项会悄悄地让 step 对每一项所做的事翻倍。

### 在 step 中作为数组

```go
for _, table := range p.Run.ParamList("tables") {
    load(table)
}
```

```python
from brevis import run

for table in run.param_list("tables"):
    load(table)
```

对于没人填写的参数，两者都返回 `[]`，而不是包含一个空字符串的列表：遍历“无事可做”应该什么都不做。各项以字符串形式返回，`list|integer` 也不例外，因为 step 比库更清楚该如何处理数字。

### 在界面中

带 `enum` 的列表渲染为复选框，不需要 JavaScript，浏览器会为每个勾选的框提交一个值。没有 `enum` 时，它是一个文本字段，各项变成可移除的标签；关闭 JavaScript 时，它仍是一个内容为 `users,orders` 的字段，这正是服务器所期望的。

### 在命令行中

```bash
brevis run wf.yaml --param tables=events,sessions --param days=1,30
```

:::note 旧版引擎无法读取它
该类型与 workflow 一起保存，而在这个功能出现之前的引擎，会在 publish 时把 `list|string` 当作未知类型而拒绝。使用它的 workflow 发布之前，请先升级引擎。
:::

## 在命令中使用

值通过模板进入 `run`，写法是双大括号加一个点：

```yaml
run: ./load.sh --since {{ .start_date }}
```

## 在命令行中传递

```bash
brevis run wf.yaml --param load_full=true
brevis run wf.yaml --param load_full=true --param start_date=2026-01-01
```

`--param` 可以重复。不带 `=` 的输入是**错误**：

```
erro: --param "load_full": use chave=valor
```

这是有意为之。`--param load_full` 忘了写值，就会以默认值运行，而操作者还以为 backfill 用的是他们想要的参数。

## 在 backfill 中

这些值适用于范围内的**每一个** slot。这是核心用例：“用 `load_full=true` 重新处理整个一月份”。

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

## 在界面中

带有 `params` 的 workflow 会得到一个**表单**，取代简单的触发按钮。字段来自声明，`pattern` 在提交前校验。

## 在 step 的环境中

run 参数也会带着前缀进入每个 step 的**环境**。这样，用 [SDK](/zh/docs/sdk/index.md) 编写的抓取器无需以参数形式接收任何东西，就能看到它们：

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    if p.Run.Params["load_full"] == "true" {
        p.Source.URL += "&full=1"
    }
    return nil
},
```

## 自动参数

每个 run 还带有**自动参数**：引擎自己算出的值。没有人声明它们，每个 run 都有，并且显示在 run 的页面上。

其中最重要的是 `adjusted_at`，值得说明它消除的那个 bug。抓取器读取 `now()`，减去自己的窗口，向供应商请求最近两个小时的数据。对于按时启动的 run，这是对的。对于被队列延迟了四十分钟的 run，就错了四十分钟，而这四十分钟**不属于任何 run**，因为下一个 slot 读取的也是它自己的 `now()`。没有任何东西失败；数据就是缺了，而且几周后才会被发现。

所以引擎改为提供一个时钟。在你原本会读取 `now()` 的地方，读取 `adjusted_at`：

| 参数 | | |
|---|---|---|
| `adjusted_at` | 始终 | 这个 run 应该读取的时钟：有 slot 时是 slot，否则是开始时间 |
| `date` | 始终 | `adjusted_at` 的 `YYYY-MM-DD` 形式，UTC：分区、文件夹、`WHERE` |
| `delay_seconds` | 始终 | 这次尝试相对其 slot 晚了多久；没有 slot 的手动 run 为 `0` |
| `previous_error` | 始终 | 在这个 run 之前的那个 run 没有成功 |
| `scheduled_at` | 有 slot 时 | cron 所要求的 slot |
| `started_at` | | 这次尝试开始的时间；重试会改变它 |
| `interval_start` | 有 slot 时 | **上一个** slot |
| `interval_end` | 有 slot 时 | 这个 slot，**不含** |
| `previous_success_at` | | 最近一次成功的 run 的 slot |

当 run 由调度产生、由 backfill 产生，或者由手动触发**针对某个过去的 slot** 时，它就有 slot：触发请求中的 `slot` 字段（`2026-10-27T04:00:00Z`）会用该 slot 的参数再次运行该 slot。cron 从不触发的 slot 会被拒绝，并指出在它之前最近的一个。

**这里的每个时间戳都是 UTC**，写成带 `Z` 的 RFC 3339 格式，run 的页面也以 UTC 显示，因此页面与 `$BREVIS_AUTO_ADJUSTED_AT` 永远一致。`date` 是 **UTC 日期**，对于 slot 跨越 UTC 午夜的 workflow 来说，它不是本地日期：`America/Sao_Paulo` 时区中的 `0 22 * * *`，其 `date` 是第二天。Airflow 的 `ds` 行为相同，原因也相同：slot 是一个时刻，只有选定时区之后，一个时刻才属于某一天。

`interval_start` 和 `interval_end` 来自 **cron**，而不是历史：对三月的某个 slot 做 backfill，得到的是三月当时的窗口，而不是这个 workflow 的 run 今天恰好描述出的窗口。请求 `[start, end)` 的 pipeline，无论运行得多晚、重试得多频繁，都不会重叠，也不会出现空缺。

它们是一份**快照**，在 run 开始时计算并存储在 run 上。`previous_error` 是关于这个 run 开始那一刻的事实；明天重新计算它，在前一个 run 被重试并通过之后，会用同一个名称回答另一个问题。

### 读取它们

在 shell step 中，每个值对应一个变量，因此无需解析任何东西：

```yaml
steps:
  - id: extract
    run: |
      curl "https://api.example.com/events?since=$BREVIS_AUTO_INTERVAL_START&until=$BREVIS_AUTO_INTERVAL_END" \
        > "/data/$BREVIS_AUTO_DATE.json"
```

整套参数也以一个 JSON 对象的形式放在 `$BREVIS_AUTO_PARAMS` 中，适合想一次性解析的 step。

在 Go 中，[SDK](/zh/docs/sdk/index.md) 会替你读取：

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    start, end, ok := p.Run.Auto.Window()
    if !ok { // no schedule: fall back to a fixed window
        start, end = p.Run.Auto.Now().Add(-24*time.Hour), p.Run.Auto.Now()
    }
    p.Source.URL += "&from=" + start.Format(time.RFC3339) +
        "&to=" + end.Format(time.RFC3339)
    return nil
},
```

`Auto.Now()` 是用来取代 `time.Now()` 的时钟。在引擎之外，比如有人手动运行的抓取器，它*就是*挂钟时间，因此本地开发无需做任何特殊处理。

在 Python 中，`pip install brevis` 读取同样的值：

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

当 workflow 没有调度时，`window()` 返回 `None`，而不是一对零值时间：从纪元开始的查询会选中所有数据，这种失败不该是无声的。上面的 `or` 就是全部的回退逻辑。

### 在 dlt 中，什么都不用读取

[dlt](https://dlthub.com) 在自己的游标上保存状态，写入目的地。这个状态只会**向前**移动，这在重新运行旧 slot 之前都没问题：游标仍然指向今天，于是 backfill 读取的是今天的窗口，而 dlt 对此的处理方式是丢弃状态。

Brevis 提供的窗口来自 cron，所以三月的 run 无论重试多少次，得到的都是三月的窗口。dlt 会接受它：这些名称是 dlt 自己的，它会在去找 Airflow 之前先读取它们，而引擎会在每个调度的 run 上设置它们：

```python
@dlt.resource(primary_key="id")
def events(
    updated_at=dlt.sources.incremental(
        "updated_at",
        initial_value=datetime(1970, 1, 1, tzinfo=timezone.utc),
        allow_external_schedulers=True,   # this line is the whole integration
    )
):
    yield from api.events(since=updated_at.start_value, until=updated_at.end_value)
```

`allow_external_schedulers=True` 就是全部。`$DLT_INTERVAL_START` 和 `$DLT_INTERVAL_END` 成为 dlt 的 `initial_value` 和 `end_value`，两端都是半开区间：恰好落在起点上的行包含在内，恰好落在终点上的行不包含，这正是连续的 slot 既不留空缺也不重叠的原因。

带有 `end_value` 的增量**不会触及持久化的状态**。这是关键所在：重新运行某一个 slot 是幂等的，对三月做 backfill 也不会移动夜间 run 所依赖的游标。

在它生效之前，有两件事需要知道：

- **游标必须是 datetime，而不是字符串。** dlt 不会为比较而强制转换 `text` 类型的游标，并且会彻底拒绝加入调度器，抛出一个不太容易看出原因的 `JoinSchedulerError`。请在 resource 上转换它（`add_map`），或像上面那样为 `initial_value` 指定类型。
- **没有调度的 workflow 不会提供任何东西**，这时 dlt 会抛出 `ExternalSchedulerNotAvailable`，而不是悄悄回退到它自己的游标。如果一条 pipeline 需要两种方式都能工作，请不要开启 `allow_external_schedulers`，而是自己通过 `run.window()` 传入窗口。

在 step 上声明 `tools: [dlt]` 是另一回事，它只会在图上画出标签，无论是否声明，窗口都会被传入。

## 快照

参数存储在 **run 中**，而不是在执行时从 workflow 读取。run 携带触发它时的输入，因此一月份某次执行的日志，即使默认值之后发生变化，仍会显示一月份的值。

## 下一步

- [配置](/zh/docs/configuration/index.md)：进程的环境变量
- [SDK](/zh/docs/sdk/index.md)：抓取器如何读取 run 的上下文
- [step 之间的上下文](/zh/docs/context/index.md)：step 为下一个 step 发布什么
