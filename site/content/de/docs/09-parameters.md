---
title: Parameter
description: Was sich zwischen zwei Runs desselben Workflows ändert, ohne die Datei zu bearbeiten.
group: Referenz
order: 9
slug: parameters
---

Ein Workflow kann **Run-Parameter** deklarieren: Werte, die sich zwischen zwei
Auslösungen ändern, ohne dass sich die Datei ändert.

## Deklarieren

```yaml
params:
  - name: load_full
    type: boolean
    default: "false"

  - name: start_date
    type: string
    pattern: '^\d{4}-\d{2}-\d{2}$'

steps:
  - id: run
    run: dbt build --vars '{"load_full":"{{ .load_full }}"}' --select bronze+
```

| Feld | | |
|---|---|---|
| `name` | **erforderlich** | der Name, der im Template und in `--param` verwendet wird |
| `type` | | `string`, `boolean`, `integer` oder `list|<type>` für mehrere Werte |
| `default` | | wird verwendet, wenn die Auslösung keinen Wert liefert |
| `pattern` | | regulärer Ausdruck, dem der Wert entsprechen muss |
| `title` | | das Label, das das Formular zeigt, bis zu 60 Zeichen; sonst der Name |
| `group` | | fasst Parameter im Formular zusammen, bis zu 60 Zeichen |
| `warn` | | was ein vom Standard abweichender Wert kostet, unter dem Feld angezeigt, bis zu 200 Zeichen |
| `uses` | | der Schlüssel des Connectors, um den es bei dem Parameter geht (`bigquery`, `postgres`), für sein Logo |

Ein Parameter **ohne `default`** ist erforderlich: Eine Auslösung, die ihn
weglässt, schlägt fehl, bevor ein Step ausgeführt wird.

`title`, `group`, `warn` und `uses` bestimmen, wie das Formular den Parameter
darstellt, und **keiner davon ändert, was ein Wert sein darf**. Jedes ist eine
Zeile; `uses` besteht aus Kleinbuchstaben, Ziffern und `-`. Das Veröffentlichen
verweigert alles andere und nennt den Parameter.

```yaml
params:
  - name: load_full
    type: boolean
    default: "false"
    title: Full load
    group: Load
    warn: reprocesses the whole table; it costs
    uses: bigquery
```

## Eine Liste von Werten

`list|<type>` deklariert einen Parameter, der mehrere Werte trägt – die zu
ladenden Tabellen, die neu zu bauenden Schichten, die per Backfill nachzuholenden
Fenster:

```yaml
params:
  - name: tables
    type: list|string
    default: "users,orders"

  - name: layers
    type: list|string
    enum: [bronze, silver, gold]     # an enum restricts each ITEM
    default: "bronze"

  - name: days
    type: list|integer
    default: "1,7,30"
```

Das Element kann `string`, `integer` oder `boolean` sein. Jedes Element wird für
sich validiert, sodass `list|integer` mit `1,x,30` fehlschlägt und dabei
**Element 2** nennt statt den ganzen Wert.

Es ist überall **ein kommagetrennter String**: in der Datenbank, in
`BREVIS_RUN_PARAMS` und im Befehl. Dadurch funktioniert es in einem Step ganz
ohne Sonderfall –

```yaml
run: dbt build --select {{ .tables }}      # dbt build --select users,orders
```

– und deshalb kann in einem Element kein Komma vorkommen. Ein leeres Element und
ein wiederholtes werden ebenfalls verweigert: Eine Wiederholung verdoppelt
still, was der Step pro Element tut.

### Als Array, in einem Step

```go
for _, table := range p.Run.ParamList("tables") {
    load(table)
}
```

```python
from brevis import run

for table in run.param_list("tables"):
    load(table)
```

Beide liefern `[]` für einen Parameter, den niemand gefüllt hat, statt einer
Liste mit einem leeren String: Über „nichts zu tun“ zu iterieren sollte nichts
tun. Die Elemente kommen als Strings zurück – auch bei `list|integer` –, weil
der Step besser als die Bibliothek weiß, was er mit einer Zahl anfangen soll.

### In der Oberfläche

Eine Liste mit `enum` wird als Checkboxen dargestellt, die kein JavaScript
brauchen und die der Browser mit einem Wert pro angekreuztem Kästchen sendet.
Ohne `enum` ist es ein Textfeld, dessen Elemente zu entfernbaren Chips werden,
und bei ausgeschaltetem JavaScript bleibt es ein Feld mit `users,orders` – genau
das, was der Server erwartet.

### Auf der Kommandozeile

```bash
brevis run wf.yaml --param tables=events,sessions --param days=1,30
```

:::note Eine ältere Engine liest das nicht
Der Typ wird mit dem Workflow gespeichert, und eine Engine aus der Zeit vor
diesem Feature verweigert `list|string` beim Veröffentlichen als unbekannten
Typ. Aktualisieren Sie die Engine, bevor Sie einen Workflow veröffentlichen,
der einen solchen verwendet.
:::

## Im Befehl verwenden

Der Wert gelangt über ein Template in `run`, in doppelten geschweiften Klammern
mit einem Punkt:

```yaml
run: ./load.sh --since {{ .start_date }}
```

## Auf der Kommandozeile übergeben

```bash
brevis run wf.yaml --param load_full=true
brevis run wf.yaml --param load_full=true --param start_date=2026-01-01
```

`--param` ist wiederholbar. Eine Eingabe ohne `=` ist ein **Fehler**:

```
erro: --param "load_full": use chave=valor
```

Das ist Absicht. `--param load_full`, bei dem der Wert vergessen wurde, liefe
mit dem Standard – und der Operator würde glauben, der Backfill sei mit dem
beabsichtigten Parameter erfolgt.

## In einem Backfill

Die Werte gelten für **jeden** Slot im Bereich. Das ist der zentrale Anwendungsfall:
„den ganzen Januar mit `load_full=true` neu verarbeiten“.

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

## In der Oberfläche

Ein Workflow mit `params` erhält ein **Formular** anstelle des einfachen
Auslöse-Buttons. Die Felder stammen aus der Deklaration, und `pattern` wird vor
dem Absenden validiert.

## In der Step-Umgebung

Run-Parameter gelangen, mit Präfix, auch in die **Umgebung** jedes Steps. Das
gibt es, damit ein mit dem [SDK](/docs/sdk/) geschriebener Fetcher sie sehen
kann, ohne etwas als Argument zu erhalten:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    if p.Run.Params["load_full"] == "true" {
        p.Source.URL += "&full=1"
    }
    return nil
},
```

## Auto-Parameter

Jeder Run trägt außerdem **Auto-Parameter**: Werte, die die Engine selbst
ermittelt. Niemand deklariert sie, jeder Run hat sie, und sie stehen auf dem
Bildschirm des Runs.

Der wichtigste ist `adjusted_at`, und der Fehler, den er beseitigt, verdient
einen Namen. Ein Fetcher liest `now()`, zieht sein Fenster ab und fragt den
Anbieter nach den letzten zwei Stunden. Bei einem Run, der pünktlich startet,
stimmt das. Bei einem Run, den die Queue um vierzig Minuten verzögert hat, liegt
es um vierzig Minuten daneben – und diese vierzig Minuten gehören **zu keinem
Run**, weil der nächste Slot ebenfalls sein eigenes `now()` liest. Nichts
schlägt fehl; die Daten fehlen einfach, und man findet es Wochen später.

Die Engine reicht deshalb stattdessen eine Uhr weiter. Lesen Sie `adjusted_at`
dort, wo Sie `now()` gelesen hätten:

| Parameter | | |
|---|---|---|
| `adjusted_at` | immer | die Uhr, die dieser Run lesen soll: der Slot, wenn es einen gibt, sonst der Start |
| `date` | immer | `adjusted_at` als `YYYY-MM-DD`, UTC – die Partition, der Ordner, das `WHERE` |
| `delay_seconds` | immer | wie spät dieser Versuch gegenüber seinem Slot war; `0` bei einem manuellen Run ohne Slot |
| `previous_error` | immer | der Run vor diesem war nicht erfolgreich |
| `scheduled_at` | mit Slot | der Slot, den der Cron angefordert hat |
| `started_at` | | wann dieser Versuch begann; ein Retry verschiebt ihn |
| `interval_start` | mit Slot | der **vorherige** Slot |
| `interval_end` | mit Slot | dieser Slot, **ausgeschlossen** |
| `previous_success_at` | | der Slot des letzten Runs, der erfolgreich war |

Ein Run hat einen Slot, wenn der Zeitplan ihn erzeugt hat, wenn ein Backfill ihn
erzeugt hat oder wenn er von Hand **für einen vergangenen Slot** ausgelöst
wurde: Das Feld `slot` der Auslösung (`2026-10-27T04:00:00Z`) führt diesen Slot
erneut aus, mit den Parametern dieses Slots. Ein Slot, den der Cron nie feuert,
wird verweigert, wobei der nächstliegende davor genannt wird.

**Jeder Zeitstempel hier ist UTC**, als RFC 3339 mit einem `Z` geschrieben, und
der Bildschirm des Runs zeigt sie ebenfalls in UTC – damit die Seite und
`$BREVIS_AUTO_ADJUSTED_AT` nie voneinander abweichen. `date` ist der **UTC-Tag**,
der bei einem Workflow, dessen Slot die UTC-Mitternacht überschreitet, nicht der
lokale Tag ist: Ein `0 22 * * *` in `America/Sao_Paulo` hat ein `date` des
Folgetages. Airflows `ds` verhält sich genauso, aus demselben Grund – der Slot
ist ein Zeitpunkt, und ein Zeitpunkt hat erst dann einen einzigen Tag, wenn eine
Zeitzone gewählt ist.

`interval_start` und `interval_end` stammen aus dem **Cron**, nicht aus der
Historie: Ein Backfill eines Slots vom März erzeugt das Fenster, das der März
hatte, nicht das Fenster, das die Runs dieses Workflows heute zufällig
beschreiben. Eine Pipeline, die `[start, end)` anfordert, überlappt nie und lässt
nie Lücken, egal wie spät sie läuft und wie oft sie wiederholt.

Sie sind ein **Snapshot**, beim Start des Runs berechnet und auf ihm gespeichert.
`previous_error` ist eine Tatsache über den Moment, in dem dieser Run begann;
es morgen neu zu berechnen – nachdem der vorherige Run wiederholt wurde und
durchlief – würde eine andere Frage unter demselben Namen beantworten.

### Sie lesen

In einem Shell-Step eine Variable pro Wert, sodass nichts geparst werden muss:

```yaml
steps:
  - id: extract
    run: |
      curl "https://api.example.com/events?since=$BREVIS_AUTO_INTERVAL_START&until=$BREVIS_AUTO_INTERVAL_END" \
        > "/data/$BREVIS_AUTO_DATE.json"
```

Die gesamte Menge steht außerdem in `$BREVIS_AUTO_PARAMS` als ein
JSON-Objekt, für einen Step, der lieber einmal parst.

In Go liest das [SDK](/docs/sdk/) sie für Sie:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    start, end, ok := p.Run.Auto.Window()
    if !ok { // no schedule: fall back to a fixed window
        start, end = p.Run.Auto.Now().Add(-24*time.Hour), p.Run.Auto.Now()
    }
    p.Source.URL += "&from=" + start.Format(time.RFC3339) +
        "&to=" + end.Format(time.RFC3339)
    return nil
},
```

`Auto.Now()` ist die Uhr, die statt `time.Now()` zu lesen ist. Außerhalb der
Engine – ein Fetcher, den jemand von Hand startet – *ist* sie die Wanduhr, sodass
für die lokale Entwicklung nichts als Sonderfall behandelt werden muss.

In Python liest `pip install brevis` dieselben Werte:

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

`window()` liefert `None`, wenn der Workflow keinen Zeitplan hat, statt eines
Paars aus Nullzeiten – eine Abfrage ab der Epoche wählt alles aus, und dieser
Fehler sollte nicht still bleiben. Das `or` oben ist der gesamte Fallback.

### In dlt gibt es gar nichts zu lesen

[dlt](https://dlthub.com) führt seinen eigenen Cursor, in einem Zustand, den es
ins Ziel schreibt. Dieser Zustand bewegt sich nur **vorwärts**, was in Ordnung
ist, bis Sie einen alten Slot erneut ausführen: Der Cursor sagt weiterhin
heute, der Backfill liest also das Fenster von heute, und dlts eigene Antwort
darauf ist, den Zustand zu verwerfen.

Das Fenster, das Brevis weiterreicht, stammt aus dem Cron, sodass der Märzlauf
das Fenster des März erzeugt, wie oft er auch wiederholt wird. dlt nimmt es an –
die Namen sind seine eigenen, vor der Suche nach Airflow gelesen, und die Engine
setzt sie bei jedem geplanten Run:

```python
@dlt.resource(primary_key="id")
def events(
    updated_at=dlt.sources.incremental(
        "updated_at",
        initial_value=datetime(1970, 1, 1, tzinfo=timezone.utc),
        allow_external_schedulers=True,   # this line is the whole integration
    )
):
    yield from api.events(since=updated_at.start_value, until=updated_at.end_value)
```

`allow_external_schedulers=True` ist die ganze Integration. `$DLT_INTERVAL_START`
und `$DLT_INTERVAL_END` werden zu dlts `initial_value` und `end_value`,
halboffen auf beiden Seiten – die Zeile genau am Anfang ist drin, die genau am
Ende nicht, wodurch aufeinanderfolgende Slots weder Lücken lassen noch
überlappen.

Ein Incremental mit einem `end_value` **berührt den persistierten Zustand
nicht**. Das ist der Teil, auf den es ankommt: Einen Slot erneut auszuführen ist
idempotent, und ein Backfill des März verschiebt nicht den Cursor, von dem der
nächtliche Run abhängt.

Zwei Dinge sind zu wissen, bevor es funktioniert:

- **Der Cursor muss ein Datetime sein, kein String.** dlt wandelt einen
  `text`-Cursor für den Vergleich nicht um und weigert sich, dem Scheduler
  überhaupt beizutreten, mit einem `JoinSchedulerError`, der das nicht
  offensichtlich sagt. Konvertieren Sie ihn an der Resource – `add_map` – oder
  typisieren Sie den `initial_value` wie oben.
- **Ein Workflow ohne Zeitplan reicht nichts weiter**, und dlt wirft dann
  `ExternalSchedulerNotAvailable`, statt still auf seinen Cursor zurückzufallen.
  Soll eine Pipeline in beiden Fällen funktionieren, lassen Sie
  `allow_external_schedulers` aus und übergeben das Fenster selbst über
  `run.window()`.

`tools: [dlt]` am Step zu deklarieren ist davon getrennt und zeichnet nur den
Chip im Graphen – das Fenster wird in beiden Fällen weitergereicht.

## Der Snapshot

Parameter werden **im Run** gespeichert, nicht zur Ausführungszeit aus dem
Workflow gelesen. Der Run trägt die Eingabe, mit der er ausgelöst wurde, und das
Log einer Ausführung im Januar zeigt weiterhin die Werte des Januars, auch
nachdem sich der Standard geändert hat.

## Nächste Schritte

- [Konfiguration](/docs/configuration/) – Umgebungsvariablen des Prozesses
- [SDK](/docs/sdk/) – wie ein Fetcher den Run-Kontext liest
- [Kontext zwischen Steps](/docs/context/) – was ein Step für den nächsten veröffentlicht
