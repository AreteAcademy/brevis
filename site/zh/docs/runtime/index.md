# step 运行时

> step 对其使用的语言和驱动的工具做了什么声明，以及为什么你很少需要声明。

*https://brevis.sh/zh/docs/runtime/ · brevis.sh docs (zh-CN)*

---

图会在每个 step 上画一个标签，说明它的语言和它所驱动的工具：

```
┌──────────────────────────────────────┐
│ ● transform            SDK v0.57.0   │
│   dbt build --select gold            │
│   ⬤ Python  ▢ dbt                    │
│   2m14s · attempt 1                  │
└──────────────────────────────────────┘
```

**大多数时候你什么都不用写。** 引擎会读取 `run:` 和 `image:`，自己推断出来。

## 何时需要声明

两个字段，都是可选的，都是按 step 设置：

```yaml
steps:
  - id: fetch
    run: /opt/brevis/bin/fetch-weather
    runtime: go            # a compiled binary; the path says nothing

  - id: transform
    run: dbt build --select gold+
    tools: [dbt]           # what it drives, not what it is
```

| 字段 | |
|---|---|
| `runtime` | step 的语言：`go`、`python`、`node`、`shell` 等 |
| `tools` | 命令所驱动的工具：`dbt`、`dlt`、`spark` 等 |

经验法则：**在推断出错时才声明。** 指向已编译二进制文件的 `run:` 无法表明它是 Go；内部调用 `dbt` 的包装脚本也无法表明它驱动的是 dbt。

## 为什么使用封闭的词表

词表之外的 id 会在 **publish 时被拒绝**，并指出有效的取值。另一种做法是，三天后某个界面上渲染出一个空白标签，而无从追查原因。

```
workflow "daily": step "transform": `runtime: pyton` is not valid
  (valid: go, python, node, shell, …)
```

## 这个标签的用途

它不是装饰。当 run 在凌晨三点失败时，首先要问的是“这个 step 执行的是什么？”，而答案通常在另一个仓库里。这个标签把答案直接放在图上，旁边还有该 step 所使用的 SDK 版本，这正是区分“今天坏的”与“从周二升级以来就坏了”的依据。

## 下一步

- [Workflows](/zh/docs/workflows/index.md)：step 的所有字段
- [每个 step 一个 pod](/zh/docs/pod-per-step/index.md)：step 实际在哪里运行
- [可观测性](/zh/docs/observability/index.md)：与之配套的指标
