# Console

> Core 的 Web 界面：什么正在运行，什么即将运行，以及每个 run 发生了什么。

*https://brevis.sh/zh/docs/console/ · brevis.sh docs (zh-CN)*

---

界面内置于二进制文件中，不需要另外安装和维持一个独立的服务。成功率、平均耗时、队列、尝试历史，以及接下来要运行的调度。

Console 随 `brevis serve` 一起启动，与 HTTP API 使用同一个端口（`BREVIS_HTTP_ADDR`，默认为 `:8080`）。在[快速开始](/zh/docs/quickstart/index.md) 中，它在 `localhost:8080` 打开。

下面的界面是根据 Console 用示例数据以 SVG 重绘的，绝不是截图：真实的 Console 在生产环境中操作的是客户数据。

## 概览

![Brevis Console 的概览界面：344 次运行的成功率为 94.2%，每小时运行次数的柱状图及平均耗时，一个分布环图，进行中的内容，以及接下来的调度运行。](/assets/console.svg)

带有指标和图表的概览。成功率、平均耗时、进行中的内容以及接下来的调度运行，都在同一个界面上。

## Workflows

![workflow 列表：搜索、按状态和标签的筛选，以及十二个 workflow，每个都有暂停开关、cron 表达式、下一次和上一次运行、标签，以及立即运行它的按钮。](/assets/console-workflows.svg)

带有搜索、筛选、暂停和运行的 workflow 列表。每一行都有 cron、下一次和上一次运行，以及标签。

## 单个 workflow

![单个 workflow 的页面：它的四个 step、三个依赖、cron 和标签；带参数运行它的表单；176 次运行的成功率为 96.1%；显示每个 step 状态的 DAG；以及一年的日历，其中每个方块是一天，按当天最差的结果着色。](/assets/console-workflow.svg)

step 和依赖、带参数运行的表单、显示每个 step 状态的 DAG，以及一个年历，其中每一天的颜色取决于当天最差的结果。

## 运行

![运行历史：1,284 次运行，包括状态、workflow、触发方式、所属的 slot、各自耗时以及创建时间。部分运行显示是第几次尝试成功的。](/assets/console-runs.svg)

运行历史，包含每次运行的尝试。状态、workflow、触发方式、该运行所属的 slot、耗时以及创建时间。

## 单次运行

![一个已完成的 run：它的状态、触发方式和 slot；带有每个 step 的命令、镜像和耗时的图；引擎为每个 run 算出的九个自动参数，即用来取代 now() 的时钟、分区日期、这次尝试晚了多久启动以及它所覆盖的窗口；以及每个 step 的输出。](/assets/console-run.svg)

run 的实时 DAG，以及引擎为它算出的自动参数。图中带有每个 step 的命令、镜像和耗时，以及每个 step 的输出。

## 项目

![两个项目 analytics 和 platform，各自为自己的 workflow 提供命名空间：两者都可以有 daily_ingest 而不会冲突。](/assets/console-projects.svg)

项目，这样两个团队不会在同一个名称上冲突。每个项目为自己的 workflow 提供命名空间：两个项目可以各有一个 `daily_ingest`。

## 内置于二进制文件

字体和资源包由二进制文件提供，无需互联网也能工作。
