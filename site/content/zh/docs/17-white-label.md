---
title: 白标
description: 通过一个 YAML 文件修改界面的标题、格言、logo 和配色。
group: 运维
order: 17
slug: white-label
---

界面可以通过一个文件来定制。颜色会在**运行时**覆盖 CSS 变量，所以更换客户的主题不需要重新编译任何东西。

## 文件

把 `brand.example.yaml` 复制为根目录下的 `brand.yaml`，或者让 `BREVIS_BRAND_FILE` 指向它：

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

**每个字段都是可选的。** 省略的字段保持默认值，所以只含 `titulo:` 的文件已经是有效的。

`frase` 中的换行会被保留：它决定了侧边栏中这句话的节奏。

## Logo

```yaml
logo: https://example.com/mark.svg
```

接受 URL（`https://` 或 `http://`）或以 `/` 开头的内部路径。省略时使用内置符号，它会跟随主题颜色。

:::warning 托管在别处的 logo 依赖那个主机
如果它宕机，或者集群没有出站互联网访问，图片就无法加载。界面的其余部分仍会正常工作，但选择时请权衡这一点：界面的其余部分（包括字体和资源包）都是由二进制文件自己提供的，正是为了不依赖网络。
:::

## 校验

写错的十六进制值只有在容器启动时才会暴露，而且错误信息会通过 pod 日志到达，离编辑文件的人很远。因此有了一个子命令：

```bash
brevis marca brand.yaml
```

```
  ok    Acme Data · Data platform
        logo      /assets/logo.svg  (built-in symbol)
        accent    (the product's own)
        Powered by Brevis
```

在安装环境的 CI 中调用它，错误就会在 pull request 中返回。

:::note 缺失在不同地方含义不同
在 `serve` 中，文件缺失表示“使用默认标识”，API 照常启动。在 `marca` 中，文件缺失是**错误**：要求校验某个路径的人，期望被告知它不存在。
:::

## 启动时的行为

内容错误（无效的颜色、损坏的 YAML）**不会阻止界面启动**。进程会记录一条警告并使用默认主题。因为一个颜色而让 API 下线，比用默认主题提供服务更糟糕。

```
level=WARN msg="visual identity ignored" file=brand.yaml error="brand.yaml: colour accent: \"#gggggg\" is not a hex value (#rgb, #rrggbb or #rrggbbaa)"
```

## 无法定制的内容

页脚中的 **“Powered by Brevis”** 这一行不来自配置，而来自代码。

## 在 Kubernetes 上

从 ConfigMap 挂载该文件：

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
