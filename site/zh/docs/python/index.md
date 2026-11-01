# Python

> pip install brevis：step 之间的上下文、run 的时钟，以及你的 pipeline 自己的指标，完全没有依赖。

*https://brevis.sh/zh/docs/python/ · brevis.sh docs (zh-CN)*

---

```bash
pip install brevis
```

```python
from brevis import context, metrics, run

bucket = context.get("extract.bucket")
since, until = run.window()          # the window this run covers

context.set(rows=48213, watermark="2026-09-07T03:00:00Z")
metrics.set("rows_loaded", 48213)    # reaches the engine's /metrics
```

**永远没有依赖。** 它读取一个环境变量，写入一个文件，向 stdout 打印一行。需要 Python 3.9+。

它不是 Go SDK 的移植版：没有驱动，没有分页，没有摄取 id。它存在的目的，是让团队可以使用 pandas、Polars 或 dbt，同时仍能获得编排器应当提供的东西。

## 读取上下文

键始终由发布它的 step 限定：

```python
context.get("extract.bucket")            # the value
context.get("extract.nope", default=0)   # absent is not an error
context.of("extract")                    # everything that step published
context.published()                      # what THIS step has published so far
```

不带 step 的 `context.get("bucket")` 会被**拒绝**，并指出发布了该键的 step。step 之间是相互隔离的：`extract` 和 `transform` 都可以发布 `bucket`，而不会有任何一个丢失，而不带 step 的键一旦两个 step 都发布了，就会丢掉这种隔离。通过搜索挑选其中一个，是偶然形成的优先级：它在悄悄失效之前一直有效。

读取当前 step 并不依赖的 step 是错误，错误信息会指出修复办法。

## 写入

```python
context.set(name="Daniel")
context.set(label="Name")
# both survive: {"name": "Daniel", "label": "Name"}
```

调用会**合并**。同一个键写两次，保留最后一次写入；不同的键则不会影响第一个。**没有 step 参数**：进程唯一能写入的命名空间就是它自己的，这使隔离成为结构上的保证，而不是一条规则。

在进程退出之前，没有任何内容会写入磁盘，所以异常终止的 step 什么都不会发布。这是正确的：崩溃的 step 的输出所描述的工作并没有完成。

## 这个 run 应该读取的时钟

每个 run 还带有**自动参数**：引擎自己算出的值。没有人声明它们，每个 run 都有，它们回答的是 pipeline 否则会用 `datetime.now()` 回答的问题。

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

:::warning 为什么不用 `datetime.now()`
在调度的 run 上，`run.now()` 是 **slot**。读取 `datetime.now()` 并减去两小时的抓取器，在按时启动的 run 上是对的，而在被队列延迟的 run 上会错四十分钟，这四十分钟**不属于任何 run**，因为下一个 slot 读取的也是它自己的 `now()`。没有任何东西失败，缺口要几周后才会被发现。
:::

| | |
|---|---|
| `run.now()` | 时钟：slot，或 run 开始的时间 |
| `run.window()` | `(start, end)`，不含 end，或者 `None` |
| `run.auto()` | 自动参数，见下文 |
| `run.context()` | id、尝试次数、触发方式、参数和 auto |
| `run.params()` · `run.param(name)` | workflow 中声明的参数 |
| `run.map_index()` · `run.map_value()` | `for_each:` 下的实例 |

`window()` 来自 **cron**，而不是历史：对三月的某个 slot 做 backfill，得到的是三月当时的窗口。请求 `[start, end)` 的 pipeline，无论 run 多晚、重试多频繁，都不会重叠，也不会出现缺口。

当 workflow 没有调度时，它返回 `None`，而不是一对零值时间：从纪元开始的查询会选中所有数据，这种失败不该是无声的。上面的 `or` 就是全部的回退逻辑。

### 自动参数

| 字段 | |
|---|---|
| `date` | `"2026-09-08"`：分区、文件夹、`WHERE` |
| `adjusted_at` | 用来取代 `now()` 的时钟 |
| `scheduled_at` | cron 所要求的 slot；手动 run 时为 `None` |
| `started_at` | **这次尝试**开始的时间；重试会改变它 |
| `delay_seconds` | 这次尝试晚了多久；永远不会为负 |
| `interval_start` · `interval_end` | 窗口，不含 end |
| `previous_error` | 在这个 run 之前的那个 run 没有成功 |
| `previous_success_at` | 最近一次**成功**的 run 的 slot，这使 `previous_error` 变得可操作 |

手动运行脚本时，`run.now()` **就是**挂钟时间，`window()` 是 `None`，所以本地开发无需编写任何分支。

## 做了映射的 step 的一个实例

在 [`for_each:`](/zh/docs/workflows/index.md) 下，引擎为每个元素运行一个进程，并告诉每个进程它拿到的是哪一个：

```python
partition = run.map_value()
if partition is None:
    raise SystemExit("this step is meant to run under `for_each:`")

load(f"/data/{partition}.csv")
```

JSON 字符串会**不带引号**传入：对 `["2026-01"]` 做 `for_each` 时，step 收到的是 `2026-01`，而不是 `"2026-01"`。其他任何类型（对象、数字、列表）都以其 JSON 形式传入，因此用 `json.loads(run.map_value())`。

在没有做映射的 step 上，两者都是 `None`。是 `None`，而不是 `-1` 或 `""`：哨兵值是最终会有人拿来做算术的数字，而空字符串与*本身就是*空字符串的元素无法区分。

做了映射的 step 不会向下游发布**任何 context**：四个实例无法共用一个键。需要传递东西的 step 应写一个文件，或者由其后的 step 统计落地了什么。

## 只有你的 pipeline 才知道的指标

引擎已经度量了它能看到的东西：耗时、尝试次数、队列深度。它无从得知的是有多少行落地、供应商拒绝了多少行、丢弃了多少字节。step 知道这些，而库负责把它们带出去。

```python
from brevis import metrics

metrics.set("rows_loaded", 48213)          # a gauge: the last value wins
metrics.inc("vendor_rejected_total")       # a counter: it adds up
metrics.inc("bytes_discarded_total", 4096)
```

它们出现在**引擎**的 `/metrics` 中，位于调度器的端口上，并带有 workflow 和 step 的标签：

```
brevis_step_rows_loaded{workflow="daily_sales",step="load"} 48213
brevis_step_vendor_rejected_total{workflow="daily_sales",step="load"} 5
```

| | |
|---|---|
| `metrics.set(name, value)` | gauge。step 退出后，该值会保留，直到下一次 run 写入另一个值 |
| `metrics.inc(name, by=1)` | counter。在 step 之间和 run 之间累加 |

### 为什么这不会开启端口

因为 **step 不可被抓取**。`:9090` 属于调度器和 API，它们是长期运行、地址稳定的进程，每十五到六十秒被抓取一次。step 在自己的 pod 中启动，运行四十秒就退出：它打开的端口要么永远不会被抓取，要么只是碰巧被抓取一次。

已知的三种应对办法是：Pushgateway（需要运维的组件，而且序列会一直保留到有人删除）、OTLP collector（既是一个组件，也是这个库里的一项依赖，而这个库没有依赖，也不会有），或者让**编排器**来承载它们。Brevis 本来就会读取每个 step 的 stdout，寻找 `@brevis:` 行，SDK 的各个阶段就是这样到达图上的，而且它本来就有计量器和 Prometheus 端点。

所以库向 stdout 写**一行**，其余交给引擎。指标会自动带上 workflow 和 step 的标签，因为引擎知道自己运行的是什么；而通过 Pushgateway，这个库必须自己加标签，并且会加错。

写入使用 `print(flush=True)`，因为引擎是**实时**读取这条管道的。带缓冲的行要到进程退出时才会到达，对于运行一小时的 step 来说就是晚了一小时，对于被杀掉的 step 则永远不会到达。

### 指标会拒绝什么

**无效的名称会被明确拒绝。** Prometheus 接受字母、数字和下划线，且首字符不能是数字。它不接受的名称不会产生一个损坏的指标，而是产生一份它**拒绝解析**的输出，导致**整次抓取**丢失，连同其他所有指标。所以 `rows-loaded` 会在犯错的地方抛出 `MetricError`，而不是以一个没人写过的名称到达。

| 被拒绝的 | 原因 |
|---|---|
| `rows-loaded`、`rows loaded`、`2rows` | Prometheus 不会解析它，整次抓取都会丢失 |
| `metrics.set("ok", True)` | 在 Python 中 `bool` 是 `int`，而上报 `1` 是一个含义取决于是否知道这一点的数字。请上报数量或耗时，而不是状态 |
| `metrics.inc("x", -1)` | counter 只增不减。对它做的每个 `rate()` 都基于这一假设，而问题会在几周后的图表中暴露出来 |

手动运行脚本时，这一行仍会输出到 stdout，在那里它就是它本来的样子。没有东西收集它，也没有东西失败，这与笔记本电脑上的 `context.set()` 一样。

## 它拒绝什么

| | 原因 |
|---|---|
| 超过 4096 字节 | 平台会**截断**而不是拒绝，所以过大的对象会被截断一半，并在下游被读作“什么都没发布” |
| 不是 JSON 的值 | `datetime` 会变成 `"..."` 还是消失，取决于由谁来序列化。错误信息会指出键并建议使用 `.isoformat()` |
| 非字符串的键 | `{1: "x"}` 会变回 `{"1": "x"}`，导致查找落空 |

这个上限是平台的数字，不是我们的。每个“任务之间的 context”功能都以同样的方式失败：有人把**数据**放到了只适合放 **context** 的地方。四千字节足够放下一个水位线、一个计数或一个分区列表。请发布引用，让行留在原处。

:::danger 这不是密钥存储
发布的所有内容对任何能看到这个 run 的人都是可见的：它在 pod 的状态里，也在 run 的历史里。请发布路径，而不是签名 URL。
:::

## 手动运行

在 Brevis 之外没有输入，也没有任何东西会读取输出。`get()` 返回默认值，`set()` 接受并丢弃，库会在 INFO 级别说明一次。它**不会抛出异常**：无法手动运行的脚本，是无法开发的。校验仍然会运行，所以在你笔记本电脑上能工作的值，在生产环境中也能工作。

## 测试你的 step

整个约定就是两个环境变量，因此没有什么需要 mock：

```python
def test_my_step(monkeypatch, tmp_path):
    monkeypatch.setenv("BREVIS_INPUT", '{"extract": {"rows": 42}}')
    monkeypatch.setenv("BREVIS_OUTPUT", str(tmp_path / "out"))
    assert context.get("extract.rows") == 42
```

## 版本

该库的版本与引擎和 Go SDK 各自独立。

:::warning 不要使用 `0.2.0`
它在 PyPI 上只发布了 wheel。`0.2.1` 是同样的代码，且是完整的。
:::

`brevis.run` 需要 **`v0.9.0` 或更新版本**的引擎才有东西可读；在更旧的引擎上，每个字段都是空的，`run.now()` 是挂钟时间，与它在引擎之外的行为相同。

## 下一步

- [客户端库](/zh/docs/libraries/index.md)：约定以及其他语言
- [step 之间的上下文](/zh/docs/context/index.md)：概念
- [参数](/zh/docs/parameters/index.md)：声明的参数
