# 简介

> brevis.sh 是什么，它解决什么问题，以及它在什么情况下不是合适的选择。

*https://brevis.sh/zh/docs/introduction/ · brevis.sh docs (zh-CN)*

---

brevis.sh 是一个**用 Go 编写的数据编排运行时**。一个二进制文件就集成了通常需要四种工具才能完成的工作：声明式转换、workflow 编排、持久化队列、调度器，以及用于操作这一切的界面。

它以 MIT 许可证开源，项目诞生于 [Aretê Academy](https://areteacademy.com.br/)。

## 问题

常见的数据技术栈由转换工具、编排器、队列和界面组合而成。每一块都擅长自己的事，但它们之间的集成却成了永久性的工作：四套版本方案、四种配置方式、出错时要查看四个地方。

这项成本从不出现在架构图上，而是体现在团队的时间里：他们花在维护胶水代码上的时间，本可以用来处理只有他们才懂的业务领域。

## 方案

一个制品，按角色需要选择对应的子命令：

```bash
brevis serve        # API + interface
brevis scheduler    # materialises schedules and drains the queue
brevis run wf.yaml  # runs now, no database
```

workflow 是一个受版本控制的 YAML 文件。评审一条 pipeline，就变成了评审一份 diff，而不是一场点击操作。

```yaml
name: ingest
schedule: "0 5 * * *"

steps:
  - id: bronze
    image: registry.example/dbt:1.10.3
    run: dbt build --select bronze+

  - id: notify
    image: ghcr.io/example/notify:0.3
    run: /notify --channel data
    depends_on: [bronze]
```

## 它的不同之处

**每个 step 都在自己的 pod 中运行，使用自己的镜像。** 没有等待任务的通用 worker：任务自带运行时。`dbt` step 用 dbt 镜像启动；旁边的 Go 抓取器只占 5.8 MB 和 32Mi 内存，而不会继承最大邻居的体积。

**调度器创建 run，队列执行 run。** 两个相互独立的循环，这种分离是刻意的：其中一个宕机不会打断另一个，重新处理过去一个月的数据也不依赖时钟。

**校验不需要数据库。** `brevis validate` 可以在 CI 中与测试一起运行，因此 YAML 错误会在 pull request 中失败，而不是在集群里失败。

## 三个部分，刻意分开

| 组成部分 | 它是什么 | 版本 |
|---|---|---|
| **engine** | `brevis`，本页所讲的二进制文件：负责编排、调度、执行和运维 | `0.15` |
| **SDK** | 一个 Go 模块，用来编写负责抽取和加载的 step | `0.66` |
| **gateway** | 一个 HTTP 端点，用来接收*主动到达*的数据，而不是去抓取数据 | `0.8` |

版本相互独立，因为成熟度本来就不同。仅仅因为 engine 在那里，就把 gateway 编号为 `0.15.3`，等于宣称了它并不具备的成熟度，而已发布的版本无法撤回。

把这三者缝合在一起的是一个数字：`ingestion_id`，即对 `provider|entity|source_key|record_ts` 计算的固定 UUID v5。同一条记录，由 [gateway](/zh/docs/ingestion/index.md) 落地的行与由 [SDK](/zh/docs/sdk/index.md) 抓取器落地的行是*同一行*，两者之间无需对账。

## 什么时候 brevis.sh 不是合适的选择

记录一个工具不做什么，比记录它做什么更能节省时间：

- **你已经在运行 Airflow 或 Dagster，并且用得很满意。** 迁移的成本高于少运维一个二进制文件所节省的成本。
- **你的 workflow 与数据无关。** brevis.sh 假定处理的是转换和加载的 DAG；通用编排器更适合你。
- **你需要一套现成的 operator 目录。** 这里的 step 就是一个镜像加一条命令。这很简单，也有局限：没有成百上千的集成可以直接拿来用。
- **你的团队不使用 Kubernetes，也不打算使用。** 本地模式可以工作，但设计是以 pod 为前提的。

## 开放的代码，自选参与方式。

brevis.sh 是 100% 开源的。没有专有许可证，没有受限版本，也不会限制你构建的东西。

### 100% 开放

*代码 · 开源许可证*

阅读、使用、改造并改进运行时，用在你自己的业务中。完整的运行时 · 可审计的代码 · 开放的贡献。

[查看仓库](https://github.com/AreteAcademy/brevis)

### 共同构建

*社区 · 公开的 issue* · **主要途径**

参与决定项目走向的讨论。Issue 与提案 · 同行交流 · 公开的路线图。

[关注项目](https://github.com/AreteAcademy/brevis/issues)

### 深入学习

*aretê · 有目标的技术*

了解这所将哲学思考与软件工程相结合的学校。原创内容 · 持续学习 · 开放的渠道。

[访问 Aretê](https://areteacademy.com.br/)

## 接下来去哪里

| 如果你想 | 前往 |
|---|---|
| 安装并看到它运行 | [安装](/zh/docs/installation/index.md) 和[快速开始](/zh/docs/quickstart/index.md) |
| 了解 workflow 格式 | [Workflows](/zh/docs/workflows/index.md) |
| 了解架构 | [调度器与队列](/zh/docs/scheduler-and-queue/index.md) |
| 查看命令和标志列表 | [CLI](/zh/docs/cli/index.md) |
| 用 Go 编写抓取器 | [SDK](/zh/docs/sdk/index.md) |
| 把一个值从一个 step 传给下一个 | [step 之间的上下文](/zh/docs/context/index.md) |
| 用 Python 编写 step | [客户端库](/zh/docs/libraries/index.md) |
| 通过 HTTP 接收数据，而不是去抓取 | [摄取](/zh/docs/ingestion/index.md) |
| 构建仪表盘和告警 | [可观测性](/zh/docs/observability/index.md) |

:::note 关于名字
*Brevis* 在拉丁语中是“短、简短”的意思，也是英文 *brevity* 的词根。它出自塞内卡的 **De Brevitate Vitae**：不是我们的时间太少，而是我们浪费了太多。
:::
