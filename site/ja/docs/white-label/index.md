# ホワイトラベル

> 1 つの YAML ファイルで、インターフェースのタイトル、モットー、ロゴ、パレットを変更します。

*https://brevis.sh/ja/docs/white-label/ · brevis.sh docs (ja)*

---

インターフェースは、ファイルでカスタマイズできます。色は CSS 変数を**実行時に**上書きするため、
顧客のテーマを変更しても何も再コンパイルされません。

## ファイル

`brand.example.yaml` をルートの `brand.yaml` にコピーするか、`BREVIS_BRAND_FILE` でそのパスを
指定します。

```yaml
titulo: Acme Data
subtitulo: Data platform

frase: |
  Measure before deciding.
  Decide before scaling.

cores:
  fundo:    "#f4efe4"
  tinta:    "#21180f"
  destaque: "#aa8450"
```

**すべてのフィールドは任意です。** 省略したものはデフォルトのままなので、`titulo:` だけのファイルでも
すでに有効です。

`frase` の改行は保持されます。サイドバーでの文のリズムを決めるものです。

## ロゴ

```yaml
logo: https://example.com/mark.svg
```

URL(`https://` または `http://`)か、`/` で始まる内部パスを受け付けます。指定がなければ、
テーマカラーに従う組み込みのシンボルを使います。

:::warning 外部にホストされたロゴは、そのホストに依存します
そのホストが落ちた場合や、クラスタにインターネットへの外向き通信がない場合、画像は読み込まれません。
インターフェースの残りは動き続けますが、選ぶときにはそれを考慮してください。UI の残りの部分、
フォントやバンドルを含め、ネットワークに依存しないようにバイナリ自身から配信されているからです。
:::

## 検証

誤った 16 進値はコンテナが起動するときにしか表面化せず、そのメッセージは pod のログを通じて届くため、
ファイルを編集した人から遠いところに現れます。そのため、サブコマンドがあります。

```bash
brevis marca brand.yaml
```

```
  ok    Acme Data · Data platform
        logo      /assets/logo.svg  (simbolo embutido)
        destaque  #aa8450
        Powered by Brevis
```

インストール側の CI で呼び出せば、エラーはプルリクエストに返ってきます。

:::note 「ない」ことの意味は異なります
`serve` では、ファイルがないことは「デフォルトのアイデンティティを使う」という意味で、API は
起動します。`marca` では、ファイルがないことは**エラー**です。パスの検証を依頼した人は、
それが存在しないと教えてもらうことを期待するからです。
:::

## 起動時の挙動

内容のエラー(無効な色、壊れた YAML)があっても、インターフェースの**起動は止まりません**。
プロセスは警告をログに出し、デフォルトのテーマを使います。色のために API を止めるのは、
デフォルトのテーマで提供するよりも悪いからです。

```
level=WARN msg="identidade visual ignorada" arquivo=brand.yaml erro="cor invalida: #gggggg"
```

## カスタマイズできないもの

フッターの **「Powered by Brevis」** の行は、設定からではなくコードから来ます。

## Kubernetes では

ファイルを ConfigMap からマウントします。

```yaml
volumes:
  - name: brand
    configMap: {name: brevis-brand}
containers:
  - name: api
    volumeMounts:
      - {name: brand, mountPath: /etc/brevis}
    env:
      - name: BREVIS_BRAND_FILE
        value: /etc/brevis/brand.yaml
```
