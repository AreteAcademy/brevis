# Workflows

> Das vollständige YAML-Format – Steps, Abhängigkeiten, Images, Ressourcen und Zeitplan.

*https://brevis.sh/de/docs/workflows/ · brevis.sh docs (de)*

---

Ein Workflow ist eine YAML-Datei. Sie deklariert, **was** ausgeführt wird, **in
welcher Reihenfolge** und **mit welcher Laufzeitumgebung** – und nichts darüber
hinaus.

## Minimale Struktur

```yaml
name: hello
steps:
  - id: only
    run: echo done
```

`name` identifiziert den Workflow in der Datenbank und in der Oberfläche. `steps`
ist die Liste der Steps, jeder mit einer eindeutigen `id` und einem `run`.

### `description` – wozu er dient

Optional, Freitext, und von nichts in der Engine gelesen:

```yaml
name: daily-orders
description: >
  Pulls yesterday's orders from the vendor and lands them in bronze.
  Runs at 04:00 because the vendor closes its books at 03:30.
steps:
  - id: extract
    run: python fetch.py
```

Sie erscheint oben auf der Seite des Workflows, über den Zählern und dem
Zeitplan. Der Graph sagt bereits, was ein Flow **tut**; dies ist die einzige
Stelle, die sagt, warum er existiert – und genau das braucht, wer ihn um drei
Uhr nachts zum ersten Mal öffnet.

Ein Workflow ohne sie zeigt dort nichts an – keinen leeren Block, der wie etwas
wirkt, das nicht geladen werden konnte.

## Reihenfolge: chain oder dag

```yaml
type: chain   # order is the file's order
type: dag     # order comes from depends_on
```

`chain` ist syntaktischer Zucker: Der Parser macht aus der Abfolge Kanten, und
**die Engine kennt nur DAGs**. Verwenden Sie `dag`, wenn die Reihenfolge nicht
linear ist:

```yaml
type: dag

steps:
  - id: prepare
    run: ./prepare.sh

  # Siblings: they depend on the same step, so they run in parallel.
  - id: extract
    run: ./extract.sh
    depends_on: [prepare]

  - id: validate
    run: ./validate.sh
    depends_on: [prepare]

  - id: publish
    run: ./publish.sh
    depends_on: [extract, validate]
```

Der Runner durchläuft den Graphen **ebenenweise**: Alles innerhalb einer Ebene
läuft parallel, und die nächste Ebene startet erst, wenn die vorherige
vollständig abgeschlossen ist.

:::warning Ein Fehler stoppt die ganze Ebene
Der Runner hält beim **ersten** Fehler in einer Ebene an, ohne die nächste zu
starten. Nach einem Fehler weiterzumachen würde ein Teilergebnis liefern, das
vollständig aussieht.
:::

## Zeitplan

```yaml
schedule: "0 5 * * *"   # five-field cron
concurrency: 1
```

Ohne `schedule` ist der Workflow manuell: Er läuft nur per Auslöser in der
Oberfläche, über `brevis run` oder über `backfill`.

`concurrency: 1` begrenzt gleichzeitige Runs desselben Workflows – damit
verhindert ein `*/15`, dass er sich selbst überlappt, wenn ein Run länger als
fünfzehn Minuten dauert.

Zwei **verschiedene** Workflows, die dieselben Tabellen schreiben, brauchen eine
**Gruppe**. Ein täglicher und ein `*/15`, die beide dieselben dbt-Modelle
bauen, deklarieren jeweils:

```yaml
concurrency:
  group: jaffle_models   # the shared resource, named the same in both files
  limit: 1               # optional; a group without one means 1
```

Die Runs aller Workflows der Gruppe zählen gegen ein gemeinsames Limit, gleich
was sie gestartet hat – der Zeitplan, ein Landing, ein Backfill oder ein Klick.
Ein Run, der die Gruppe voll vorfindet, **wartet in der Queue**; er wird nicht
übersprungen und schlägt nicht fehl. Ein dritter Workflow, der dieselben
Tabellen schreibt, tritt bei, indem er die Gruppe nennt. Während er wartet,
sagt die Seite des Runs das: welche Gruppe, ihr Limit und welche Runs die Slots
belegen.

- Jedes Mitglied deklariert **dasselbe Limit**. `validate` und `publish`
  verweigern eine Gruppe, die in einer Datei mit 1 und in einer anderen mit 3
  deklariert ist, auch gegenüber bereits veröffentlichten Mitgliedern.
- Eine Gruppe serialisiert **Brevis-Runs**. Ein `dbt run`, das jemand auf dem
  eigenen Rechner startet, steht nicht in dieser Queue und scheitert auf die
  gleiche Weise.

## Image und Ressourcen

```yaml
image: us-central1-docker.pkg.dev/example/apps/dbt:1.10.3
resources:
  cpu: 200m
  memory: 1Gi
  limits: {memory: 2Gi}
```

Oben deklariert, sind sie der **Standard für jeden Step**. Jeder Step kann seine
eigenen überschreiben:

```yaml
steps:
  - id: bronze
    run: dbt build --select bronze+          # inherits the top-level image

  - id: notify
    image: ghcr.io/example/notify:0.3        # another runtime
    shell: false                             # distroless has no shell
    run: /notify --channel data
    resources: {cpu: 25m, memory: 32Mi, limits: {memory: 64Mi}}
    depends_on: [bronze]
```

Das ist es, was einen Go-Fetcher neben einem 1,9 GB großen `dbt build` nur 12 MB
und 32Mi kosten lässt, statt dass beide die Größe des größeren zahlen. Siehe
[Pod pro Step](/de/docs/pod-per-step/index.md).

## Felder eines Steps

| Feld | | |
|---|---|---|
| `id` | **erforderlich** | eindeutig im Workflow; der Name, der im Graphen und in den Logs angezeigt wird |
| `run` | | der Befehl |
| `image` | | das Image des Steps; ohne Angabe wird das oberste übernommen |
| `host` | | den Befehl an eine Maschine schicken, die die Engine **nicht** verwaltet – eine Alternative zu `image`, nie eine Ergänzung. Siehe [`host:` im Kubernetes-Leitfaden](https://github.com/AreteAcademy/brevis/blob/master/docs/KUBERNETES.md#a-third-place-a-step-can-run-host) |
| `shell` | `true` | `false` führt ohne Shell aus – erforderlich bei distroless |
| `depends_on` | | Liste der `id`s, die zuerst abgeschlossen sein müssen; ein Eintrag kann `{step, label}` sein |
| `marker` | `false` | ein Step ohne Befehl, für ein `start` oder ein `end` |
| `resources` | | `cpu`, `memory` und `limits` für diesen Step |
| `runtime`, `tools` | | Sprache und Werkzeuge des Steps – siehe [Step-Laufzeitumgebung](/de/docs/runtime/index.md) |
| `when` | `all_success` | bei welchem Zustand seiner Abhängigkeiten dieser Step läuft – siehe unten |
| `unless_empty` | | ein Kontext-Schlüssel, der entscheidet, ob es etwas zu tun gibt – siehe unten |
| `for_each` | | ein Kontext-Schlüssel mit einer Liste; der Step läuft einmal pro Element – siehe unten |
| `group` | | zeichnet diesen Step in einen benannten, einklappbaren Kasten – siehe unten |
| `uses` | | ein anderer Workflow, dessen Steps die Stelle dieses Steps einnehmen – siehe unten |
| `on_error` | | meldet die Fehler dieses Steps – siehe unten |

## Einen Step nur ausführen, wenn es etwas zu tun gibt

```python
# in extract
context.set(has_rows=len(rows) > 0)
```

```yaml
  - id: transform
    run: ./transform.sh
    depends_on: [extract]
    unless_empty: extract.has_rows
```

`unless_empty` nennt einen **Schlüssel**, keinen Ausdruck. Der Step entscheidet
und veröffentlicht die Antwort; die Engine liest einen Schlüssel und fragt, ob
er leer ist.

| gilt als leer | gilt als vorhanden |
|---|---|
| `false`, `0`, `""`, `null`, `[]`, `{}` | alles andere |

`"false"` als **String** ist vorhanden. Ein Step, der diese fünf Zeichen
veröffentlicht hat, hat etwas veröffentlicht, und zu raten, dass ein Boolean
gemeint war, ist der Weg, auf dem eine Regel Meinungen bekommt, die ihr Autor
nicht sehen kann. Veröffentlichen Sie einen echten Boolean.

### Warum kein Ausdruck

Der verlockende Entwurf ist `when: "{{ context.extract.rows > 0 }}"`. Eine
Mini-Ausdruckssprache ist eine große Verpflichtung: ein Parser, ein Typsystem,
das festlegt, was `>` über ein JSON-`any` hinweg bedeutet, eine
Sicherheitsfrage, weil der Ausdruck aus einem YAML stammt, das jemand anderes
geschrieben hat, und Fehlermeldungen, die in einen String zeigen. Jeder
Orchestrator, der eine hat, hat einen Bugtracker voll davon.

Hier liegt die Entscheidung in der Sprache, die ihr Autor ohnehin schreibt, wo
sein eigenes Test-Framework sie erreicht.

### Ein fehlender Schlüssel ist ein Fehler, kein leerer Wert

Fehlt der Schlüssel, **schlägt der Step fehl**, und die Meldung nennt ihn
zusammen mit dem, was der Veröffentlichende tatsächlich geschrieben hat:

```
step "transform" reads `extract.has_row`, and "extract" published "has_rows" but not "has_row"
```

Die Alternative – einen fehlenden Schlüssel als „leer, also überspringen“ zu
lesen – macht aus einem Tippfehler einen Step, der still und für immer nicht
mehr läuft, ohne dass irgendwo steht, warum. Das ist das schlimmste Ergebnis,
das dieses Feature haben kann.

Der Schlüssel wird immer durch den Step qualifiziert, der ihn veröffentlicht,
und dieser Step muss einer sein, von dem dieser abhängt. Beides wird beim
**Veröffentlichen** verweigert: Ein Step, der an einen Schlüssel gebunden ist,
den er nie sehen kann, würde für immer übersprungen, und das zu entdecken,
wenn ein nächtlicher Lauf aufhört, ist zu spät.

## Einen Step einmal pro Element ausführen

```python
# in extract
context.set(partitions=["2026-01", "2026-02", "2026-03"])
```

```yaml
  - id: load
    run: ./load.sh "$BREVIS_MAP_VALUE"
    depends_on: [extract]
    for_each: extract.partitions
```

Der Step läuft einmal pro Element, jeweils mit eigener Zeile, eigenem Retry und
eigenem Exit-Code. Der Graph zeigt **einen Knoten mit `[3]`**, nicht drei
Knoten.

| Variable | |
|---|---|
| `BREVIS_MAP_INDEX` | `0`, `1`, `2` … |
| `BREVIS_MAP_VALUE` | das Element |

Ein JSON-String kommt **ohne Anführungszeichen** an: `for_each` über
`["2026-01"]` gibt einer Shell `2026-01` und nicht `"2026-01"`. Alles andere –
eine Zahl, ein Objekt, eine Liste – kommt als sein JSON an.

Keine der beiden Variablen existiert bei einem nicht abgebildeten Step. Eine,
die immer da und immer leer ist, bringt jedem, der die Umgebung liest, bei, sie
zu ignorieren.

### Die Form des DAG ändert sich nicht

Ein abgebildeter Step ist weiterhin ein Knoten mit einem Satz Kanten. Nur die
Zahl der Zeilen darunter variiert, das Layout bleibt also, was es immer war,
und das `[3]` wird beim Lesen aus diesen Zeilen gezählt – nichts wird
gespeichert, also muss nichts synchron gehalten werden, und eine falsche Zahl
kann eine korrigierte Abfrage nicht überleben.

Solange Instanzen noch laufen, zeigt die Karte `[2/3]`.

### Eine fehlgeschlagene Instanz lässt den Step fehlschlagen

Drei Partitionen laden und eine nicht: Das ist ein Step, der seine Aufgabe nicht
erfüllt hat, und die Steps darunter sehen das. Ein Knoten, der grün wird, weil
das meiste funktioniert hat, ist ein Abzeichen, das lügt.

**Ein Retry wiederholt nur die Instanzen, die fehlgeschlagen sind.** Achtzehn
Partitionen, die funktioniert haben, werden nicht neu geladen, weil zwei
kaputtgingen.

### Eine leere Liste ist `skipped`, kein Erfolg

Ein Step, der nichts getan hat, weil nichts zu tun war, hat darin nicht Erfolg
gehabt. Ein grüner Knoten über null Instanzen ist genau das, worauf jemand ein
Dashboard baut.

Ein Wert, der **keine Liste** ist, schlägt fehl, ebenso ein Schlüssel, der nicht
da ist – dieselbe Richtlinie wie bei `unless_empty:`, und aus demselben Grund:
Ein Tippfehler, der still null Instanzen erzeugt, würde den Step für immer
abschalten.

### Der Fan-out ist begrenzt, und die Grenze wird geerbt

Die Liste reist im Kontext, und der Kontext hat eine Obergrenze von 4096 Byte,
die vom kubelet stammt. Ein Workflow kann nicht zehntausend Pods anfordern, ohne
vorher einen Weg zu finden, das in vier Kilobyte zu sagen. Das ist eine Grenze,
die man behalten sollte, statt sie zu umgehen.

### Was ein abgebildeter Step veröffentlicht

Seine Ausgabe wird auf der Zeile jeder Instanz festgehalten und ist für die
Steps darunter **nicht** sichtbar. Vier Instanzen, die unter dem Namen eines
Steps veröffentlichen, sind vier Werte für einen Schlüssel, und es gibt auf
`context.String("load.bucket")` keine Antwort, die nicht geraten wäre. Der Step
sagt das in seinem Log, statt es still zu verwerfen.

## Steps im Graphen gruppieren

```yaml
steps:
  - id: extract_orders
    group: sales_data_reporting
    run: ./extract.sh
  - id: load_orders
    group: sales_data_reporting
    depends_on: [extract_orders]
    run: ./load.sh
```

Die Steps werden in einen benannten Kasten gezeichnet, der sich einklappen
lässt – Airflows TaskGroup, für den Fall, dass ein DAG so groß wird, dass er
nicht mehr lesbar ist.

**Nur visuell.** Airflows TaskGroups stellen den Ids darin auch ein *Präfix*
voran, sodass aus `extract` `sales.extract` wird. Das hier nicht: Ein Präfix
würde jedes `depends_on`, jeden Kontext-Schlüssel und jede festgehaltene Zeile
in einem bestehenden Workflow verändern, für ein Feature, dessen ganzer Wert
darin besteht, dass sich ein großer Graph besser liest. Namensräume lassen sich
später ergänzen – das ist eine strikte Erweiterung davon.

Eine Gruppe ist ein **Label**, kein Container. Steps behalten ihre globalen
Ids, eine Gruppe kann über Ebenen hinweggehen, und an der Ausführung ändert sich
nichts.

Ein Klick auf den Namen der Gruppe klappt sie ein: Ihre Steps verschwinden, und
die Pfeile, die die Grenze kreuzten, zeigen stattdessen auf den Kasten.

## Einen anderen Workflow wiederverwenden

```yaml
# nightly.yaml
steps:
  - id: prepare
    run: ./prepare.sh

  - id: mlops
    uses: ml_training       # another workflow in the same publish
    depends_on: [prepare]

  - id: report
    run: ./report.sh
    depends_on: [mlops]
```

Beim **Veröffentlichen** nehmen die Steps von `ml_training` die Stelle dieses
Steps ein, mit dessen Id als Präfix, und der `uses`-Knoten verschwindet:

```
prepare → mlops.train → mlops.evaluate → report
```

Sie kommen als eine einklappbare Gruppe an, sodass der Graph zeigt, was die
Datei gesagt hat.

**Beim Veröffentlichen expandiert, nicht zur Laufzeit ausgeführt.** Ein Step,
der einen Kind-*Run* auslöst und wartet, ist der Entwurf, der Airflow in einen
Deadlock führte, und diese Engine hat dieselbe Zutat: Die Pod-Obergrenze ist
ein Semaphor pro Prozess, sodass ein Elternteil, das einen Slot hält und auf
ein Kind wartet, das Slots aus demselben Pool braucht, hängt – und nur unter
Last, also in Produktion. Ein Run, ein Graph, ein Pool.

Aufgegeben wird damit ein Kind-Run mit eigener Id und eigener Historie.

### Die Regeln

| | |
|---|---|
| das Kind muss im **selben Publish** sein | nicht bloß bereits veröffentlicht |
| Pfeile | in den Step hinein werden zu Pfeilen in jede **Wurzel** des Kindes; aus ihm heraus zu Pfeilen aus jedem seiner **Blätter** |
| `image`, `env`, `secrets`, `resources` des Kindes | werden auf jeden Step materialisiert, sodass er in dem läuft, was seine eigene Datei sagte |
| die Schlüssel `unless_empty` / `for_each` des Kindes | wandern mit dem Präfix |
| Verschachtelung | wird abgeflacht, Tiefe zuerst |
| ein Zirkel | wird beim Veröffentlichen verweigert, mit Nennung der Kette |

**Derselbe Publish und nicht „bereits veröffentlicht“ ist die wichtige Regel.**
Eine Expansion, die die Datenbank läse, ließe `brevis validate` – das mit Absicht
keine Datenbank berührt – eine andere Frage beantworten als `brevis publish`,
und die Datei, die die CI bestanden hat, wäre die Datei, die das Deployment
scheitern lässt.

Das Kind ist weiterhin für sich veröffentlichbar: Die Expansion **kopiert**, sie
verbraucht nicht. Ein Step kann nicht zugleich `uses:` und `run:`, `action:`
oder `marker:` deklarieren – das wäre eine Datei, die zwei Dinge sagt.

## Sagen, was ein Pfeil bedeutet

Eine Abhängigkeit kann ein Label tragen, das am Pfeil im Graphen angezeigt wird.

```yaml
steps:
  - id: determine_load_type
    run: ./decide.sh

  - id: load_full
    run: ./full.sh
    depends_on:
      - {step: determine_load_type, label: additional data}

  - id: load_delta
    run: ./delta.sh
    depends_on:
      - step: determine_load_type
        label: changed existing data
```

Die nackte Form – `depends_on: [extract]` – funktioniert weiter und bleibt die
normale. Die meisten Abhängigkeiten haben nichts zu sagen, und ein Label an
jedem Pfeil ist Rauschen.

Die Labels verdienen ihren Platz bei einer **Verzweigung**: Zwei Pfeile, die
denselben Step verlassen und auf denen nichts steht, sind ein Diagramm, für das
man den Quelltext öffnen muss – das eine, was ein Graph vermeiden soll.

## Ein Step, der nichts tut

```yaml
steps:
  - id: start
    marker: true

  - id: end
    marker: true
    depends_on: [load_full, load_delta, report]
```

`marker: true` ist ein Step ohne Befehl. Er führt nichts aus, ist sofort
erfolgreich und erscheint im Graphen.

Er ist keine Dekoration. Ein `end`, das von jedem Zweig abhängt, macht aus
*Ist das Ganze fertig?* einen Knoten statt sechs Pfeilen, denen man folgen muss –
und es verhält sich wie jeder andere Step, sodass ein `end` unter einem
fehlgeschlagenen Zweig **übersprungen** wird, nicht grün.

**Ein Step ohne `run:` und ohne `marker: true` wird weiterhin verweigert.** Die
beiden Fälle dürfen nicht zu einem verschmelzen: Ein leerer Befehl ist fast
immer ein Fehler, und `marker: true` ist die Art, zu sagen, dass es Absicht war.
Ein Marker, der auch `run:` deklariert, wird ebenfalls verweigert – das wäre
eine Datei, die zwei Dinge sagt.

## Einen Step nur ausführen, wenn etwas fehlgeschlagen ist

Standardmäßig läuft ein Step, wenn alles davor funktioniert hat. `when:` ändert
das.

```yaml
steps:
  - id: extract
    run: python fetch.py

  - id: notify_failure
    run: ./notify.sh
    depends_on: [extract]
    when: any_failed          # runs precisely when extract did not make it

  - id: cleanup
    run: ./cleanup.sh
    depends_on: [extract, transform]
    when: all_done            # runs either way
```

| Regel | |
|---|---|
| `all_success` | der Standard. Alles, wovon dieser Step abhängt, war erfolgreich – seine **eigenen** Abhängigkeiten, wie in Airflow |
| `any_failed` | mindestens ein Step, von dem dieser abhängt, ist fehlgeschlagen |
| `all_done` | alles, wovon dieser Step abhängt, ist abgeschlossen, gleich wie es endete |

Eine unbekannte Regel wird beim Veröffentlichen verweigert, mit Nennung dessen,
was gültig ist. Das muss so sein: Ein `when: on_failure`, das als „der Standard“
gelesen würde, liefe bei **Erfolg** – das Gegenteil dessen, was es sagt,
entdeckt in der Nacht, auf die es ankam.

### Ein Step, der nicht läuft, ist `skipped`

Das ist ein eigener Zustand, in eigener Farbe gezeichnet, und er trägt den
Grund: *`extract` war failed*. Davor hatte ein Step unterhalb eines Fehlers
überhaupt keinen Eintrag, und der Bildschirm zeigte ihn für immer als pending.

`skipped` ist weder ein Fehler noch ein Erfolg. Ein Step, dessen Upstream
kaputtging, ist nicht fehlgeschlagen – er hat nie die Chance bekommen –, und ihn
Erfolg zu nennen ist eine Lüge, die bis zum Status des Runs selbst reicht.

Eine Trigger-Regel entscheidet, welche Steps **laufen**. Sie entscheidet nicht
über das Ergebnis des Runs: Ein `notify_failure`, das seine Nachricht
zugestellt hat, heißt nicht, dass die Pipeline funktioniert hat, und der Run ist
weiterhin failed.

### `all_success` ist lokal, wie in Airflow

Die Regel fragt nach den **eigenen** Abhängigkeiten eines Steps und sonst
nichts. Ein Fehler in einem unabhängigen Zweig hält diesen nicht auf:

```
extract_orders ──✕                 (failed)
extract_users  ──✓── transform_users ──✓     keeps going
```

Ein Zweig **unterhalb** des Fehlers stoppt weiterhin – dass `all_success` lokal
ist, heißt nicht, dass es fehlt.

Diese Engine brach früher den ganzen Graphen beim ersten Fehler ab. Das hatte
einen Grund: Nach einem Fehler weiterzumachen lieferte ein Teilergebnis, das
vollständig aussah, und eine Pipeline lief 28 Tage zu spät, ohne dass es jemand
bemerkte.

**Was diesen Schutz ersetzt**, und es ist nicht nichts:

- der Run **schlägt** weiterhin **fehl**, mit demselben Fehler;
- der Graph zeigt den fehlgeschlagenen Step in Rot und jeden übersprungenen in
  eigener Farbe, jeweils mit dem Hinweis, welcher Step ihn gestoppt hat;
- der Alarm geht weiterhin hinaus, wenn der Run aufgibt.

Ein Teilergebnis sieht nicht mehr vollständig aus, weil der Run sagt, dass es
das nicht ist.

**Was aufgegeben wird**, offen gesagt: Ein unabhängiger Zweig schreibt nun seine
Daten in einem Run, der anderswo fehlgeschlagen ist.

### Ein Retry führt nur aus, was fehlgeschlagen ist

Der Retry eines Runs wiederholt nicht den ganzen Graphen. Ein Step, der in einem
früheren Versuch **desselben Runs** bereits erfolgreich war, behält sein
Ergebnis und läuft nicht erneut – so, wie sich ein geleerter DAG-Run in Airflow
verhält.

```
attempt 1   extract ✓    load ✕    report (skipped)
attempt 2   extract –    load ✓    report ✓            extract is not re-run
```

Er behält seine ursprüngliche Zeile: Dauer, Log und das, was er veröffentlicht
hat, stammen vom ersten Versuch, denn dann hat die Arbeit stattgefunden. **Der
Step darunter liest weiterhin, was er veröffentlicht hat** – der Kontext des
Runs wird gespeichert, nicht neu aufgebaut.

Die Unterscheidung, auf die es ankommt: Hier geht es um frühere Versuche
**dieses Runs**. Ein Step, der im Run von *gestern* erfolgreich war, läuft heute
normal. (Auch die andere Frage gibt es, und sie sagt dem SDK, ob es das erste
Mal eines Steps ist – siehe `BREVIS_RUN_FIRST`.)

Ein Step, der **übersprungen** wurde, ist nicht erledigt: Er hat nie gelaufen,
und der Retry entscheidet mit den Ergebnissen des neuen Versuchs erneut über
ihn.

## Die Fehler eines Steps melden

Jeder Fehler wird bereits gemeldet: Ist ein Webhook konfiguriert, sendet ein
Run, der aufgibt, eine Nachricht, ohne dass ein Block in irgendeiner Datei
wiederholt würde. `on_error` ist für den Step, der eine eigene braucht.

```yaml
steps:
  - id: fetch_observations
    run: python fetch.py
    on_error:
      type: SLACK
```

| Feld | | |
|---|---|---|
| `type` | **erforderlich** | `SLACK`. Ein unbekannter Wert wird beim Veröffentlichen verweigert, mit Nennung dessen, was gültig ist |
| `when` | `give_up` | `attempt` meldet jeden fehlgeschlagenen Versuch, nicht nur den letzten |

Es kommen zwei Nachrichten statt einer an, und das ist der Punkt: Der Alarm auf
Run-Ebene sagt *`id_verification` ist fehlgeschlagen*, was braucht, wer die
Pipeline verantwortet; der auf Step-Ebene sagt *`fetch_observations` ist
fehlgeschlagen*, was braucht, wer diese Integration verantwortet.

**Es gibt kein Feld `webhook` oder `url`, und es wird keines geben.** Das Ziel
ist eine Zugangsberechtigung – wer sie hält, postet im Kanal, als wäre er die
Plattform –, und eine Workflow-Datei schreibt jemand, der nicht unbedingt
bestimmen darf, wohin die Alarme des Unternehmens gehen. Die Datei sagt,
**ob** und **wie**; die Installation sagt, **wohin**, über `BREVIS_SLACK_WEBHOOK`.

**`when: give_up` ist der Standard, weil es darum geht, wessen Nacht es ist.**
Ein Step, der zweimal fehlschlägt und beim dritten Versuch gelingt, würde beim
anderen Standard zwei Nachrichten senden, und die zweite käme an, nachdem das
Problem verschwunden war.

**Ein Step, der nicht fehlgeschlagen ist, wird nie gemeldet.** In einem
Workflow, in dem ein Zweig kaputtgeht, wird niemand geweckt, der den anderen
Zweig verantwortet.

Die Zustellung ist die Aufgabe von `brevis alert` – der Alarm wird in derselben
Transaktion wie der Fehler festgehalten, sodass ein Slack-Ausfall ihn verzögert,
statt ihn zu verlieren.

## Tags

```yaml
tags: [analytics, dbt, daily]
```

Sie filtern in der Oberfläche. Auf die Ausführung wirken sie sich nicht aus.

## Validierung

Die Validierung braucht keine Datenbank und läuft daher in der CI neben den
Tests:

```bash
brevis validate workflows/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencias  cron 0 2 * * *
```

Sie nimmt eine Datei oder ein Verzeichnis. In einem Verzeichnis passt sie auf
`*.y*ml` und sortiert – zwei Läufe erzeugen dasselbe Log, und der Unterschied
zwischen zwei Deployments wird nicht zu Rauschen.

## Veröffentlichen

```bash
brevis publish workflows/
brevis publish workflows/ --project acme --prune
```

`--prune` entfernt aus dem Projekt die Workflows, die in der veröffentlichten
Liste fehlen, und bewahrt ihre Historie. Es ist aus einem praktischen Grund
**nicht** der Standard: `publish one-file.yaml` darf die anderen achtundvierzig
nicht löschen, nur weil sie auf der Kommandozeile nicht genannt wurden.

## Nächste Schritte

- [Parameter](/de/docs/parameters/index.md) – was sich zwischen zwei Runs ändert
- [Scheduler und Queue](/de/docs/scheduler-and-queue/index.md) – wie aus einem Workflow Ausführung wird
- [Kontext zwischen Steps](/de/docs/context/index.md) – was ein Step dem nächsten mitteilt
