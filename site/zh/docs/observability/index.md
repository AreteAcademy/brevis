# 可观测性

> 两个进程的 Prometheus 指标、应该关注什么，以及刻意缺失的内容。

*https://brevis.sh/zh/docs/observability/ · brevis.sh docs (zh-CN)*

---

Brevis 在自己的端口上暴露 Prometheus 指标，**两个**进程都会提供。

```
GET http://<pod>:9090/metrics
```

端口来自 `BREVIS_METRICS_ADDR`。它刻意与 HTTP 端口分开：指标不经过界面的认证，而从同一个地方提供两者，就得在需要认证的抓取与开放的仪表盘之间二选一。

## 两个进程，两个端点

这是大多数部署出错的地方：只抓取了 API。

| | 提供 | 上报 |
|---|---|---|
| `brevis serve` | 界面和 HTTP API | 队列深度 |
| `brevis scheduler` | 不通过 HTTP 提供任何东西 | 队列深度、认领延迟、slot、孤儿、run、step |

执行工作的是调度器，所以几乎所有指标都来自它。只基于 API 搭建的仪表盘会显示队列在增长，却**完全看不到**是什么在消费它。

## step 发布的指标

除了引擎度量的内容之外，step 还可以发布自己的指标，比如有多少行落地、供应商拒绝了多少行：

```python
from brevis import metrics
metrics.set("rows_loaded", 48213)
```

它们出现在**同一个**调度器 `/metrics` 上，由引擎加上前缀和标签：

```
brevis_step_rows_loaded{workflow="daily_sales",step="load"} 48213
```

step 不会开启端口：它向 stdout 写一行，由引擎记录，因为只存活四十秒的 pod 无法被抓取。见 [Python](/zh/docs/python/index.md)。

## 应该关注什么

| 信号 | 原因 |
|---|---|
| 队列深度只升不降 | 调度器挂了，或者 `--concurrency` 对当前负载来说太低 |
| 认领延迟在增长 | 数据库存在争用，或者队列对该间隔来说太大 |
| 孤儿 > 0 | run 被一个在完成之前就死掉的进程认领了 |
| slot 没有生成 | 调度器循环停止了，调度也随之停止 |

仅凭前两项，无法区分“高负载”与“进程已死”，这就是 slot 重要的原因。队列上升**同时**有 slot 在创建，是负载；队列上升而**没有** slot，则是循环停止了。

## Kubernetes

```yaml
env:
  - name: BREVIS_METRICS_ADDR
    value: ":9090"
ports:
  - name: metrics
    containerPort: 9090
```

使用 Prometheus Operator 时，每个角色一个 `PodMonitor`，而且确实需要每个角色一个，因为两个 Deployment 的标签不同，上报的指标集也不同。

## 刻意缺失的内容

**Trace。** 目前还没有 OpenTelemetry。step 作为自己的 pod 运行，关于它的重要信息（开始、结束、尝试、退出码、日志）已经在数据库中，也显示在界面上。真正有用的 trace 会在 step 内部，而那属于 step 的代码，不属于编排器。

只要 step 是最小单位，这一点就成立。等到某天 step 调用了另一个服务，问题变成“这 40 秒花在哪里了”，trace 就开始值得付出它的成本了。

## 下一步

- [Kubernetes](/zh/docs/kubernetes/index.md)：部署两个进程
- [配置](/zh/docs/configuration/index.md)：`BREVIS_METRICS_ADDR` 及其他变量
