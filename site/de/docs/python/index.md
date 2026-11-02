# Python

> pip install brevis – Kontext zwischen Steps, die Uhr des Runs und die eigenen Metriken Ihrer Pipeline, ganz ohne Abhängigkeiten.

*https://brevis.sh/de/docs/python/ · brevis.sh docs (de)*

---

```bash
pip install brevis
```

```python
from brevis import context, metrics, run

bucket = context.get("extract.bucket")
since, until = run.window()          # the window this run covers

context.set(rows=48213, watermark="2026-09-07T03:00:00Z")
metrics.set("rows_loaded", 48213)    # reaches the engine's /metrics
```

**Keine Abhängigkeiten, niemals.** Es liest eine Umgebungsvariable, schreibt eine
Datei und gibt eine Zeile auf stdout aus. Erfordert Python 3.10+.

Es ist keine Portierung des Go-SDK: keine Treiber, keine Paginierung, keine
Ingestion-Ids. Es existiert, damit ein Team pandas, Polars oder dbt nutzen kann
und dennoch bekommt, wofür ein Orchestrator da ist.

## Kontext lesen

Ein Schlüssel wird immer durch den Step qualifiziert, der ihn veröffentlicht hat:

```python
context.get("extract.bucket")            # the value
context.get("extract.nope", default=0)   # absent is not an error
context.of("extract")                    # everything that step published
context.published()                      # what THIS step has published so far
```

`context.get("bucket")` ohne Step wird **verweigert**, wobei die Steps genannt
werden, die ihn veröffentlicht haben. Steps sind isoliert – `extract` und
`transform` können beide `bucket` veröffentlichen, ohne dass einer ihn verliert –,
und ein nackter Schlüssel wirft das in dem Moment weg, in dem es zwei tun. Zu
suchen und einen auszuwählen ist Vorrang durch Zufall: Es funktioniert, bis es
still nicht mehr funktioniert.

Einen Step zu lesen, von dem dieser nicht abhängt, ist ein Fehler, und die
Meldung weist auf die Lösung.

## Schreiben

```python
context.set(name="Daniel")
context.set(label="Name")
# both survive: {"name": "Daniel", "label": "Name"}
```

Aufrufe **führen zusammen**. Derselbe Schlüssel zweimal behält den letzten
Schreibvorgang; ein anderer Schlüssel lässt den ersten in Ruhe. **Es gibt kein
Step-Argument** – der einzige Namensraum, den ein Prozess schreiben kann, ist sein
eigener, und genau das macht die Isolation strukturell statt zu einer Regel.

Nichts erreicht die Platte, bevor der Prozess endet, sodass ein Step, der hart
stirbt, nichts veröffentlicht. Das ist korrekt: Die Ausgabe eines abgestürzten
Steps beschrieb Arbeit, die nicht fertig wurde.

## Die Uhr, die dieser Run lesen soll

Jeder Run trägt außerdem **Auto-Parameter**: Werte, die die Engine selbst
ermittelt. Niemand deklariert sie, jeder Run hat sie, und sie beantworten die
Frage, die eine Pipeline sonst mit `datetime.now()` beantworten würde.

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

:::warning Warum nicht `datetime.now()`
`run.now()` ist bei einem geplanten Run der **Slot**. Ein Fetcher, der
`datetime.now()` liest und zwei Stunden abzieht, liegt bei einem pünktlich
startenden Run richtig und bei einem, den die Queue verzögert hat, um vierzig
Minuten daneben – und diese vierzig Minuten gehören **zu keinem Run**, weil der
nächste Slot ebenfalls sein eigenes `now()` liest. Nichts schlägt fehl, und die
Lücke wird Wochen später gefunden.
:::

| | |
|---|---|
| `run.now()` | die Uhr: der Slot oder der Zeitpunkt, zu dem der Run begann |
| `run.window()` | `(start, end)`, end ausgeschlossen – oder `None` |
| `run.auto()` | die Auto-Parameter, unten |
| `run.context()` | Id, Versuch, Auslöser, Parameter und Auto |
| `run.params()` · `run.param(name)` | die im Workflow deklarierten Parameter |
| `run.map_index()` · `run.map_value()` | die Instanz, unter `for_each:` |

`window()` stammt aus dem **Cron**, nicht aus der Historie: Ein Backfill eines
Slots vom März erzeugt das Fenster, das der März hatte. Wer `[start, end)`
anfordert, überlappt nie und lässt nie Lücken, egal wie spät der Run ist und wie
oft er wiederholt wird.

Es liefert `None` statt eines Paars aus Nullzeiten, wenn der Workflow keinen
Zeitplan hat – eine Abfrage ab der Epoche wählt alles aus, und dieser Fehler
sollte nicht still bleiben. Das `or` oben ist der gesamte Fallback.

### Die Auto-Parameter

| Feld | |
|---|---|
| `date` | `"2026-09-08"` – die Partition, der Ordner, das `WHERE` |
| `adjusted_at` | die Uhr, die statt `now()` zu lesen ist |
| `scheduled_at` | der Slot, den der Cron angefordert hat; `None` bei einem manuellen Run |
| `started_at` | wann **dieser Versuch** begann; ein Retry verschiebt ihn |
| `delay_seconds` | wie spät dieser Versuch war; nie negativ |
| `interval_start` · `interval_end` | das Fenster, end ausgeschlossen |
| `previous_error` | der Run vor diesem war nicht erfolgreich |
| `previous_success_at` | der Slot des letzten, der es **war** – was `previous_error` handlungsfähig macht |

Führen Sie das Skript von Hand aus, **ist** `run.now()` die Wanduhr und
`window()` ist `None`, sodass für die lokale Entwicklung kein Zweig zu schreiben
ist.

## Eine Instanz eines abgebildeten Steps

Unter [`for_each:`](/de/docs/workflows/index.md) führt die Engine einen Prozess pro Element
aus und sagt jedem, welches er erhalten hat:

```python
partition = run.map_value()
if partition is None:
    raise SystemExit("this step is meant to run under `for_each:`")

load(f"/data/{partition}.csv")
```

Ein JSON-String kommt **ohne Anführungszeichen** an – `for_each` über
`["2026-01"]` gibt dem Step `2026-01`, nicht `"2026-01"`. Alles andere – ein
Objekt, eine Zahl, eine Liste – kommt als sein JSON an, also
`json.loads(run.map_value())`.

Beide sind `None` bei einem Step, der nicht abgebildet ist. `None`, und nicht `-1`
oder `""`: Ein Sentinel ist eine Zahl, mit der irgendwann jemand rechnet, und ein
leerer String ist nicht von einem Element zu unterscheiden, das der leere String
*ist*.

Ein abgebildeter Step veröffentlicht **keinen Kontext** nach unten – vier
Instanzen können sich keinen Schlüssel teilen. Ein Step, der etwas weitergeben
muss, schreibt eine Datei, oder ein Step danach zählt, was gelandet ist.

## Metriken, die nur Ihre Pipeline kennt

Die Engine misst bereits, was sie sehen kann: Dauer, Versuche, Queue-Tiefe. Was
sie nicht wissen kann, ist, wie viele Zeilen gelandet sind, wie viele der Anbieter
abgelehnt hat oder wie viele Byte verworfen wurden. Das weiß der Step, und die
Bibliothek trägt es.

```python
from brevis import metrics

metrics.set("rows_loaded", 48213)          # a gauge: the last value wins
metrics.inc("vendor_rejected_total")       # a counter: it adds up
metrics.inc("bytes_discarded_total", 4096)
```

Sie kommen aus dem `/metrics` der **Engine**, auf dem Port des Schedulers, mit
Workflow und Step als Labels:

```
brevis_step_rows_loaded{workflow="daily_sales",step="load"} 48213
brevis_step_vendor_rejected_total{workflow="daily_sales",step="load"} 5
```

| | |
|---|---|
| `metrics.set(name, value)` | ein Gauge. Nachdem der Step endet, bleibt der Wert, bis der nächste Run einen anderen schreibt |
| `metrics.inc(name, by=1)` | ein Counter. Summiert sich über Steps und über Runs |

### Warum das keinen Port öffnet

Weil **ein Step nicht abfragbar ist**. `:9090` gehört dem Scheduler und der
API – langlebigen Prozessen mit stabiler Adresse, alle fünfzehn bis sechzig
Sekunden abgefragt. Ein Step startet in einem eigenen Pod, läuft vierzig Sekunden
und endet: Ein Port, den er öffnete, würde nie abgefragt oder einmal durch
Zufall.

Die drei bekannten Antworten darauf sind ein Pushgateway (eine Komponente, die
betrieben werden muss, und Serien, die bleiben, bis jemand sie löscht), ein
OTLP-Collector (eine Komponente **und** eine Abhängigkeit in dieser Bibliothek,
die keine hat und keine bekommen wird) oder den **Orchestrator** sie tragen zu
lassen. Brevis liest ohnehin die stdout jedes Steps und sucht `@brevis:`-Zeilen –
so gelangen die Phasen des SDK in den Graphen –, und es hat bereits einen Meter
und einen Prometheus-Endpoint.

Die Bibliothek schreibt also **eine Zeile nach stdout**, und die Engine erledigt
den Rest. Die Metrik kommt kostenlos mit Workflow und Step als Labels an, weil die
Engine weiß, was sie ausführte; über ein Pushgateway müsste diese Bibliothek sich
selbst labeln und würde es falsch machen.

Der Schreibvorgang ist `print(flush=True)`, denn die Engine liest diese Leitung
**live**. Eine gepufferte Zeile kommt an, wenn der Prozess endet – bei einem Step,
der eine Stunde läuft, also eine Stunde zu spät, und bei einem Step, der beendet
wird, nie.

### Was eine Metrik verweigert

**Ein ungültiger Name wird laut verweigert.** Prometheus akzeptiert Buchstaben,
Ziffern und Unterstrich, und das erste Zeichen darf keine Ziffer sein. Ein Name,
den es ablehnt, erzeugt keine kaputte Metrik – er erzeugt eine Ausgabe, die es
**zu parsen verweigert**, und der **gesamte Scrape** geht verloren, jede andere
Metrik mit ihm. Deshalb löst `rows-loaded` hier `MetricError` aus, wo der Fehler
gemacht wurde, statt unter einem Namen anzukommen, den niemand geschrieben hat.

| Verweigert | Weil |
|---|---|
| `rows-loaded`, `rows loaded`, `2rows` | Prometheus parst es nicht, und der gesamte Scrape geht verloren |
| `metrics.set("ok", True)` | `bool` ist in Python ein `int`, und `1` zu melden ist eine Zahl, deren Bedeutung davon abhängt, das zu wissen. Melden Sie die Anzahl oder die Dauer, nicht den Zustand |
| `metrics.inc("x", -1)` | ein Counter geht nur nach oben. Jedes `rate()` darüber setzt das voraus, und der Bruch zeigt sich Wochen später in einem Graphen |

Führen Sie das Skript von Hand aus, geht die Zeile weiterhin nach stdout, wo sie
sich als das liest, was sie ist. Nichts sammelt sie ein und nichts schlägt fehl –
genau wie bei `context.set()` auf einem Laptop.

## Was es verweigert

| | Weil |
|---|---|
| mehr als 4096 Byte | die Plattform **schneidet ab**, statt zu verweigern, sodass ein zu großes Objekt in der Mitte durchgeschnitten ankommt und sich nachgelagert als „nichts veröffentlicht“ liest |
| ein Wert, der kein JSON ist | ein `datetime` wird zu `"..."` oder verschwindet, je nachdem, wer serialisiert. Der Fehler nennt den Schlüssel und schlägt `.isoformat()` vor |
| ein Schlüssel, der kein String ist | `{1: "x"}` kommt als `{"1": "x"}` zurück, und die Suche läuft ins Leere |

Die Obergrenze ist die Zahl der Plattform, nicht unsere. Jedes Feature für
„Kontext zwischen Tasks“ scheitert auf dieselbe Weise – jemand legt **Daten**
dorthin, wo nur **Kontext** passt. Vier Kilobyte fassen ein Watermark, eine Anzahl
oder eine Liste von Partitionen. Veröffentlichen Sie eine Referenz und lassen Sie
die Zeilen, wo sie sind.

:::danger Das ist kein Secret-Speicher
Alles Veröffentlichte ist für jeden sichtbar, der den Run sehen kann – es steht im
Status des Pods und in der Run-Historie. Veröffentlichen Sie einen Pfad, keine
signierte URL.
:::

## Von Hand ausführen

Außerhalb von Brevis gibt es keine Eingabe, und nichts liest die Ausgabe. `get()`
liefert den Standardwert, `set()` nimmt an und verwirft, und die Bibliothek sagt
das einmal auf INFO. Sie **wirft keine Ausnahme**: Ein Skript, das sich nicht von
Hand ausführen lässt, lässt sich nicht entwickeln. Die Validierung läuft weiterhin,
sodass ein Wert, der auf Ihrem Laptop funktioniert, auch in Produktion
funktioniert.

## Ihren Step testen

Der gesamte Vertrag besteht aus zwei Umgebungsvariablen, es gibt also nichts zu
mocken:

```python
def test_my_step(monkeypatch, tmp_path):
    monkeypatch.setenv("BREVIS_INPUT", '{"extract": {"rows": 42}}')
    monkeypatch.setenv("BREVIS_OUTPUT", str(tmp_path / "out"))
    assert context.get("extract.rows") == 42
```

## Versionen

Die Bibliothek versioniert für sich, getrennt von der Engine und vom Go-SDK.

:::warning Verwenden Sie nicht `0.2.0`
Sie wurde nur als Wheel auf PyPI ausgeliefert. `0.2.1` ist derselbe Code,
vollständig.
:::

`brevis.run` braucht eine Engine ab **`v0.9.0`**, um etwas zum Lesen zu haben; bei
einer älteren ist jedes Feld leer und `run.now()` die Wanduhr – dasselbe, was es
außerhalb der Engine tut.

## Nächste Schritte

- [Client-Bibliotheken](/de/docs/libraries/index.md) – der Vertrag und die anderen Sprachen
- [Kontext zwischen Steps](/de/docs/context/index.md) – das Konzept
- [Parameter](/de/docs/parameters/index.md) – die deklarierten
