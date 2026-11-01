---
title: Scheduler und Queue
description: Zwei unabhängige Schleifen – wer Runs erzeugt, wer sie ausführt und warum sie getrennt sind.
group: Konzepte
order: 5
slug: scheduler-and-queue
---

brevis.sh trennt das **Erzeugen** eines Runs von seiner **Ausführung**. Zwei
Schleifen laufen zusammen und hängen von keiner der beiden ab.

```
scheduler                          queue
─────────                          ─────
reads the schedule
materialises the slot
creates the Run  ─────────────►  (pending in the database)
                                   claims it
                                   walks the graph
                                   executes each step
                                   records state and log
```

## Warum getrennt

Eine einzelne Schleife, die plant und ausführt, hat eine schlechte Eigenschaft:
Wenn die Ausführung hängt, bleibt die Planung mit stehen. Die Slots der
nächsten zwölf Stunden existieren schlicht nicht, und niemand merkt es, bis
jemand nach Daten sucht, die nie ankamen.

Mit der Trennung gilt:

- der **Scheduler** kann ausfallen, und die Queue arbeitet weiter ab, was
  erzeugt wurde;
- die **Queue** kann ausfallen, und Slots werden weiter materialisiert, um nach
  ihrer Rückkehr ausgeführt zu werden;
- **Neuverarbeitung hängt nicht von der Uhr ab** – `backfill` erzeugt
  vergangene Runs über denselben Weg, den auch cron nähme.

## Der Prozess

Beide Schleifen leben im selben Befehl:

```bash
brevis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| Flag | Standard | |
|---|---|---|
| `--interval` | `10s` | Intervall zwischen den Zyklen des Schedulers |
| `--concurrency` | `5` | gleichzeitige **Runs** |
| `--max-pods` | `5` | gleichzeitige **Steps** insgesamt |

`--concurrency` und `--max-pods` zählen bewusst Verschiedenes: Fünf Runs mit je
drei parallelen Steps wären fünfzehn Pods, läge die einzige Grenze bei den Runs.

:::note `serve` materialisiert keine Zeitpläne
Der API-Prozess startet denselben Scheduler, aber **ohne die Schleife** – er ist
nur dazu da, manuelle Auslöser aus der Oberfläche zu bedienen, sodass die Regel
„der Scheduler erzeugt Runs“ einen einzigen Besitzer behält. Damit Zeitpläne
feuern, muss daneben ein `brevis scheduler` laufen.
:::

## Der Definitions-Snapshot

Wenn der Scheduler einen Run erzeugt, speichert er die **Workflow-Definition
innerhalb des Runs**. Die Ausführung liest diesen Snapshot, nicht das YAML auf
der Platte.

Der Grund ist konkret: Zwischen Auslösung und Ausführung kann jemand die Datei
bearbeitet und deployt haben. Ohne den Snapshot würde ein um 05:00 mit einer
Definition erzeugter Run um 05:02 mit einer anderen ausgeführt – und das Log
hätte keine Möglichkeit, den Unterschied zu erklären.

## Retries

Ein fehlgeschlagener Run wird **dreimal** versucht, und die Wartezeit
**verdoppelt** sich: Mit den Standardwerten liegen die Versuche bei **0s, 30s
und 1m30s**.

```bash
brevis scheduler --max-attempts 3 --retry-backoff 30s --retry-backoff-max 1h
```

| Flag | Standard | |
|---|---|---|
| `--max-attempts` | `3` | Versuche pro Run, den ersten eingerechnet |
| `--retry-backoff` | `30s` | die erste Wartezeit, bei jedem weiteren Versuch verdoppelt |
| `--retry-backoff-max` | `1h` | Obergrenze für diese Wartezeit |

Der Prozess gibt den Zeitplan beim Start aus, sodass er nie aus zwei Zahlen
errechnet werden muss:

```
scheduler and dispatcher are up  max_attempts=3 retry_backoff=30s retry_at="0s, 30s, 1m30s"
```

:::tip
**Ein Anbieter mit Rate-Limit will länger.** Diese Pipelines scheitern vor allem
aus einem Grund: einem vorübergehenden Upstream – eine API, die mit 200 und „Sie
haben Ihr Anfragelimit erreicht“ antwortet, ein kurzer Ausschlag bei der
Warehouse-Quote. Das braucht etwa eine Minute, bis es sich löst, und ein Retry,
der vorher endet, ist ein Retry, der nie stattgefunden hat. `--retry-backoff 1m`
legt die Versuche auf 0s, 1m und 3m.
:::

Der Retry gilt pro **Run** und führt nur die Steps erneut aus, die fehlgeschlagen
sind – ein fehlgeschlagenes `dbt_build` wiederholt nicht den darüberliegenden
Fetch, der erfolgreich war.

Der Versuch wird persistiert, er ist kein Prozesszustand. Ein Neustart des
Workers löscht nicht, was über diesen Run bereits bekannt war.

Die Versuchsnummer des **Runs** geht in den Pod-Namen ein. Ohne sie würde ein
Retry den Run von null neu starten, auf den Pod des vorherigen Versuchs unter
demselben Namen treffen und für immer in `Pending` hängen.

## Backfill

Materialisiert vergangene Slots eines bereits veröffentlichten Workflows:

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

```
  31 backfill run(s) queued for daily (2026-01-01 to 2026-01-31)
  run `brevis scheduler` to execute them
```

Er **reiht ein, er führt nicht aus.** Die Ausführung ist Aufgabe des
`scheduler` – wieder dieselbe Trennung. `--to` schließt den ganzen Tag ein.

## Von einem Landing gestartet

Ein Workflow kann laufen, weil Daten eingetroffen sind, statt weil die Uhr es
sagte:

```yaml
trigger:
  on_landed:
    - bigquery://acme-prod/bronze/orders
    - bigquery://acme-prod/bronze/*
  debounce: 5m
```

Jeder Step deklariert bereits, was er geschrieben hat, auf der Leitung, die die
Phasen trägt – das ist es, was `/data` füllt. Ein Workflow abonniert einen dieser
Namen und startet, wenn dort etwas landet.

Ein abschließendes `/*` abonniert das gesamte Dataset. Es existiert für
`auto_table`, das eine Tabelle pro Route anlegt: Das Dataset ist das Einzige,
was sich für Tabellen nennen lässt, die niemand deklariert hat. Es passt auf
`bronze/orders` und nicht auf `bronze_raw/orders`.

### Das Fenster, und warum zehn Landings ein Run sind

`debounce` fasst einen Schwall zusammen. Zehn Tabellen, die innerhalb von fünf
Minuten landen, starten **einen** Run, nicht zehn, und der Run sagt, welche:

```
BREVIS_AUTO_LANDED=bigquery://acme-prod/bronze/orders,bigquery://acme-prod/bronze/items
```

Die genaue Liste steht auch als JSON in `BREVIS_AUTO_PARAMS`, und das ist zu
lesen, wenn ein Ziel ein Komma enthalten könnte.

Das Fenster wird ab dem Zeitpunkt geschnitten, **zu dem die Engine das Landing
festgehalten hat**, nicht ab dem, zu dem der Step sagte, es sei geschehen. Ein
Step, der den Moment meldet, in dem seine Abfrage begann, meldet etwas
Wahres und etwas Altes, und eine einzige verstellte Uhr würde sonst einen
Schwall in zwei Runs teilen.

Lassen Sie `debounce` weg, startet jedes Landing einen Run.

### Was es nicht tun wird

**Ein Workflow wird nie durch seinen eigenen Schreibvorgang gestartet.** Die
Engine verwirft ein Landing, dessen Schreiber der abonnierende Workflow ist, und
protokolliert den Grund – ohne das würde ein Workflow, der dieselbe Tabelle
liest und schreibt, sich ewig selbst auslösen.

Es wird verweigert, **wenn es feuert**, und nicht, wenn die Datei geprüft wird,
und das ist eine Grenze, kein Versehen: `brevis validate` liest Dateien, und
keine Datei deklariert, was ein Step schreibt. Ein Warehouse-Ziel bestimmt die
Verbindung, nicht das YAML. `validate` lässt also einen Workflow durch, der zur
Laufzeit jedes Mal verweigert wird, mit dem Grund im Log.

**Der erste Zyklus startet nichts.** Ein Trigger beginnt in dem Moment zu
zählen, in dem er aktiv wird, wie ein Zeitplan – sonst würde das Veröffentlichen
eines Triggers einen Run für jedes Landing in der Historie des Katalogs feuern.

## Alarme

```bash
export BREVIS_SLACK_WEBHOOK='https://hooks.slack.com/services/...'
export BREVIS_UI_URL='https://brevis.example.com'
```

Ohne den Webhook warnt der Prozess beim Start, dass Fehler nicht gemeldet
werden. Eine Installation, die still scheitert, wird tendenziell vom Kunden
entdeckt, nicht vom Team.

`BREVIS_UI_URL` baut den Run-Link innerhalb des Alarms – ohne ihn sagt der Alarm,
was fehlgeschlagen ist, lässt den Leser aber den Run von Hand suchen.

## Nächste Schritte

- [Pod pro Step](/docs/pod-per-step/) – wo jeder Step tatsächlich läuft
- [CLI](/docs/cli/) – jedes Flag von `scheduler` und `backfill`
