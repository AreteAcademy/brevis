---
title: ステップごとの pod
description: なぜ各ステップが専用の pod で動くのか、そしてそれがコストと分離にどう影響するのか。
group: 基本概念
order: 6
slug: pod-per-step
---

Kubernetes では、**ワークフローの各ステップが pod になり**、そのステップで宣言された
イメージを使います。仕事を待つ汎用ワーカーは存在せず、仕事が自分のランタイムを持ち込みます。

![図: Core がステップごとに 1 つの pod を起動します — Go 上の extract、Python 上の prepare、Go 上の load、dbt 上の transform。ステップ同士はコンテキストを受け渡し、それを読み書きするのが SDK です。](/assets/execution.svg)

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

## 何が変わるか

モノリシックなワーカーでは、どのステップが必要とするかもしれないものをすべて、イメージに
入れなければなりません。実際には次のようになります。

| | 単一イメージ | ステップごとの pod |
|---|---|---|
| dbt のステップ | 1.9 GB · 1Gi RAM | 1.9 GB · 1Gi RAM |
| その隣の Go フェッチャー | **1.9 GB · 1Gi** | **12 MB · 32Mi** |
| dbt のバージョンを上げる | すべてを再ビルド | そのワークフローの YAML の 1 行 |
| メモリリークするステップ | ワーカーと隣のステップごと落ちる | 単独で落ちる |

3 行目は最もわかりにくく、日々のコストとしては最も高くつきます。単一イメージでは、
1 つのパイプラインで dbt を 1.10 から 1.11 に上げると、すべてのパイプラインで上げることに
なります。

## ロールごとのイメージ

イメージは、プロジェクトごとではなく**ロール**ごとです。イメージに余分に残ったものは、
そのステップを実行するすべてのノードが取得します。

| ロール | サイズ | コールドスタート |
|---|---|---|
| **Go** | **5.8 MB** | 0.18 s |
| **Python** | **118 MB** | 0.54 s |
| **dbt** | **620 MB** | 3.3 s |

以前は、すべてに対して 1.87 GB の単一イメージでした。

dbt では、ビルド時に `dbt parse` が実行され、`target/partial_parse.msgpack` がイメージに
含まれます。これにより、呼び出しのたびに、pod あたり **2.68 秒短縮**されます。

## 各ステップが動く場所

この判断は起動時に一度だけ行われ、`BREVIS_PODS` とクラスタの有無によって決まります。

| `BREVIS_PODS` | クラスタあり | クラスタなし |
|---|---|---|
| `auto`(デフォルト) | `image:` のあるステップは pod になる | すべてローカルプロセス。ログに警告が出る |
| `on` | `image:` のあるステップは pod になる | **起動エラー** |
| `off` | すべてローカルプロセス | すべてローカルプロセス |

**`host:` のあるステップは、このどれも通りません。** pod より先、プロセスより先に判定され、
すでに動いているマシンに送られます。`BREVIS_PODS` は適用されず、ローカルプロセスに
フォールバックすることも決してありません。これは意図的です。`image:` が警告付きでローカル実行に
縮退するのは、コンテナとプロセスが、同じコマンドを 2 通りに包んだものだからです。ホストは違います。
ライセンスがあり、GPU があり、データがある場所です。

`auto` がデフォルトなのは、同じバイナリが両方の場所で動くからです。ノート PC ではサービスアカウントが
マウントされておらずローカルプロセスにフォールバックし、クラスタではマウントされていて
pod の作成を始めます。

`on` は、黙ってローカル実行になっては**ならない**デプロイのためにあります。そこでは、
クラスタがないことは起動エラーでなければなりません。

:::warning `brevis run` は pod を作りません
`run` コマンドが組み立てるのはプロセスのエグゼキュータだけです。`image:` のあるステップは
インスタンス自身で実行され、その旨が表示されます。黙っていると、宣言したイメージで
ステップが動いたように見えてしまいます。それは、結果がすでに間違っているときにしか
表面化しない類のミスです。
:::

## ID と認証情報

pod がどの ID で起動するかは、ワークフローではなく**インストール**の判断です。パイプラインが
自分の実行するサービスアカウントを選べてはなりません。そのため、環境から取得します。

```bash
BREVIS_POD_NAMESPACE=data
BREVIS_POD_SERVICE_ACCOUNT=brevis-runner
BREVIS_POD_PULL_SECRETS=registry-cred
BREVIS_POD_ENV_FROM_SECRETS=bigquery-cred,api-tokens
```

pod モードでは、タスクの環境はクラスタの Secret から取得され、`BREVIS_TASK_ENV` からは
取得されません。

## ノードセレクタと toleration

```bash
BREVIS_POD_NODE_SELECTOR='kubernetes.io/arch=arm64'
BREVIS_POD_TOLERATIONS='dedicated=data:NoSchedule'
```

toleration では `Equal` 演算子だけが受け付けられます。`Exists` はそのキーを持つ**あらゆる**
taint を許容してしまい、環境変数から来る判断としては広すぎます。

不正な形式の toleration は、起動エラーにはせず**無視されます**。pod は `Pending` のままになり、
それは目に見えます。一方、起動を拒否すると、そのプールを必要としないワークフローまで
止まってしまいます。

## 失敗のデバッグ

```bash
BREVIS_POD_KEEP_ON_FAILURE=true
```

失敗した pod を残し、`kubectl logs` と `kubectl describe` で調べられるようにします。
本番ではオフにしておいてください。停止した pod はクラスタのクォータを消費します。

## 次のステップ

- [Kubernetes](/docs/kubernetes/) — デプロイの全体
- [設定](/docs/configuration/) — すべての `BREVIS_POD_*` 変数
- [オブザーバビリティ](/docs/observability/) — 両方のプロセスのメトリクス
