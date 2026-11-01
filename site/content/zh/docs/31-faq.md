---
title: 常见问题
description: 最先被问到的问题，附简短回答和下一步该去哪里。
group: 项目
order: 31
slug: faq
---

## brevis.sh 真的是 100% 开源吗？

是的。完整的运行时都可以阅读、使用、改造和贡献。

## 提到塞内卡对这个项目意味着什么？

De Brevitate Vitae 启发我们采用更有意图的工程方式：减少消耗时间的复杂性，为有目标的工作保留空间。

详见[理念](/docs/philosophy/)。

## pod 隔离是如何工作的？

每个 workflow 的 step 都作为自己的 pod 运行，使用自己的镜像，使依赖和执行上下文彼此分离。

详见[每个 step 一个 pod](/docs/pod-per-step/)。

## 与 Aretê Academy 是什么关系？

brevis.sh 是 Aretê Academy 的项目，该学院让哲学中的伟大思想更贴近构建软件的真实挑战。

## 我可以只使用其中一个组成部分吗？

可以。每个组成部分都有自己的模块、版本和镜像：SDK 是一个可以在任何程序中运行的 Go 库，Gateway 只需一个 YAML 文件即可独立启动，无需数据库，brevis-sql 则作为容器运行。你需要时，Core 可以编排其他几个部分，但它不是其中任何一个的前提条件。

详见[生态](/docs/ecosystem/)。
