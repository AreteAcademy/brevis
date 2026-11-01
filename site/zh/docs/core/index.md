# Core

> 编排器：调度 run，把队列保存在 Postgres 中，并为每个 step 启动一个 pod，从不接触数据。

*https://brevis.sh/zh/docs/core/ · brevis.sh docs (zh-CN)*

---

整个 workflow 就是一个受版本控制的文件：评审一条 pipeline，就是评审一份 diff。Core 负责调度、排队，并用各自的镜像启动每个 step，从不接触数据。

![Core 的流程：调度器和队列保存在 Postgres 中，为 workflow 的每个 step 启动一个 pod，即 prepare、extract、validate 和 publish。从 Core 出去的只有控制线。数据从 HTTP 源进入运行 SDK 的 extract pod，再从那里流向 BigQuery；没有任何数据经过 Core。](/assets/flow-core.svg)

## 它做什么

- **调度。** 调度器读取每个 workflow 的调度并创建 run。它与队列是两个相互独立的循环：其中一个卡住不会阻塞另一个，见[调度器与队列](/zh/docs/scheduler-and-queue/index.md)。
- **排队。** 队列保存在 Postgres 中：执行方认领一个待处理的 run，遍历图，并记录每个 step 的状态。
- **为每个 step 启动一个 pod**，各自使用自己的镜像，见[每个 step 一个 pod](/zh/docs/pod-per-step/index.md)。
- **从不接触数据。** 从 Core 出去的只有控制线：读写数据的是 step，使用 [SDK](/zh/docs/sdk/index.md) 或你自己的任何代码。

## 一个 workflow，从头到尾

workflow 就是一个 YAML 文件。依赖同一个 step 的两个 step 会并行运行：

```yaml
name: hello
type: dag
tags: [example]

steps:
  - id: prepare
    run: sh -c 'echo preparing; sleep 1'

  # The next two depend on the same step, so they run IN PARALLEL.
  - id: extract
    run: sh -c 'sleep 2; echo 42 extracted'
    depends_on: [prepare]

  - id: validate
    run: sh -c 'sleep 1; echo validated'
    depends_on: [prepare]

  - id: publish
    run: echo published
    depends_on: [extract, validate]
```

在本地运行它，不需要队列，也不需要集群：

```bash
docker run --rm -v ./hello.yaml:/w/hello.yaml -w /w \
  areteacademy/brevis:0.40.0-worker run hello.yaml
```

输出：

```text
workflow hello (dag, 4 steps) in .
  ▶ prepare
    prepare | preparing
  ✓ prepare
  ▶ validate
  ▶ extract
    validate | validated
  ✓ validate
    extract | 42 extracted
  ✓ extract
  ▶ publish
    publish | published
  ✓ publish

workflow hello finished
```

这是来自已发布镜像的真实输出。brevis run 在本地执行，不经过队列；pod 由调度器启动。

## 接下来去哪里

| 如果你想 | 前往 |
|---|---|
| 完整的 YAML 格式 | [Workflows](/zh/docs/workflows/index.md) |
| 谁创建 run、谁执行 run | [调度器与队列](/zh/docs/scheduler-and-queue/index.md) |
| 为什么每个 step 一个 pod | [每个 step 一个 pod](/zh/docs/pod-per-step/index.md) |
| step 向下一个 step 传递什么 | [step 之间的上下文](/zh/docs/context/index.md) |
| 全部启动并在界面中看到图 | [快速开始](/zh/docs/quickstart/index.md) |
