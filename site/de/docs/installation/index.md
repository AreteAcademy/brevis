# Installation

> Wie Sie das Binary beziehen, was es voraussetzt und wie Sie bestätigen, dass die Installation korrekt ist.

*https://brevis.sh/de/docs/installation/ · brevis.sh docs (de)*

---

brevis.sh wird als **einzelnes Binary** und als **Docker-Image** ausgeliefert.
Daneben ist keine Laufzeitumgebung zu installieren: Migrationen,
Oberflächen-Assets und Schriften stecken alle im Binary.

## Voraussetzungen

| | |
|---|---|
| **PostgreSQL** | erforderlich für `serve`, `scheduler`, `migrate`, `publish` und `backfill` |
| **Go 1.25+** | nur, wenn Sie aus dem Quellcode bauen |
| **Kubernetes** | optional – ohne Cluster laufen Steps als lokale Prozesse |

`brevis run`, `validate`, `marca`, `hash` und `version` brauchen **keine** Datenbank.

## Aus dem Quellcode bauen

Dieser Weg erzeugt ein Binary mit eingestempelter Version und eingestempeltem Commit:

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

:::warning Die Engine hat noch kein getaggtes Release
Jeder Tag im Repository gehört zum SDK (`sdk/v*`); das Root-Modul hat keinen. Ein
`go install github.com/AreteAcademy/brevis/cmd/brevis@latest` löst auf den
neuesten Commit des Branches auf und erzeugt ein Binary, das sich als
`brevis dev` ausweist, ohne Commit und ohne Datum – die `-ldflags` kommen nur
über `make build` und `docker build` hinein. Für eine nachvollziehbare Version
nutzen Sie das Image oder `make build`.
:::

## Docker

Zwei Images desselben Binarys, weil die beiden Rollen gegensätzliche Anforderungen haben:

| Tag | Basis | Warum |
|---|---|---|
| `:0.11.2` | distroless | die API liefert nur HTTP und führt nichts aus – keine Shell, minimale Angriffsfläche |
| `:0.11.2-worker` | alpine + tini | `run:`-Steps brauchen eine Shell |

```bash
docker run --rm brevissh/brevis:latest version
```

Beide Images liegen unter [hub.docker.com/r/brevissh/brevis](https://hub.docker.com/r/brevissh/brevis),
für `amd64` und `arm64`.

## Eine vollständige lokale Umgebung

Das Repository liefert eine `docker-compose.yml` mit, die Postgres, die API und
den Scheduler hochfährt:

```bash
make up      # Postgres + API + scheduler
make smoke   # checks /health and /ready
make logs
make down
```

Die Oberfläche erreichen Sie unter `http://localhost:8080`.

## Minimale Konfiguration

Eine einzige Variable ist erforderlich:

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
```

Ohne sie scheitern die Subcommands, die die Datenbank nutzen, beim Start – mit
Absicht. Ein Prozess, der ohne Zugangsdaten startet, entdeckt das Problem erst
bei der ersten Anfrage, und dann hat die Readiness dem Orchestrator bereits
etwas Falsches gemeldet.

```
$ brevis migrate status
erro: BREVIS_DATABASE_URL e obrigatoria
```

Die vollständige Liste finden Sie unter [Konfiguration](/de/docs/configuration/index.md).

## Das Schema anwenden

Migrationen sind im Binary eingebettet und werden über einen eigenen Subcommand
angewendet – **`serve` ändert das Schema nie**:

```bash
brevis migrate up
brevis migrate status
```

## Bestätigen

```bash
brevis version
brevis validate examples/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencias  cron 0 2 * * *
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

Wenn beide antworten, ist die Installation korrekt. Weiter geht es mit dem
[Schnellstart](/de/docs/quickstart/index.md).
