# 安装

> 如何获取二进制文件、它有哪些要求，以及如何确认安装正确。

*https://brevis.sh/zh/docs/installation/ · brevis.sh docs (zh-CN)*

---

brevis.sh 以**单个二进制文件**和 **Docker 镜像**的形式发布。除此之外无需安装任何运行时：迁移、界面资源和字体都包含在二进制文件内部。

## 要求

| | |
|---|---|
| **PostgreSQL** | `serve`、`scheduler`、`migrate`、`publish` 和 `backfill` 需要 |
| **Go 1.25+** | 仅在从源码构建时需要 |
| **Kubernetes** | 可选：没有集群时，step 作为本地进程运行 |

`brevis run`、`validate`、`marca`、`hash` 和 `version` **不需要**数据库。

## 从源码构建

这条路径生成的二进制文件会写入版本号和 commit：

```bash
git clone https://github.com/AreteAcademy/brevis.git
cd brevis
make build
./bin/brevis version
```

```
brevis 0.11.2
  commit  bb832ff
  build   2026-09-05T12:00:00Z
  go      go1.25.7 darwin/arm64
```

:::warning 引擎还没有带标签的正式版本
仓库里的每一个标签都属于 SDK（`sdk/v*`），根模块没有任何标签。执行 `go install github.com/AreteAcademy/brevis/cmd/brevis@latest` 会解析到该分支上最新的 commit，生成的二进制文件会自称 `brevis dev`，没有 commit 也没有日期：`-ldflags` 只会通过 `make build` 和 `docker build` 传入。若要得到可追溯的版本，请使用镜像或 `make build`。
:::

## Docker

同一个二进制文件有两个镜像，因为这两个角色的需求相反：

| 标签 | 基础镜像 | 原因 |
|---|---|---|
| `:0.11.2` | distroless | API 只提供 HTTP 服务，不执行任何东西：没有 shell，攻击面最小 |
| `:0.11.2-worker` | alpine + tini | `run:` step 需要 shell |

```bash
docker run --rm brevissh/brevis:latest version
```

两个镜像都在 [hub.docker.com/r/brevissh/brevis](https://hub.docker.com/r/brevissh/brevis)，提供 `amd64` 和 `arm64` 两种架构。

## 完整的本地环境

仓库附带一个 `docker-compose.yml`，可以启动 Postgres、API 和调度器：

```bash
make up      # Postgres + API + scheduler
make smoke   # checks /health and /ready
make logs
make down
```

界面位于 `http://localhost:8080`。

## 最小配置

只有一个变量是必需的：

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
```

缺少它时，使用数据库的子命令会在启动时失败，这是有意为之。没有凭据就启动的进程，要到第一次请求时才会发现问题，而那时就绪探针早已向编排器撒了谎。

```
$ brevis migrate status
erro: BREVIS_DATABASE_URL e obrigatoria
```

完整列表见[配置](/zh/docs/configuration/index.md)。

## 应用 schema

迁移内嵌在二进制文件中，并由专门的子命令应用：**`serve` 绝不会修改 schema**：

```bash
brevis migrate up
brevis migrate status
```

## 确认

```bash
brevis version
brevis validate examples/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencias  cron 0 2 * * *
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

如果两条命令都有响应，说明安装正确。接下来请看[快速开始](/zh/docs/quickstart/index.md)。
