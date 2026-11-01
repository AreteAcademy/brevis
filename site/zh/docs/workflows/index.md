# Workflows

> 完整的 YAML 格式：step、依赖、镜像、资源和调度。

*https://brevis.sh/zh/docs/workflows/ · brevis.sh docs (zh-CN)*

---

workflow 是一个 YAML 文件。它声明**运行什么**、**以什么顺序**以及**使用什么运行时**，除此之外别无其他。

## 最小结构

```yaml
name: hello
steps:
  - id: only
    run: echo done
```

`name` 在数据库和界面中标识这个 workflow。`steps` 是 step 列表，每个 step 都有唯一的 `id` 和一个 `run`。

### `description`：它的用途

可选，自由格式，引擎中没有任何部分会读取它：

```yaml
name: daily-orders
description: >
  Pulls yesterday's orders from the vendor and lands them in bronze.
  Runs at 04:00 because the vendor closes its books at 03:30.
steps:
  - id: extract
    run: python fetch.py
```

它显示在 workflow 页面的顶部，位于计数和调度之上。图已经说明了一个流程**做什么**；这里是唯一说明它为何存在的地方，而这正是凌晨三点第一次打开它的人真正需要的。

没有描述的 workflow 在这里什么都不显示，而不是显示一个空块，因为空块看起来像是加载失败了。

## 顺序：chain 或 dag

```yaml
type: chain   # order is the file's order
type: dag     # order comes from depends_on
```

`chain` 只是语法糖：解析器把这个序列转换成边，而**引擎只认识 DAG**。当顺序不是线性的时，使用 `dag`：

```yaml
type: dag

steps:
  - id: prepare
    run: ./prepare.sh

  # Siblings: they depend on the same step, so they run in parallel.
  - id: extract
    run: ./extract.sh
    depends_on: [prepare]

  - id: validate
    run: ./validate.sh
    depends_on: [prepare]

  - id: publish
    run: ./publish.sh
    depends_on: [extract, validate]
```

运行器**按层**遍历图：同一层内的所有 step 并行运行，只有上一层完全结束，下一层才会开始。

:::warning 一次失败会停止整层
运行器在一层中遇到**第一个**失败就会停下，不会启动下一层。出错后继续执行，会产生一个看起来完整的部分结果。
:::

## 调度

```yaml
schedule: "0 5 * * *"   # five-field cron
concurrency: 1
```

没有 `schedule` 时，workflow 是手动的：只能通过界面中的触发、`brevis run` 或 `backfill` 运行。

`concurrency: 1` 限制同一个 workflow 同时运行的 run 数量，它能防止 `*/15` 在某次 run 超过十五分钟时与自己重叠。

两个写入相同表的**不同** workflow 需要一个**组**。一个每日 workflow 和一个 `*/15` workflow 如果都构建相同的 dbt 模型，则各自声明：

```yaml
concurrency:
  group: jaffle_models   # the shared resource, named the same in both files
  limit: 1               # optional; a group without one means 1
```

组内所有 workflow 的 run 都计入同一个限制，无论是由什么触发的：调度、一次 landing、一次 backfill 还是一次点击。发现组已满的 run 会**在队列中等待**；它不会被跳过，也不会失败。第三个写入相同表的 workflow 只需指定该组名就能加入。等待期间，该 run 的页面会说明原因：哪个组、它的限制是多少，以及哪些 run 占用着 slot。

- 所有成员声明**相同的限制**。如果一个文件里组声明为 1，另一个文件里声明为 3，`validate` 和 `publish` 会拒绝，包括与已发布的成员比对时。
- 组只会让 **Brevis 的 run** 串行化。有人在自己的机器上启动的 `dbt run` 不在这个队列中，同样会失败。

## 镜像与资源

```yaml
image: us-central1-docker.pkg.dev/example/apps/dbt:1.10.3
resources:
  cpu: 200m
  memory: 1Gi
  limits: {memory: 2Gi}
```

在顶层声明时，它们是**每个 step 的默认值**。每个 step 都可以覆盖自己的设置：

```yaml
steps:
  - id: bronze
    run: dbt build --select bronze+          # inherits the top-level image

  - id: notify
    image: ghcr.io/example/notify:0.3        # another runtime
    shell: false                             # distroless has no shell
    run: /notify --channel data
    resources: {cpu: 25m, memory: 32Mi, limits: {memory: 64Mi}}
    depends_on: [bronze]
```

正是这一点，使一个 Go 抓取器与一个 1.9 GB 的 `dbt build` 并存时只需 12 MB 和 32Mi，而不是两者都按较大的那个来付出代价。见[每个 step 一个 pod](/zh/docs/pod-per-step/index.md)。

## step 字段

| 字段 | | |
|---|---|---|
| `id` | **必填** | 在 workflow 内唯一；显示在图和日志中的名称 |
| `run` | | 命令 |
| `image` | | step 的镜像；省略时继承顶层的镜像 |
| `host` | | 把命令发送到一台引擎**不**管理的机器，是 `image` 的替代方案，绝不与之并用。见 [Kubernetes 指南中的 `host:`](https://github.com/AreteAcademy/brevis/blob/master/docs/KUBERNETES.md#a-third-place-a-step-can-run-host) |
| `shell` | `true` | `false` 表示不使用 shell 运行，distroless 镜像必须如此 |
| `depends_on` | | 必须先完成的 `id` 列表；其中的条目可以是 `{step, label}` |
| `marker` | `false` | 没有命令的 step，用于 `start` 或 `end` |
| `resources` | | 该 step 的 `cpu`、`memory` 和 `limits` |
| `runtime`, `tools` | | step 的语言和工具，见 [step 运行时](/zh/docs/runtime/index.md) |
| `when` | `all_success` | 依赖处于什么状态时运行这个 step，见下文 |
| `unless_empty` | | 一个 context 键，用来决定是否有事可做，见下文 |
| `for_each` | | 一个保存列表的 context 键；step 对每个元素各运行一次，见下文 |
| `group` | | 把这个 step 画在一个带名称的可折叠方框内，见下文 |
| `uses` | | 另一个 workflow，其 step 将取代这个 step 的位置，见下文 |
| `on_error` | | 通知这个 step 的失败，见下文 |

## 仅在有事可做时运行 step

```python
# in extract
context.set(has_rows=len(rows) > 0)
```

```yaml
  - id: transform
    run: ./transform.sh
    depends_on: [extract]
    unless_empty: extract.has_rows
```

`unless_empty` 指定的是一个**键**，而不是表达式。由 step 做出判断并发布答案；引擎只读取一个键，并判断它是否为空。

| 视为空 | 视为非空 |
|---|---|
| `false`、`0`、`""`、`null`、`[]`、`{}` | 其他所有值 |

作为**字符串**的 `"false"` 是非空的。发布了这五个字符的 step 发布的就是一个有内容的值，而去猜测它想表示布尔值，正是规则开始带上作者看不见的主观判断的方式。请发布真正的布尔值。

### 为什么不用表达式

看起来诱人的设计是 `when: "{{ context.extract.rows > 0 }}"`。一门迷你表达式语言是一项庞大的承诺：需要解析器，需要类型系统来说明 `>` 在 JSON 的 `any` 上意味着什么，需要一套安全方案（因为表达式来自别人写的 YAML），还有指向字符串内部的错误信息。每个提供这种能力的编排器，其 bug 跟踪器里都满是相关问题。

在这里，判断留在作者本来就在使用的语言里，他们自己的测试框架可以触及它。

### 缺失的键是失败，而不是空值

如果键不存在，step 会**失败**，错误信息会指出这个键，以及发布方实际写入了什么：

```
step "transform" reads `extract.has_row`, and "extract" published "has_rows" but not "has_row"
```

另一种做法是把缺失的键当作“为空，所以跳过”，这会把一个拼写错误变成一个悄悄地、永远停止运行的 step，而且任何地方都不会说明原因。这是这个功能可能出现的最糟糕的结果。

键始终以发布它的 step 来限定，并且该 step 必须是当前 step 所依赖的 step。这两点都在 **publish** 时检查：以一个永远看不到的键作为条件的 step 将永远被跳过，等到某个夜间任务不再运行时才发现就太晚了。

## 对每个元素各运行一次 step

```python
# in extract
context.set(partitions=["2026-01", "2026-02", "2026-03"])
```

```yaml
  - id: load
    run: ./load.sh "$BREVIS_MAP_VALUE"
    depends_on: [extract]
    for_each: extract.partitions
```

step 对每个元素各运行一次，每次都有自己的行、重试和退出码。图中显示的是**一个带有 `[3]` 的节点**，而不是三个节点。

| 变量 | |
|---|---|
| `BREVIS_MAP_INDEX` | `0`、`1`、`2` …… |
| `BREVIS_MAP_VALUE` | 该元素 |

JSON 字符串会**不带引号**传入，因此对 `["2026-01"]` 做 `for_each` 时，shell 收到的是 `2026-01` 而不是 `"2026-01"`。其他任何类型（数字、对象、列表）都以其 JSON 形式传入。

这两个变量在未做映射的 step 上都不存在。一个始终存在却始终为空的变量，只会教会读取环境的人忽略它。

### DAG 的形状不会改变

做了映射的 step 仍然是一个节点、一组边。只有它下面的行数会变化，因此布局与以往一致，`[3]` 是在读取时根据这些行统计出来的：没有存储任何东西，所以无需保持同步，查询修好之后错误的计数也不会残留。

实例仍在运行时，卡片显示为 `[2/3]`。

### 一个实例失败，整个 step 就失败

三个分区在加载，其中一个没有成功，这个 step 就没有完成它的工作，它下面的 step 会看到这一点。一个因为大部分成功就变绿的节点，是一个说谎的徽标。

**重试只会重做失败的实例。** 已经成功的十八个分区，不会因为另外两个出错而被重新加载。

### 空列表是 `skipped`，而不是成功

一个因为无事可做而什么都没做的 step，并没有成功地完成它。一个对应零个实例的绿色节点，恰恰是有人会拿来搭建仪表盘的那种东西。

**不是列表**的值会导致失败，键不存在也一样，这与 `unless_empty:` 的策略相同，原因也相同：一个悄悄产生零个实例的拼写错误，会让该 step 永久失效。

### 展开有上限，且上限是继承来的

列表通过 context 传递，而 context 有 4096 字节的上限，这个上限来自 kubelet。workflow 不能先不想办法用四千字节表达，就要求一万个 pod。这是一个值得保留而不是绕过的限制。

### 做了映射的 step 发布什么

它的输出记录在每个实例的行上，对其下方的 step **不可见**。四个实例以同一个 step 的名义发布，就是同一个键对应四个值，而对于 `context.String("load.bucket")` 来说，任何答案都只是猜测。step 会在日志中说明这一点，而不是悄悄丢弃。

## 在图上对 step 分组

```yaml
steps:
  - id: extract_orders
    group: sales_data_reporting
    run: ./extract.sh
  - id: load_orders
    group: sales_data_reporting
    depends_on: [extract_orders]
    run: ./load.sh
```

这些 step 被画在一个可折叠的带名称方框内，相当于 Airflow 的 TaskGroup，适用于 DAG 大到难以阅读的情况。

**仅限视觉。** Airflow 的 TaskGroup 还会给其中的 id 加*前缀*，于是 `extract` 变成 `sales.extract`。这里不会这样：加前缀会改变现有 workflow 中的每个 `depends_on`、每个 context 键和每条已记录的行，而这个功能的全部价值仅仅在于让大图更易读。命名空间以后可以再加，它是对现有功能的严格补充。

组是一个**标签**，而不是容器。step 保留它们的全局 id，组可以跨越多层，执行方面没有任何改变。

点击组名即可折叠：其中的 step 消失，穿过边界的箭头改为指向方框。

## 复用另一个 workflow

```yaml
# nightly.yaml
steps:
  - id: prepare
    run: ./prepare.sh

  - id: mlops
    uses: ml_training       # another workflow in the same publish
    depends_on: [prepare]

  - id: report
    run: ./report.sh
    depends_on: [mlops]
```

在 **publish** 时，`ml_training` 的 step 取代该 step 的位置，并加上它的 id 作为前缀，`uses` 节点随之消失：

```
prepare → mlops.train → mlops.evaluate → report
```

它们作为一个可折叠的组出现，因此图展示的就是文件所写的内容。

**在 publish 时展开，而不是在运行时执行。** 让 step 触发一次子 *run* 并等待，正是让 Airflow 发生死锁的设计，而这个引擎具备同样的条件：pod 上限是每个进程的信号量，所以父级占着一个 slot 等待需要同一个池中 slot 的子级时就会挂起，而且只在负载高时发生，也就是在生产环境中。一个 run，一张图，一个池。

它放弃的是拥有自己 id 和自己历史的子 run。

### 规则

| | |
|---|---|
| 子 workflow 必须在**同一次 publish** 中 | 仅仅已经发布过是不够的 |
| 箭头 | 指向该 step 的箭头变为指向子 workflow 每个**根**的箭头；从它出发的箭头改为从子 workflow 的每个**叶**出发 |
| 子 workflow 的 `image`、`env`、`secrets`、`resources` | 物化到每个 step 上，使其按自己文件所写的环境运行 |
| 子 workflow 的 `unless_empty` / `for_each` 键 | 随前缀一起移动 |
| 嵌套 | 展平，深度优先 |
| 循环引用 | 在 publish 时被拒绝，并指出整条链 |

**重要的是“同一次 publish”，而不是“已经发布”。** 如果展开时读取数据库，`brevis validate`（它刻意不接触数据库）和 `brevis publish` 回答的就是不同的问题，通过 CI 的文件会在部署时失败。

子 workflow 仍可单独发布：展开是**复制**，而不是消耗。一个 step 不能同时声明 `uses:` 和 `run:`、`action:` 或 `marker:`，因为那样文件就在说两件事。

## 说明箭头的含义

依赖可以带一个标签，显示在图中的箭头上。

```yaml
steps:
  - id: determine_load_type
    run: ./decide.sh

  - id: load_full
    run: ./full.sh
    depends_on:
      - {step: determine_load_type, label: additional data}

  - id: load_delta
    run: ./delta.sh
    depends_on:
      - step: determine_load_type
        label: changed existing data
```

简写形式 `depends_on: [extract]` 仍然有效，并且仍是常规写法。大多数依赖没什么可说的，每个箭头上都加标签只会造成噪音。

标签的价值体现在**分支**上：从同一个 step 发出的两个箭头上什么都没写，这样的图需要打开源文件才能读懂，而这恰恰是图应该避免的。

## 什么都不做的 step

```yaml
steps:
  - id: start
    marker: true

  - id: end
    marker: true
    depends_on: [load_full, load_delta, report]
```

`marker: true` 是没有命令的 step。它不运行任何东西，立即成功，并显示在图上。

它不是装饰。一个依赖所有分支的 `end`，把“整件事完成了吗？”变成一个节点，而不是要跟踪的六条箭头。它的行为与其他 step 一样，所以位于失败分支之下的 `end` 会被**跳过**，而不是变绿。

**既没有 `run:` 也没有 `marker: true` 的 step 仍然会被拒绝。** 这两种情况不能混为一谈：空命令几乎总是一个错误，而 `marker: true` 是有人表明自己确实是有意为之的方式。同时声明了 `run:` 的 marker 也会被拒绝，因为那样文件就在说两件事。

## 仅在有东西失败时运行 step

默认情况下，step 在它之前的一切都成功时运行。`when:` 可以改变这一点。

```yaml
steps:
  - id: extract
    run: python fetch.py

  - id: notify_failure
    run: ./notify.sh
    depends_on: [extract]
    when: any_failed          # runs precisely when extract did not make it

  - id: cleanup
    run: ./cleanup.sh
    depends_on: [extract, transform]
    when: all_done            # runs either way
```

| 规则 | |
|---|---|
| `all_success` | 默认值。该 step 所依赖的一切都成功了，指的是它**自己的**依赖，与 Airflow 相同 |
| `any_failed` | 该 step 所依赖的 step 中至少有一个失败 |
| `all_done` | 该 step 所依赖的一切都已结束，无论结果如何 |

未知的规则会在 publish 时被拒绝，并指出有效的取值。必须如此：如果 `when: on_failure` 被当作“默认值”，它会在**成功**时运行，与字面意思正好相反，而且会在最关键的那个夜晚才被发现。

### 不运行的 step 是 `skipped`

它是一个独立的状态，用独立的颜色绘制，并带有原因：*`extract` 失败了*。在此之前，失败之下的 step 完全没有记录，界面会一直显示它处于 pending。

`skipped` 既不是失败，也不是成功。上游出错的 step 并没有失败，它只是从未获得运行的机会；把它称为成功是一种谎言，会一直传到 run 自身的状态。

触发规则决定哪些 step **运行**，不决定 run 的结果：成功送出消息的 `notify_failure` 并不意味着 pipeline 正常工作，这次 run 仍然是失败的。

### `all_success` 是局部的，与 Airflow 一样

该规则只询问 step **自己的**依赖，别无其他。不相关分支中的失败不会阻止这个 step：

```
extract_orders ──✕                 (failed)
extract_users  ──✓── transform_users ──✓     keeps going
```

失败**之下**的分支仍然会停止：`all_success` 是局部的，并不意味着它不存在。

这个引擎过去会在第一次失败时中止整张图。这样做有它的理由：出错后继续执行会产生一个看起来完整的部分结果，曾经有一条 pipeline 在没人察觉的情况下晚了 28 天。

**取代这种保护的是什么**，而且并非微不足道：

- run 仍然会**失败**，并带着同样的错误；
- 图中把失败的 step 标为红色，每个被跳过的 step 用自己的颜色，并各自说明是哪个 step 阻止了它；
- run 放弃时，告警仍然会发出。

部分结果不再看起来完整，因为 run 会说明它并不完整。

**放弃的是什么**，直说：不相关的分支现在会在别处失败的 run 上写入它的数据。

### 重试只重新运行失败的部分

run 的重试不会重做整张图。在**同一个 run** 的早先尝试中已经成功的 step 会保留其结果，不再重新运行，这与 Airflow 中清除 DAG run 的行为一致。

```
attempt 1   extract ✓    load ✕    report (skipped)
attempt 2   extract –    load ✓    report ✓            extract is not re-run
```

它保留原来的行：其耗时、日志和所发布的内容都属于第一次尝试，因为工作就是在那时完成的。**它下面的 step 仍然读取它发布的内容**，因为 run 的 context 是存储下来的，而不是重新构建的。

关键的区别在于：这只涉及**这个 run** 先前的尝试。在*昨天的* run 中成功的 step，今天照常运行。（另一个问题同样存在，它告诉 SDK 这是否是某个 step 的首次运行，见 `BREVIS_RUN_FIRST`。）

被**跳过**的 step 不算已确定：它从未运行，重试会根据新一次尝试的结果再次决定它。

## 通知 step 的失败

每次失败都已经会被通知：配置了 webhook 后，放弃的 run 会发出一条消息，任何文件中都不需要重复任何块。`on_error` 适用于需要自己专属通知的 step。

```yaml
steps:
  - id: fetch_observations
    run: python fetch.py
    on_error:
      type: SLACK
```

| 字段 | | |
|---|---|---|
| `type` | **必填** | `SLACK`。未知的值会在 publish 时被拒绝，并指出有效的取值 |
| `when` | `give_up` | `attempt` 会通知每一次失败的尝试，而不仅仅是最后一次 |

会收到两条消息而不是一条，这正是用意所在：run 级别的告警说的是 *`id_verification` 失败了*，这是 pipeline 负责人需要的；step 级别的告警说的是 *`fetch_observations` 失败了*，这是该集成负责人需要的。

**没有 `webhook` 或 `url` 字段，将来也不会有。** 目的地是一项凭据，谁持有它，谁就能像平台一样在频道里发言；而 workflow 文件的作者不一定有权选择公司的告警发往何处。文件说明**是否**以及**如何**通知；安装环境决定发往**哪里**，通过 `BREVIS_SLACK_WEBHOOK`。

**`when: give_up` 是默认值，是出于被打扰的是谁的夜晚的考虑。** 一个失败两次、第三次通过的 step，在另一种默认值下会发出两条消息，而第二条到达时问题已经消失了。

**没有失败的 step 绝不会被通知。** 在一个分支出错的 workflow 中，另一个分支的负责人不会被吵醒。

投递由 `brevis alert` 负责：告警与失败记录在同一个事务中，所以 Slack 故障只会延迟告警，而不会丢失它。

## 标签

```yaml
tags: [analytics, dbt, daily]
```

它们用于在界面中筛选，不影响执行。

## 校验

校验不需要数据库，因此可以在 CI 中与测试一起运行：

```bash
brevis validate workflows/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencias  cron 0 2 * * *
```

它接受文件或目录。对于目录，它匹配 `*.y*ml` 并排序：两次运行产生相同的日志，两次部署之间的差异不会变成噪音。

## 发布

```bash
brevis publish workflows/
brevis publish workflows/ --project acme --prune
```

`--prune` 会从项目中删除不在已发布列表中的 workflow，但保留它们的历史。它**不是**默认行为，原因很实际：`publish one-file.yaml` 不应该仅仅因为另外四十八个文件没有在命令行中列出，就把它们删掉。

## 下一步

- [参数](/zh/docs/parameters/index.md)：两次 run 之间可以变化的内容
- [调度器与队列](/zh/docs/scheduler-and-queue/index.md)：workflow 如何变成执行
- [step 之间的上下文](/zh/docs/context/index.md)：一个 step 告诉下一个 step 什么
