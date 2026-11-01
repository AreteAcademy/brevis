---
title: インストール
description: バイナリの入手方法、必要なもの、そしてインストールが正しいことの確認方法。
group: はじめに
order: 2
slug: installation
---

brevis.sh は**単一のバイナリ**と **Docker イメージ**として提供されます。一緒にインストールする
ランタイムはありません。マイグレーション、インターフェースのアセット、フォントはすべて
バイナリの中に入っています。

## 要件

| | |
|---|---|
| **PostgreSQL** | `serve`、`scheduler`、`migrate`、`publish`、`backfill` に必要です |
| **Go 1.25+** | ソースからビルドする場合のみ必要です |
| **Kubernetes** | 任意です。クラスタがなければ、ステップはローカルプロセスとして実行されます |

`brevis run`、`validate`、`marca`、`hash`、`version` にデータベースは**不要**です。

## ソースからビルドする

バージョンとコミットが埋め込まれたバイナリを作る方法です。

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

:::warning エンジンにはまだタグ付きのリリースがありません
リポジトリのタグはすべて SDK(`sdk/v*`)のもので、ルートモジュールにはありません。
`go install github.com/AreteAcademy/brevis/cmd/brevis@latest` はブランチの最新コミットに
解決され、`brevis dev` と名乗るバイナリを生成します。コミットも日付もありません。`-ldflags` は
`make build` と `docker build` のときにだけ渡されるためです。追跡可能なバージョンが必要なら、
イメージか `make build` を使ってください。
:::

## Docker

同じバイナリのイメージが 2 つあります。2 つの役割で必要なものが正反対だからです。

| タグ | ベース | 理由 |
|---|---|---|
| `:0.11.2` | distroless | API は HTTP を提供するだけで何も実行しないため、シェルなし、攻撃面は最小です |
| `:0.11.2-worker` | alpine + tini | `run:` のステップにはシェルが必要です |

```bash
docker run --rm areteacademy/brevis:latest version
```

どちらのイメージも [hub.docker.com/r/areteacademy/brevis](https://hub.docker.com/r/areteacademy/brevis) にあり、
`amd64` と `arm64` に対応しています。

## ローカル環境一式

リポジトリには `docker-compose.yml` が含まれており、Postgres、API、スケジューラを起動します。

```bash
make up      # Postgres + API + scheduler
make smoke   # checks /health and /ready
make logs
make down
```

インターフェースは `http://localhost:8080` にあります。

## 最小限の設定

必須の変数は 1 つだけです。

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
```

これがないと、データベースを使うサブコマンドは起動時に失敗します。これは意図的です。認証情報なしで
起動したプロセスは最初のリクエストで初めて問題に気づき、その時点では readiness がすでに
オーケストレータに嘘をついています。

```
$ brevis migrate status
erro: BREVIS_DATABASE_URL e obrigatoria
```

全一覧は[設定](/docs/configuration/)にあります。

## スキーマの適用

マイグレーションはバイナリに埋め込まれており、専用のサブコマンドで適用します。
**`serve` がスキーマを変更することはありません**。

```bash
brevis migrate up
brevis migrate status
```

## 動作確認

```bash
brevis version
brevis validate examples/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencias  cron 0 2 * * *
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

両方が応答すれば、インストールは正しく完了しています。
[クイックスタート](/docs/quickstart/)に進んでください。
