# step 之间的上下文

> 一个 step 告诉下一个 step 什么：两个环境变量和一个文件。

*https://brevis.sh/zh/docs/context/ · brevis.sh docs (zh-CN)*

---

step 发布一些值，依赖它的 step 读取这些值。这个功能做的就是这些，仅此而已。

```yaml
steps:
  - id: extract
    run: python fetch.py

  - id: transform
    run: python transform.py
    depends_on: [extract]
```

```python
# extract
from brevis import context
context.set(bucket="s3://landing/2026-09-07", rows=48213)
```

```python
# transform
from brevis import context
bucket = context.get("extract.bucket")
```

键始终是 `<step id>.<name>`。step 读取的是**它自己的依赖**发布的内容，别无其他。

:::note 它不是什么
它不是消息队列，不是 run 之间的状态，也不是存放数据的地方。它是一个 step 留给下一个 step 的便条：一个路径、一个水位线、一个计数。
:::

## 任何语言，无需任何库

约定只有**两个环境变量和一个文件**，因此完全不使用 SDK 的 step 也能参与：

```bash
window=$(jq -r '.extract.watermark' <<< "$BREVIS_INPUT")
./publish --until "$window"
jq -n --arg n "$(date -Is)" '{published_at: $n}' > "$BREVIS_OUTPUT"
```

| | |
|---|---|
| `BREVIS_INPUT` | `{"<step id>": {…}}`：这个 step 的依赖所发布的内容 |
| `BREVIS_OUTPUT` | 一个路径，用来写入**一个** JSON 对象 |

在 Kubernetes 下，这个路径是 `/dev/termination-log`，引擎本来就会读取它来获取退出码。所以**没有 API 调用，没有 token，也没有端口**，对应的库只需要解析一次 JSON 并写一次文件。

| 语言 | |
|---|---|
| Python | [`pip install brevis`](/zh/docs/python/index.md)：它还带有 `brevis.run`，提供[自动参数](/zh/docs/parameters/index.md) |
| Go | `sdk/context`，属于 SDK 模块，不依赖任何东西 |
| 其他任何语言 | 上面的两个变量 |

## 隔离

任何 step 都不能覆盖另一个 step 发布的内容。键带有写入者 id 作为前缀，而这个前缀由引擎决定，不由 step 决定。两个 step 都发布 `bucket`，得到的是 `extract.bucket` 和 `load.bucket`，二者都不会消失。

## 4096 字节的上限

step 发布的内容必须能放进 **4096 字节**。这个限制并非随意设定：在 Kubernetes 下，传输通道是 `/dev/termination-log`，kubelet 只接受这么多。

超出上限是一个 **step 错误**，错误信息会说明写入了多少字节。另一种做法是截断，但被截断的 JSON 是无效的 JSON，要由下一个 step 才能发现，而它无从得知错不在自己。

如果一个值放不下，它就不属于 context：把它写进 bucket，然后发布路径。

## 重试与恢复的 run

新的一次尝试中，step 会再次发布，之前的值被替换。已经成功的 step 既不会重新运行，也不会重新发布：它所说的内容对依赖它的 step 依然有效。

## 手动运行一个 step

这两个变量就是全部约定，所以复现一次运行，只需把它们导出：

```bash
export BREVIS_INPUT='{"extract":{"bucket":"s3://landing/2026-09-07"}}'
export BREVIS_OUTPUT=/tmp/out.json
python transform.py
cat /tmp/out.json
```

## 下一步

- [参数](/zh/docs/parameters/index.md)：run 之间可以变化的内容，以及自动参数
- [step 运行时](/zh/docs/runtime/index.md)：step 对其运行位置的声明
- [Python](/zh/docs/python/index.md)：客户端库，以及其他语言
