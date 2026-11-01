# 基准测试

> 我们如何度量 gateway、数字说明了什么，以及必须一致的三个见证。

*https://brevis.sh/zh/docs/benchmarks/ · brevis.sh docs (zh-CN)*

---

一条命令，就能度量整个 gateway：

```bash
BREVIS_BENCH_IMAGE=areteacademy/brevis-gateway:0.11.0 ./bench/run.sh
```

它会启动一个 Postgres，启动 gateway，用 [k6] 对其加压，**等待缓冲区排空**，抓取 gateway 自己的 `/metrics`，并写出 `bench/results/REPORT.md`。

## 三个见证，其中第三个才是关键

k6 知道生产者看到了什么。gateway 知道它自己做了什么。只带有第一个见证的负载测试，无法区分快速的 gateway 与那种返回 `202` 却丢弃了已接受数据的 gateway。

所以报告把两者并排放在一起，并统计 Postgres 中的行数：

| 见证 | 事件数 |
|---|---:|
| k6 被告知 `accepted` | 5,431,000 |
| gateway 统计的已接收数 | 5,431,000 |
| Postgres 中的行数 | 5,431,000 |
| dead letter 中的记录数 | 0 |

前两个都是 gateway 自己的算术：一个来自它写出的响应，一个来自它递增的计数器，而丢失事件的 bug 可以让它们**彼此**保持一致。只有数据库没有相信我们的一面之词。

**三者不一致的运行就是失败的**，无论百分位数多么漂亮。

## 上面的运行

`bench/results/REPORT.md` 中 2026-09-26 的运行：`areteacademy/brevis-gateway:0.11.0`，Darwin arm64，11 个 CPU，同一内核上容器中的 Postgres 17，`auto_table[columns]` 搭配 `write: append`：

| | |
|---|---:|
| 已接受的事件 | **5,431,000**（135,768/秒） |
| 发送的请求体 | 40 秒内 1,156.7 MiB |
| `503`（背压） | 0 |
| p50 / p95 / p99 | 5.9 ms / 16.0 ms / 22.7 ms |
| 已投递的批次 | 1,087，没有被埋入 dead letter 的 |

## 阈值才是重点

任何一个阈值失败时，k6 都会以非零状态退出，所以这是一道**关卡**，而不是一张图表：

| 阈值 | 原因 |
|---|---|
| `p95 < 500ms` | 接受路径必须与 sink 的延迟无关。请求等待 `COPY`，正是异步管道所要对抗的缺陷。 |
| `http_req_failed < 1%` | `503` 没问题；超时或 5xx 则不行。 |
| `events accepted > 1000` | 阈值套件可以在一个什么都没回答的 gateway 上通过。 |

它们刻意设得宽松：这是在 CI 分配给它的任意机器上运行的。它们捕捉的是**一个数量级**，那才是回归的样子，而不是百分之几。

**`503` 不是失败。** 它是 gateway 拒绝一个它无处安放的事件，并且可以安全重试：`ingestion_id` 是事件的固定函数，所以再次发送同样的请求体就是同一条记录。把它算作错误，会让背压看起来像 bug。

## 它不度量什么

- **不是 BigQuery。** 目的地是容器中的 Postgres。数据仓库的加载作业配额、提交延迟和 DDL 并发都不在这些数字里，也不可能在里面。
- **不是持久性。** `durability: memory` 是唯一的级别，所以 `202` 的意思是*已被接受进入 RAM*。这里度量的是这件事发生得有多快，而不是 `SIGKILL` 之后什么能幸存。
- **不是你的硬件。** 请把它读作一种形状和一个比例。

## 按 profile 划分的本地云

```bash
docker compose -f docker-compose.drivers.yml --profile gcp up -d
docker compose -f docker-compose.drivers.yml --profile all up -d
```

一个带 profile 的文件，而不是每个云一个文件：两个文件意味着两个项目名称，而两者中的 `postgres` 已经有一次替换了引擎的开发数据库。

### floci 对 BigQuery 的覆盖比看起来要少

实测，而不是假设：

| | |
|---|---|
| 创建 dataset 和表 | ✅ |
| 保留 `timePartitioning` 和 `clustering` | ✅，即 `0.9.1` 那个 bug 的形态 |
| `PATCH` 一个列 | ✅，即 `auto_table` 的演进 |
| `insertAll` | ✅ |
| 查询 | ✅，仅在挂载了 `/var/run/docker.sock` 时 |
| **加载作业** | ❌ `Only QUERY jobs are supported by the floci BigQuery emulator` |

SDK **只**通过加载作业写入 BigQuery。所以 floci 会运行 `auto_table` 中每一行把载荷变成 DDL 的代码，却**没有运行任何一次 Brevis 写入**。这仍然是目前没有本地测试的那一半，值得拥有。但针对 floci 的测试套件通过，并不意味着 BigQuery 能正常工作。

[k6]: https://k6.io
