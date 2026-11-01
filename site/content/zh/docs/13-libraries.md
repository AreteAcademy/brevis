---
title: 客户端库
description: 任何语言的 step 如何读取上下文和 run 的时钟，以及哪些语言已经有了库。
group: SDK 与库
order: 13
slug: libraries
---

step 需要编排器提供两样东西：**上一个 step 发布了什么**，以及**这个 run 应该读取哪个时钟**。客户端库只交付这两样，别无其他。

## 先有约定，后有库

引擎定义的是**两个环境变量和一个文件**：

| | |
|---|---|
| `BREVIS_INPUT` | `{"<step id>": {…}}`：这个 step 的依赖所发布的内容 |
| `BREVIS_OUTPUT` | 一个路径，用来写入**一个** JSON 对象 |

此外还有 run 的变量和自动参数，都是只读的：`BREVIS_RUN_*` 和 `BREVIS_AUTO_*`。在 Kubernetes 下，`BREVIS_OUTPUT` 是 `/dev/termination-log`，引擎本来就会读取它来获取退出码。

所以**没有 API 调用，没有 token，也没有端口**。任何语言的 step，只需解析一次 JSON 并写一次文件就能参与：

```bash
window=$(jq -r '.extract.watermark' <<< "$BREVIS_INPUT")
./publish --until "$window"
jq -n --arg n "$(date -Is)" '{published_at: $n}' > "$BREVIS_OUTPUT"
```

这里的库不会开启任何新能力：它帮你省去了校验、缺失值处理和错误信息这些不值得重写的部分。

## 已有哪些库

| 语言 | | |
|---|---|---|
| **Python** | `pip install brevis` | [可用](/docs/python/)，PyPI 上为 `0.2.1` |
| **Go** | `brevis/sdk/context` | 可用，位于 SDK 模块内 |
| **Node.js** | — | 计划中 |
| **Rust** | — | 计划中 |
| 其他任何语言 | 上面的变量 | 现在即可 |

:::note Go 不放在 `lib/` 中
Go 的 context 支持位于项目已经发布的 `sdk/` 模块中，是一个子包，对于不导入它的人来说会被裁剪得一干二净。`lib/go-context` 意味着为一个已经有归属的包再建一个 Go 模块。

规则是：**每种尚未有已发布制品的语言，对应一个目录**。
:::

每个库都是独立的制品，有自己的版本和标签：修复 Python 客户端不需要发布新的引擎版本。

## 它们是什么，不是什么

它们是**基于该约定的轻量级客户端**，不是 Go SDK 的移植版。

移植 SDK 的 ETL 机制（驱动、分页、检查点、摄取 id）既不属于这里，大概也不属于任何地方：这些语言本身都已经有更好的工具，而 Brevis 的工作是运行它们。这正是 Python 库存在的原因：让团队可以使用 pandas、Polars 或 dbt，同时仍能获得编排器应当提供的那一件事。

## 指标

库负责**发出**指标，而由引擎来暴露它们：

```python
from brevis import metrics

metrics.set("rows_loaded", 48213)      # a gauge: the last value wins
metrics.inc("vendor_rejected_total")   # a counter: it adds up
```

它们出现在**引擎**的 `/metrics` 中，位于调度器的端口上，并带有 workflow 和 step 的标签，见[可观测性](/docs/observability/)。

**这不会开启端口，也不可能开启。** step 在自己的 pod 中运行四十秒就退出了；它打开的端口要么永远不会被抓取，要么只是碰巧被抓取一次。所以这一行输出到 stdout，也就是引擎本来就在读取、用来绘制 pipeline 各阶段的那条管道，由引擎来记录。无需安装，无需运维，标签也来自引擎，因为 step 自己不知道所属 workflow 的 slug。

API 的详细信息见 [Python](/docs/python/#只有你的-pipeline-才知道的指标)。

## 为其他语言编写客户端

如果你比我们更早做到 Node.js 或 Rust，下面是客户端必须做对的事，以及 Python 实现已经用教训学到的东西：

1. **键由 step 限定。** 不带 step 的 `get("bucket")` 必须被拒绝。两个 step 都可能发布 `bucket`，通过搜索挑选其中一个，是偶然形成的优先级。
2. **合并写入。** 以不同的键两次调用，两者都会保留；同一个键写两次，保留最后一次。
3. **仅在进程退出时刷新。** 异常终止的 step 什么都不发布，这是正确的，因为崩溃的 step 的输出所描述的工作并没有完成。
4. **超过 4096 字节就拒绝。** 平台会**截断**而不是拒绝，被截断一半的对象会作为无效的 JSON 到达下一个 step。
5. **在 Brevis 之外也能工作。** 没有 `BREVIS_INPUT` 时，`get()` 返回默认值，`set()` 接受并丢弃，不抛出异常：无法手动运行的脚本，是无法开发的。

## 下一步

- [Python](/docs/python/)：完整的 API
- [step 之间的上下文](/docs/context/)：概念以及 4096 字节的上限
- [参数](/docs/parameters/)：声明的参数和自动参数
