# ステップのランタイム

> ステップが、使う言語と操作するツールについて宣言するもの — そして、ほとんど宣言が要らない理由。

*https://brevis.sh/ja/docs/runtime/ · brevis.sh docs (ja)*

---

グラフは、各ステップに、その言語と操作するツールを示すチップを描画します。

```
┌──────────────────────────────────────┐
│ ● transform            SDK v0.57.0   │
│   dbt build --select gold            │
│   ⬤ Python  ▢ dbt                    │
│   2m14s · attempt 1                  │
└──────────────────────────────────────┘
```

**たいていの場合、何も書く必要はありません。** エンジンが `run:` と `image:` を読んで
判断します。

## いつ宣言するか

フィールドは 2 つで、どちらも任意で、ステップごとに指定します。

```yaml
steps:
  - id: fetch
    run: /opt/brevis/bin/fetch-weather
    runtime: go            # a compiled binary; the path says nothing

  - id: transform
    run: dbt build --select gold+
    tools: [dbt]           # what it drives, not what it is
```

| フィールド | |
|---|---|
| `runtime` | ステップの言語: `go`、`python`、`node`、`shell`… |
| `tools` | コマンドが操作するツール: `dbt`、`dlt`、`spark`… |

経験則は、**推論が間違うときに宣言する**ことです。コンパイル済みのバイナリを指す `run:` には、
それが Go だと伝える手段がありません。内部で `dbt` を呼び出すラッパーには、dbt を操作していると
伝える手段がありません。

## なぜ閉じた語彙なのか

語彙にない id は **publish で拒否され**、有効なものが示されます。代案は、3 日後の画面で空白の
まま描画され、原因をたどる手がかりがないチップです。

```
workflow "daily": step "transform": `runtime: pyton` is not valid
  (valid: go, python, node, shell, …)
```

## チップは何のためにあるか

飾りではありません。午前 3 時に run が失敗したとき、最初の疑問は「このステップは何を実行して
いるのか?」であり、その答えはたいてい別のリポジトリにあります。チップはそれをグラフ上に
示します。ステップが使った SDK のバージョンと並べて表示されるため、「今日壊れた」と
「火曜日のバージョンアップから壊れている」を区別できます。

## 次のステップ

- [Workflows](/ja/docs/workflows/index.md) — ステップのすべてのフィールド
- [ステップごとの pod](/ja/docs/pod-per-step/index.md) — ステップが実際に動く場所
- [オブザーバビリティ](/ja/docs/observability/index.md) — それに付随するメトリクス
