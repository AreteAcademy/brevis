---
title: Step-Laufzeitumgebung
description: Was ein Step über die Sprache und die Werkzeuge deklariert, die er ansteuert – und warum Sie es selten müssen.
group: Referenz
order: 11
slug: runtime
---

Der Graph zeichnet an jeden Step einen Chip, der seine Sprache und die
Werkzeuge nennt, die er ansteuert:

```
┌──────────────────────────────────────┐
│ ● transform            SDK v0.57.0   │
│   dbt build --select gold            │
│   ⬤ Python  ▢ dbt                    │
│   2m14s · attempt 1                  │
└──────────────────────────────────────┘
```

**Meistens schreiben Sie nichts.** Die Engine liest `run:` und `image:` und
leitet es ab.

## Wann Sie es deklarieren

Zwei Felder, beide optional, beide pro Step:

```yaml
steps:
  - id: fetch
    run: /opt/brevis/bin/fetch-weather
    runtime: go            # a compiled binary; the path says nothing

  - id: transform
    run: dbt build --select gold+
    tools: [dbt]           # what it drives, not what it is
```

| Feld | |
|---|---|
| `runtime` | die Sprache des Steps: `go`, `python`, `node`, `shell` … |
| `tools` | die Werkzeuge, die der Befehl ansteuert: `dbt`, `dlt`, `spark` … |

Die Faustregel: **Deklarieren Sie es, wenn die Ableitung danebenliegt.** Ein
`run:`, das auf ein kompiliertes Binary zeigt, kann nicht sagen, dass es Go ist;
ein Wrapper, der intern `dbt` aufruft, kann nicht sagen, dass er dbt ansteuert.

## Warum ein geschlossenes Vokabular

Eine Id außerhalb des Vokabulars wird **beim Veröffentlichen verweigert**, wobei
genannt wird, was gültig ist. Die Alternative ist ein Chip, der drei Tage später
leer auf einem Bildschirm erscheint, ohne dass sich etwas zurückverfolgen ließe.

```
workflow "daily": step "transform": `runtime: pyton` is not valid
  (valid: go, python, node, shell, …)
```

## Wozu der Chip da ist

Er ist keine Dekoration. Wenn ein Run um 3 Uhr nachts fehlschlägt, lautet die
erste Frage „Was führt dieser Step aus?“, und die Antwort liegt meist in einem
anderen Repository. Der Chip bringt sie in den Graphen selbst – neben die
SDK-Version, die der Step verwendet hat, die „heute kaputtgegangen“ von „kaputt
seit dem Update am Dienstag“ unterscheidet.

## Nächste Schritte

- [Workflows](/docs/workflows/) – jedes Step-Feld
- [Pod pro Step](/docs/pod-per-step/) – wo ein Step tatsächlich läuft
- [Observability](/docs/observability/) – die zugehörigen Metriken
