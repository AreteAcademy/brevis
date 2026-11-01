---
title: Schnellstart
description: Von null zu einem laufenden DAG in der Oberfläche, in vier Schritten.
group: Erste Schritte
order: 3
slug: quickstart
---

Diese Anleitung fährt brevis.sh hoch, veröffentlicht einen Workflow und führt
ihn aus. Am Ende sehen Sie den Graphen mit dem Live-Zustand jedes Steps.

Voraussetzung: eine erreichbare PostgreSQL und die Variable `BREVIS_DATABASE_URL`.

## 1. Das Schema anwenden

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
brevis migrate up
```

## 2. Einen Workflow schreiben

Legen Sie `hello.yaml` an. Dieses Beispiel nutzt nur die Shell und läuft daher überall:

```yaml
name: hello
type: dag
tags: [example]

steps:
  - id: prepare
    run: sh -c 'echo preparing; sleep 1'

  # The next two depend on the same step, so they run IN PARALLEL.
  - id: extract
    run: sh -c 'sleep 2; echo 42 extracted'
    depends_on: [prepare]

  - id: validate
    run: sh -c 'sleep 1; echo validated'
    depends_on: [prepare]

  - id: publish
    run: echo published
    depends_on: [extract, validate]
```

Prüfen Sie ihn vor dem Veröffentlichen – das berührt die Datenbank nicht:

```bash
brevis validate hello.yaml
```

```text
  ok    hello                        dag  4 steps, 4 dependencies  (manual)
```

## 3. Jetzt ausführen, ohne die Queue

Der kürzeste Weg, den Graphen arbeiten zu sehen:

```bash
brevis run hello.yaml
```

```text
workflow hello (dag, 4 steps) in .
  ▶ prepare
    prepare | preparing
  ✓ prepare
  ▶ extract
  ▶ validate
    validate | validated
  ✓ validate
    extract | 42 extracted
  ✓ extract
  ▶ publish
    publish | published
  ✓ publish

workflow hello finished
```

`extract` und `validate` starten gemeinsam, weil sie vom selben Step abhängen.
Der Runner durchläuft den Graphen **ebenenweise**: Alles innerhalb einer Ebene
läuft parallel, und die nächste Ebene startet erst, wenn die vorherige
vollständig abgeschlossen ist.

:::note `run` ist von Natur aus lokal
`brevis run` arbeitet nur mit `BREVIS_ENV=local` (leer zählt als local) und
führt Steps als Prozesse auf der Instanz selbst aus – auch solche, die
`image:` deklarieren. Pods erzeugt der `scheduler`.
:::

## 4. Veröffentlichen und betreiben

Damit der Workflow in der Datenbank existiert, in der Oberfläche erscheint und
seinem Zeitplan folgt:

```bash
brevis publish hello.yaml
```

```text
  published  hello                      (manual)
```

Starten Sie die Oberfläche und den Scheduler in zwei Terminals:

```bash
brevis serve
```

```bash
brevis scheduler --interval 5s --concurrency 4
```

Öffnen Sie `http://localhost:8080`, suchen Sie `hello` in der Workflow-Liste und
klicken Sie auf ▶. Der Graph zeigt live, wie jeder Step seinen Zustand wechselt.

## Was passiert ist

| | |
|---|---|
| `migrate up` | hat die Tabellen angelegt |
| `validate` | hat das YAML geprüft, ohne die Datenbank zu berühren |
| `run` | wurde auf der Instanz selbst ausgeführt, ohne Queue |
| `publish` | hat den Workflow und seinen Zeitplan gespeichert |
| `serve` | hat API und Oberfläche gestartet |
| `scheduler` | hat den Zeitplan materialisiert und die Queue abgearbeitet |

## Nächste Schritte

- [Workflows](/docs/workflows/) – das vollständige YAML-Format
- [Parameter](/docs/parameters/) – was sich zwischen zwei Runs ändert
- [Pod pro Step](/docs/pod-per-step/) – wie daraus Kubernetes wird
