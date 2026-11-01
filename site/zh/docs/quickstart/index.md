# 快速开始

> 从零开始，四步在界面中看到一个运行中的 DAG。

*https://brevis.sh/zh/docs/quickstart/ · brevis.sh docs (zh-CN)*

---

本指南会启动 brevis.sh，发布一个 workflow 并运行它。最后你会看到带有每个 step 实时状态的图。

前提：一个可访问的 PostgreSQL，以及 `BREVIS_DATABASE_URL` 变量。

## 1. 应用 schema

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
brevis migrate up
```

## 2. 编写 workflow

创建 `hello.yaml`。这个示例只用 shell，因此可以在任何地方运行：

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

发布之前先检查：这一步不会接触数据库：

```bash
brevis validate hello.yaml
```

```text
  ok    hello                        dag  4 steps, 4 dependencies  (manual)
```

## 3. 立即运行，不经过队列

看到图运行起来的最短路径：

```bash
brevis run hello.yaml
```

```text
workflow hello (dag, 4 steps) in .
  ▶ prepare
    prepare | preparing
  ✓ prepare
  ▶ extract
  ▶ validate
    validate | validated
  ✓ validate
    extract | 42 extracted
  ✓ extract
  ▶ publish
    publish | published
  ✓ publish

workflow hello finished
```

`extract` 和 `validate` 同时启动，因为它们依赖同一个 step。运行器**按层**遍历图：同一层内的所有 step 并行运行，只有上一层完全结束，下一层才会开始。

:::note `run` 天然是本地的
`brevis run` 只在 `BREVIS_ENV=local`（留空也视为 local）时工作，并且把 step 当作进程在本实例上执行，即使 step 声明了 `image:` 也一样。pod 是由 `scheduler` 创建的。
:::

## 4. 发布并运维

这样 workflow 才会存在于数据库中，出现在界面里，并按其调度运行：

```bash
brevis publish hello.yaml
```

```text
  published  hello                      (manual)
```

在两个终端中分别启动界面和调度器：

```bash
brevis serve
```

```bash
brevis scheduler --interval 5s --concurrency 4
```

打开 `http://localhost:8080`，在 workflow 列表中找到 `hello`，点击 ▶。图中会实时显示每个 step 的状态变化。

## 发生了什么

| | |
|---|---|
| `migrate up` | 创建了表 |
| `validate` | 在不接触数据库的情况下检查了 YAML |
| `run` | 在本实例上执行，不经过队列 |
| `publish` | 保存了 workflow 及其调度 |
| `serve` | 启动了 API 和界面 |
| `scheduler` | 按调度生成 run，并消费队列 |

## 下一步

- [Workflows](/zh/docs/workflows/index.md)：完整的 YAML 格式
- [参数](/zh/docs/parameters/index.md)：两次 run 之间可以变化的内容
- [每个 step 一个 pod](/zh/docs/pod-per-step/index.md)：这如何变成 Kubernetes
