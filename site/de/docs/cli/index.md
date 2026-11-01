# CLI

> Die zwölf Subcommands der Engine, mit Flags, Argumenten und dem, was jeder voraussetzt.

*https://brevis.sh/de/docs/cli/ · brevis.sh docs (de)*

---

Die Engine ist ein Binary mit Subcommands, nicht mehrere Binaries: API,
Scheduler und Worker sind Rollen desselben Systems, und ein Binary hält eine
Version, ein Image und einen Build-Weg.

```
brevis [command] [flags]
```

| Befehl | Datenbank | Zugangsdaten außerhalb von `local` | Rolle |
|---|---|---|---|
| [`serve`](#serve) | **ja** | **erforderlich** | API + Oberfläche |
| [`scheduler`](#scheduler) | **ja** | **erforderlich** | beide Schleifen |
| [`migrate`](#migrate) | **ja** | **erforderlich** | Schema |
| [`publish`](#publish) | **ja** | **erforderlich** | speichert Workflow und Zeitplan |
| [`backfill`](#backfill) | **ja** | **erforderlich** | verarbeitet einen Bereich neu |
| [`cancel`](#cancel) | **ja** | **erforderlich** | stoppt einen Run |
| [`run`](#run) | nein | — | führt jetzt aus, auf der Instanz selbst |
| [`validate`](#validate) | nein | — | validiert Workflow-YAML |
| [`brand`](#brand) | nein | — | validiert Branding-YAML |
| [`hash`](#hash) | nein | — | erzeugt den Passwort-Hash |
| [`version`](#version) | nein | — | Version, Commit, Build |

Die ersten sechs laden die Konfiguration, die ohne `BREVIS_DATABASE_URL` **beim
Start fehlschlägt**. Die anderen fünf nicht – weshalb `validate` in der CI
funktioniert, wo es kein Postgres gibt.

Genau diese sechs erben die Regel zu den Zugangsdaten: Bei einem `BREVIS_ENV`
ungleich `local` ist ein Start ohne die drei Authentifizierungsvariablen ein
**Startfehler**.

---

## serve

Startet die HTTP-API und die Oberfläche. Keine Flags – alles kommt aus der
Umgebung.

```bash
brevis serve
```

Fährt bei `SIGINT`/`SIGTERM` geordnet herunter, damit ein Deployment laufende
Anfragen nicht abschneidet. Dieser Prozess **materialisiert keine Zeitpläne**:
Dafür braucht es daneben ein `brevis scheduler`.

`CMD` des `api`-Images (distroless, keine Shell).

---

## scheduler

Beide Schleifen: Der Scheduler materialisiert Zeitpläne zu Runs, der Dispatcher
zieht aus der Queue und führt aus.

```bash
brevis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| Flag | Typ | Standard | |
|---|---|---|---|
| `--interval` | duration | `10s` | Intervall zwischen den Zyklen |
| `--concurrency` | int | `5` | gleichzeitige **Runs** |
| `--max-pods` | int | `5` | gleichzeitige **Steps** insgesamt |

`CMD` des `worker`-Images (alpine mit Shell, weil `run:`-Steps eine brauchen).

---

## alert

Stellt die Alarme zu, die der Scheduler festgehalten hat. Ein dritter Prozess
neben `serve` und `scheduler`.

```bash
brevis alert --interval 1s --max-attempts 6
```

| Flag | Typ | Standard | |
|---|---|---|---|
| `--interval` | duration | `1s` | Intervall zwischen den Zustellzyklen |
| `--max-attempts` | int | `6` | Versuche, bevor ein Alarm als nicht zugestellt festgehalten wird |

**Es existiert wegen des Ortes, an dem ein Alarm geschrieben wird, nicht weil die
Zustellung einen Prozess verdient.** Der Scheduler hält den Alarm in derselben
Transaktion fest wie den Fehler, der ihn rechtfertigt – entweder sind dem Run die
Versuche ausgegangen und der Alarm existiert, oder keins von beidem ist
geschehen. Danach muss etwas diese Tabelle leeren, und es muss sowohl einen
Slack-Ausfall als auch den eigenen Neustart überstehen. Davor bedeutete ein
fehlschlagender Webhook eine Logzeile und einen Alarm, der schlicht weg war.

Es verweigert den Start ohne konfigurierten Kanal (`BREVIS_SLACK_WEBHOOK`). Ein
Zustellprozess ohne Ziel leert die Outbox so schnell in „nicht zugestellt“, wie
sie sich füllt, und diese Zeilen sehen genau so aus, als lehne Slack sie ab.

Ein Alarm, den es aufgibt, wird **aufbewahrt**: „ausgelöst, nicht zugestellt,
4 Versuche, 403 von Slack“ ist die Zeile, auf die es ankommt, denn das ist der
Fall, in dem jemand auf eine Nachricht wartet, die nicht kommt.

`CMD` des `worker`-Images.

---

## report

Sendet die periodische Zusammenfassung dessen, was gelaufen ist.

```bash
brevis report --window 168h              # sends it
brevis report --window 168h --dry-run    # prints it, sends nothing
```

| Flag | Typ | Standard | |
|---|---|---|---|
| `--window` | duration | `168h` | wie weit zurückgeschaut wird |
| `--dry-run` | bool | `false` | ausgeben statt senden |

Ein **Befehl**, keine Schleife, und das ist der Unterschied zwischen einem
Bericht und einem Alarm: Er läuft aus einem CronJob, es gibt also keine neue
Schleife, keinen neuen Zustand und keine Leader-Wahl – der Cluster hat bereits
einen Scheduler. `--dry-run` ist der Weg, auf dem man einer wöchentlichen
Nachricht vertrauen lernt: indem man liest, was sie gesagt hätte.

Er läuft **nicht** über die Alarm-Outbox. Beide teilen sich einen Zustellkanal
und sonst nichts: Einen Alarm zu verlieren ist ein Ausfall, von dem niemand
hört, und die Zusammenfassung einer Woche zu verpassen heißt, dass die der
nächsten Woche kommt.

Die Nachricht enthält Runs, Erfolge, Fehler, die Pipelines, die am häufigsten
fehlschlugen, die, die am längsten brauchten, sowie die Zeilen und Byte, die das
SDK gemeldet hat. Sie enthält **kein** CPU und **keinen** Speicher und gibt dafür
auch nicht null aus – die Engine erhebt sie nicht, und die Nachricht sagt, wo
sie stattdessen zu finden sind.

Ein leeres Fenster sagt das, statt 100 % zu melden. Eine Erfolgsquote aus
nichts ist die beruhigendste Zahl, die ein Bericht ausgeben kann, und die
unwahrste: Eine leere Woche bedeutet meist, dass der Scheduler ausgefallen war.

`deployments/kubernetes/report.yaml` führt ihn wöchentlich aus.

---

## migrate

```bash
brevis migrate up      # applies what is missing
brevis migrate down    # reverts the last one
brevis migrate status  # shows the state
```

Nimmt genau ein Argument. Migrationen sind im Binary eingebettet und werden über
einen eigenen Subcommand angewendet – **`serve` ändert das Schema nie**.

---

## validate

Validiert eine oder mehrere Workflow-Dateien. Nimmt eine Datei **oder ein
Verzeichnis**.

```bash
brevis validate examples/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

Es berührt keine Datenbank und startet keinen Server, läuft also im Editor und in
der CI. Es beendet sich mit einem Wert ungleich null und zählt die Fehler.

---

## run

Führt einen Workflow **jetzt, auf der Instanz selbst** aus: keine Queue, keine
Datenbank, kein Scheduler.

```bash
brevis run examples/hello.yaml
brevis run wf.yaml --param load_full=true --retries 3 --timeout 5m
```

| Flag | Typ | Standard | |
|---|---|---|---|
| `--param` | wiederholbar | — | `key=value` für einen deklarierten Parameter |
| `--workdir` | string | das Verzeichnis der Datei | Arbeitsverzeichnis für Steps |
| `--retries` | int | `1` | Versuche pro Step (`1` = kein Retry) |
| `--timeout` | duration | `0` | Timeout pro Step (`0` = kein Limit) |

`--param` ohne `=` ist ein **Fehler**, keine Warnung: `--param load_full`, bei
dem der Wert vergessen wurde, liefe mit dem Standard – und der Operator würde
glauben, der Wert sei angewendet worden.

:::warning Vier Grenzen
`run` arbeitet nur mit `BREVIS_ENV=local`; ein Step mit `image:` läuft auf der
Instanz selbst und sagt das; es gibt keine Go-Task-Registry, sodass ein
`action:` für einen nicht registrierten Task fehlschlägt und die verfügbaren
nennt; und `run` liest `BREVIS_HOSTS` nicht, sodass ein Step mit `host:` hier gar
nicht laufen kann – er schlägt fehl und nennt den Host, auch wenn die Variable
gesetzt ist.
:::

---

## publish

Speichert Workflows und ihre Zeitpläne in der Datenbank. Nimmt eine Datei oder
ein Verzeichnis.

```bash
brevis publish examples/hello.yaml
brevis publish workflows/ --project acme --prune
```

| Flag | Typ | Standard | |
|---|---|---|---|
| `--project` | string | `default` | Projekt-Slug |
| `--prune` | bool | `false` | entfernt aus dem Projekt die Workflows, die in der Liste fehlen |

Mit `--prune` bleibt die Historie der entfernten Workflows erhalten.

---

## backfill

Materialisiert vergangene Slots eines bereits veröffentlichten Workflows. Er
**reiht ein, er führt nicht aus.**

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

| Flag | | |
|---|---|---|
| `--from` | **erforderlich** | Startdatum, `YYYY-MM-DD` |
| `--to` | **erforderlich** | Enddatum, `YYYY-MM-DD` |
| `--param` | | gilt für **jeden** Slot im Bereich |

`--to` schließt den ganzen Tag ein: Intern wird das Ende zu `23:59:59` dieses
Datums.

---

## cancel

Bricht einen Run ab, der noch wartet oder läuft. Der Ereignis-Feed zeigt `cli`
als den, der abgebrochen hat.

```bash
brevis cancel 3f1c9a52-6d0e-4b7a-9c41-2b8e5d7f0a13
```

| Antwort | Was geschehen ist |
|---|---|
| `canceled` | der Run wartete in der Queue; er hat sie verlassen und wird nicht laufen |
| `cancel requested` | der Run läuft; sein Dispatcher stoppt die Steps innerhalb von `--cancel-poll` (2s) |
| `already ended: <status>` | der Run war bereits beendet; nichts ändert sich |

Ein abgebrochener Run wird nicht wiederholt. Eine Id, die nicht existiert, ist
ein Fehler.

---

## gateway

Wie ein Gateway auf `/data` erscheint: seine Konfiguration, als bewusster Akt
veröffentlicht – nie sein Datenverkehr, und nie zur Laufzeit.

```bash
gateway describe gateway.yaml > manifest.json   # the gateway's own binary
brevis gateway publish manifest.json
brevis gateway unpublish events_gateway
```

`publish` prüft das Manifest vollständig, bevor es etwas schreibt, nennt Stream
und Rolle des ersten Problems und ersetzt die Ziele dieses Gateways in einer
Transaktion. `unpublish` entfernt ein außer Betrieb genommenes Gateway und ist
ein Fehler, wenn kein Gateway dieses Namens veröffentlicht war.

Wie eine `brevis-sql-api` unter Daten → Endpoints erscheint: was jeder Endpoint
annimmt und liest, als bewusster Akt veröffentlicht – nie ihr Datenverkehr.

```bash
brevis-sql-api describe --config api.yaml --out manifest.json   # the API's own binary
brevis endpoints publish manifest.json
brevis endpoints unpublish metrics-api
```

`publish` prüft das Manifest vollständig, bevor es etwas schreibt, und ersetzt
die Endpoints dieser API in einer Transaktion; ein Name, Standardwert oder
Hinweis, der wie eine Zugangsberechtigung aussieht, lässt alles scheitern.

Ein eigener Befehl statt eines Modus von `brevis publish`, weil Workflows und
Gateways unterschiedliche Mengensemantik haben: `publish --prune` darf nie die
Zeilen eines Gateways erreichen.

---

## prune

Wendet die Aufbewahrungsrichtlinie an. Es ist das **Einzige** in Brevis, das
etwas löscht, und es läuft nie von selbst – ein Operator plant es ein.

```bash
brevis prune --dry-run          # what would go, changing nothing
brevis prune
brevis prune --purge-after 0    # trim only, never delete a run
```

| Flag | Standard | |
|---|---|---|
| `--trim-after` | `720h` | leert Log, Phasen und die veröffentlichte Ausgabe |
| `--purge-after` | `8760h` | löscht den Run vollständig; `0` löscht nie |

---

## brand

Validiert eine Branding-Datei, ohne den Server zu starten.

```bash
brevis brand brand.yaml
```

```
  ok    Brevis · Orquestração
        logo      /assets/logo.svg  (simbolo embutido)
        destaque  #aa8450
        Powered by Brevis
```

Ein Verhaltensunterschied zum Start: **Eine fehlende Datei ist ein Fehler.** In
`serve` bedeutet Abwesenheit „die Standardidentität verwenden“; wer darum bat,
einen Pfad zu validieren, erwartet, gesagt zu bekommen, dass er nicht existiert.

---

## hash

Erzeugt den Wert für `BREVIS_AUTH_PASSWORD_HASH`.

```bash
brevis hash
```

Das Passwort wird **ohne Echo** vom Terminal gelesen, nie aus einem Argument:
Ein Argument erscheint in jedem `ps` und bleibt in der Shell-History. Passwörter
unter 12 Zeichen werden verweigert.

Der Hash geht allein nach **stdout** und die Beschriftungen nach **stderr**,
sodass `brevis hash > hash.txt` nur das Wesentliche speichert.

---

## version

```bash
brevis version
```

```
brevis 0.11.2
  commit  bb832ff
  build   2026-09-05T12:00:00Z
  go      go1.25.7 darwin/arm64
```

Direkt mit `go build` gebaut, gibt es `brevis dev` ohne Commit und ohne Datum aus
– und das von einem Release-Artefakt unterscheiden zu können, zählt, wenn
jemand merkwürdiges Verhalten meldet.
