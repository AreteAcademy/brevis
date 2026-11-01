---
title: 每个 step 一个 pod
description: 为什么每个 step 都在自己的 pod 中运行，以及这在成本和隔离方面带来了什么变化。
group: 概念
order: 6
slug: pod-per-step
---

在 Kubernetes 上，**workflow 的每个 step 都变成一个 pod**，使用该 step 中声明的镜像。没有等待任务的通用 worker：任务自带运行时。

![示意图：Core 为每个 step 启动一个 pod，即基于 Go 的 extract、基于 Python 的 prepare、基于 Go 的 load 和基于 dbt 的 transform。step 之间相互传递 context，读写 context 的是 SDK。](/assets/execution.svg)

```
scheduler                          cluster
─────────                          ───────
walks the graph
  for each ready step:
    builds the Pod  ───────────►  Pod: the step's image, the step's command
    follows status      ◄───────  Pending → Running → Succeeded/Failed
    follows the log     ◄───────  container stdout
    reads the exit code ◄───────  containerStatuses[0].terminated
    deletes the pod ───────────►  (gone)
```

## 这带来了什么变化

单体 worker 迫使镜像包含任何 step 可能需要的一切。实际情况是：

| | 单一镜像 | 每个 step 一个 pod |
|---|---|---|
| dbt step | 1.9 GB · 1Gi RAM | 1.9 GB · 1Gi RAM |
| 与它并排的 Go 抓取器 | **1.9 GB · 1Gi** | **12 MB · 32Mi** |
| 升级 dbt 版本 | 全部重新构建 | 该 workflow YAML 中的一行 |
| 内存泄漏的 step | 把 worker 及其邻居一起拖垮 | 只有自己死掉 |

第三行最不显眼，却是日常中代价最高的：只有一个镜像时，在某条 pipeline 中把 dbt 从 1.10 升级到 1.11，就迫使所有 pipeline 一起升级。

## 按角色划分的镜像

镜像按**角色**划分，而不是按项目。镜像中多余的一切，都会被每个运行该 step 的节点拉取：

| 角色 | 大小 | 冷启动 |
|---|---|---|
| **Go** | **5.8 MB** | 0.18 秒 |
| **Python** | **118 MB** | 0.54 秒 |
| **dbt** | **620 MB** | 3.3 秒 |

之前：所有东西共用一个 1.87 GB 的镜像。

对于 dbt，`dbt parse` 在构建时运行，`target/partial_parse.msgpack` 随镜像一起分发，每次调用**每个 pod 少用 2.68 秒**。

## 每个 step 在哪里运行

这个决定在启动时做出一次，取决于 `BREVIS_PODS` 以及是否存在集群：

| `BREVIS_PODS` | 有集群 | 无集群 |
|---|---|---|
| `auto`（默认） | 带 `image:` 的 step 变成 pod | 全部作为本地进程运行，并在日志中给出警告 |
| `on` | 带 `image:` 的 step 变成 pod | **启动错误** |
| `off` | 全部作为本地进程运行 | 全部作为本地进程运行 |

**带 `host:` 的 step 不经过上述任何一种。** 它在 pod 和进程之前就被检查，然后被发送到一台已经在运行的机器：`BREVIS_PODS` 对它不适用，它也绝不会回退为本地进程。这是有意为之：`image:` 会带着警告降级为本地执行，因为容器和进程只是同一条命令的两种包装，而主机不是。主机上有许可证，或者 GPU，或者数据。

`auto` 是默认值，因为同一个二进制文件在两种环境中都运行：在笔记本电脑上没有挂载 service account，会回退到本地进程；在集群中有，就开始创建 pod。

`on` 适用于**绝不能**悄悄变成本地执行的部署。在那里，没有集群必须是启动错误。

:::warning `brevis run` 不创建 pod
`run` 命令只构建进程执行器。带 `image:` 的 step 会在实例本身上运行，并会明确说明这一点。保持沉默会让人以为 step 是在声明的镜像上运行的，这种错误只有在结果已经出错之后才会暴露。
:::

## 身份与凭据

pod 以什么身份启动，是**安装**层面的决定，而不是 workflow 层面的：pipeline 不应该能选择自己以哪个 service account 运行。因此它来自环境变量：

```bash
BREVIS_POD_NAMESPACE=data
BREVIS_POD_SERVICE_ACCOUNT=brevis-runner
BREVIS_POD_PULL_SECRETS=registry-cred
BREVIS_POD_ENV_FROM_SECRETS=bigquery-cred,api-tokens
```

在 pod 模式下，任务环境来自集群的 Secret，而不是 `BREVIS_TASK_ENV`。

## 节点选择器与容忍

```bash
BREVIS_POD_NODE_SELECTOR='kubernetes.io/arch=arm64'
BREVIS_POD_TOLERATIONS='dedicated=data:NoSchedule'
```

容忍只接受 `Equal` 运算符：`Exists` 会容忍带有该键的**任何**污点，对于来自环境变量的决定来说范围太宽。

格式错误的容忍会被**忽略**，而不是变成启动错误：这会让 pod 停在 `Pending`，是可见的；而拒绝启动则会连带停止不需要那个节点池的 workflow。

## 调试失败

```bash
BREVIS_POD_KEEP_ON_FAILURE=true
```

保留失败的 pod，以便执行 `kubectl logs` 和 `kubectl describe`。在生产环境中请保持关闭：已停止的 pod 会占用集群配额。

## 下一步

- [Kubernetes](/docs/kubernetes/)：完整的部署
- [配置](/docs/configuration/)：所有 `BREVIS_POD_*` 变量
- [可观测性](/docs/observability/)：两个进程的指标
