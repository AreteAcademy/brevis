---
title: Benchmarks
description: Wie wir das Gateway messen, was die Zahlen sagen und die drei Zeugen, die übereinstimmen müssen.
group: Ingestion
order: 20
slug: benchmarks
---

Ein Befehl, und er misst das ganze Gateway:

```bash
BREVIS_BENCH_IMAGE=areteacademy/brevis-gateway:0.11.0 ./bench/run.sh
```

Er fährt eine Postgres hoch, startet das Gateway, belastet es mit [k6], **wartet,
bis sich der Puffer geleert hat**, liest das `/metrics` des Gateways selbst aus
und schreibt `bench/results/REPORT.md`.

## Drei Zeugen, und der dritte ist der, auf den es ankommt

k6 weiß, was ein Produzent gesehen hat. Das Gateway weiß, was es getan hat. Ein
Lasttest, der nur den ersten trägt, kann ein schnelles Gateway nicht von einem
unterscheiden, das mit `202` antwortet und verwirft, was es akzeptiert hat.

Der Bericht stellt die beiden daher nebeneinander – und zählt die Zeilen in
Postgres:

| Zeuge | Events |
|---|---:|
| k6 wurde `accepted` gemeldet | 5.431.000 |
| das Gateway zählte als empfangen | 5.431.000 |
| Zeilen in Postgres | 5.431.000 |
| Datensätze in der Dead Letter | 0 |

Die ersten beiden sind beide die eigene Arithmetik des Gateways – eine aus der
Antwort, die es schrieb, eine aus dem Zähler, den es erhöhte –, und ein Fehler,
der Events verlöre, könnte sie **untereinander** konsistent halten. Die Datenbank
ist die einzige, die uns nicht aufs Wort glaubte.

**Ein Lauf, in dem die drei nicht übereinstimmen, ist gescheitert**, wie gut die
Perzentile auch sind.

## Der obige Lauf

Der Lauf vom 26.09.2026 in `bench/results/REPORT.md`:
`areteacademy/brevis-gateway:0.11.0`, Darwin arm64, 11 CPUs, Postgres 17 in einem
Container auf demselben Kernel, `auto_table[columns]` mit `write: append`:

| | |
|---|---:|
| Events akzeptiert | **5.431.000** (135.768/s) |
| gesendeter Body | 1.156,7 MiB in 40s |
| `503` (Backpressure) | 0 |
| p50 / p95 / p99 | 5,9 ms / 16,0 ms / 22,7 ms |
| Batches zugestellt | 1.087, keiner begraben |

## Die Schwellenwerte sind der Punkt

k6 beendet sich mit einem Wert ungleich null, wenn einer scheitert, dies ist also
ein **Gate** und kein Diagramm:

| Schwellenwert | Warum |
|---|---|
| `p95 < 500ms` | der Annahmepfad muss von der Latenz der Sink fernbleiben. Eine Anfrage, die auf ein `COPY` wartet, ist der Defekt, gegen den die asynchrone Pipe existiert. |
| `http_req_failed < 1%` | ein `503` ist in Ordnung; ein Timeout oder ein 5xx nicht. |
| `events accepted > 1000` | eine Schwellenwert-Suite besteht gegen ein Gateway, das nichts beantwortet hat. |

Sie sind bewusst locker: Das läuft auf der Maschine, die die CI gerade hergab. Sie
fangen **eine Größenordnung** ab, wie eine Regression aussieht, nicht ein Prozent.

**Ein `503` ist kein Fehler.** Es ist das Gateway, das ein Event verweigert, für
das es keinen Platz hat, und es ist sicher zu wiederholen – die `ingestion_id` ist
eine unveränderliche Funktion des Events, derselbe erneut gesendete Body ist also
derselbe Datensatz. Es als Fehler zu zählen ließe Backpressure wie einen Bug
aussehen.

## Was es nicht misst

- **Nicht BigQuery.** Das Ziel ist eine Postgres in einem Container. Die
  Load-Job-Quote, die Commit-Latenz und die DDL-Nebenläufigkeit eines Warehouse
  stecken nicht in diesen Zahlen und können es nicht.
- **Nicht Dauerhaftigkeit.** `durability: memory` ist die einzige Stufe, ein `202`
  bedeutet also *in den RAM akzeptiert*. Gemessen wird, wie schnell das geschieht,
  nie, was ein `SIGKILL` übersteht.
- **Nicht Ihre Hardware.** Lesen Sie es als Form und als Verhältnis.

## Die lokale Cloud, nach Profil

```bash
docker compose -f docker-compose.drivers.yml --profile gcp up -d
docker compose -f docker-compose.drivers.yml --profile all up -d
```

Eine Datei mit Profilen, nicht eine pro Cloud: Zwei Dateien sind zwei
Projektnamen, und `postgres` in beiden hat die Entwicklungsdatenbank der Engine
schon einmal ersetzt.

### floci deckt weniger von BigQuery ab, als es scheint

Gemessen, nicht angenommen:

| | |
|---|---|
| ein Dataset und eine Tabelle anlegen | ✅ |
| `timePartitioning` und `clustering` bleiben erhalten | ✅ – die Form des Bugs aus `0.9.1` |
| eine Spalte per `PATCH` ändern | ✅ – die Evolution von `auto_table` |
| `insertAll` | ✅ |
| Abfragen | ✅, nur mit eingehängtem `/var/run/docker.sock` |
| **Load-Jobs** | ❌ `Only QUERY jobs are supported by the floci BigQuery emulator` |

Das SDK schreibt **nur** über Load-Jobs nach BigQuery. floci übt also jede Zeile
von `auto_table` aus, die eine Payload in DDL verwandelt – und **nicht einen
Brevis-Schreibvorgang**. Das ist weiterhin die Hälfte ohne lokalen Test, und sie
zu haben ist etwas wert. Aber eine grüne Suite gegen floci heißt nicht, dass
BigQuery funktioniert.

[k6]: https://k6.io

