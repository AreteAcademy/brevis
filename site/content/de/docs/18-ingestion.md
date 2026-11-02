---
title: Ingestion
description: Ein HTTP-Endpoint, der Daten landet – validieren, identifizieren, batchen, zustellen. Eigenes Image, eigene Version.
group: Ingestion
order: 18
slug: ingestion
---

Das Ingestion-Gateway ist ein **HTTP-Endpoint, der Daten landet**. Ein Client
sendet einen `POST`, erhält eine `202`, und was ankam, wird zu einem Datensatz in
einem Topic, in einer Tabelle oder in einem Bucket.

![Der Ablauf des Gateway: Ein POST an /v1/clicks wird dekodiert, erhält seine ingestion_id, wird sofort mit 202 beantwortet und an einen Batch angehängt; ein Worker stellt den Batch an die Sink zu, hier Dateien. Das Gateway hat keine Datenbank.](/assets/flow-gateway.svg)

Es ist **ein anderes Binary, ein anderes Go-Modul, eine andere Version und ein
anderes Image**. Die Engine orchestriert und berührt nie Kundendaten; das Gateway
tut nichts anderes. Die Nummerierung ist mit Absicht unabhängig: Die Engine steht
bei 0.17 und das Gateway bei 0.27, was die wahre Aussage ist.

Mit der `gateway.yaml` aus dem Abschnitt unten und dem Schlüssel, den sie in `keys_from` verlangt:

```bash
docker run -p 8080:8080 -e BREVIS_INGEST_KEYS=dev-key \
  -v ./gateway.yaml:/etc/brevis/gateway.yaml:ro \
  brevissh/brevis-gateway:0.32.0
```

```text
2026/10/09 01:03:01 drain budget 30s; set terminationGracePeriodSeconds >= 40
2026/10/09 01:03:01 connections idle out after 1m30s and are recycled after 1m0s
2026/10/09 01:03:01 listening on :8080
2026/10/09 01:03:01 metrics on :9090/metrics
```

Ein `POST` kommt sofort mit `202` zurück:

```bash
curl -X POST localhost:8080/v1/clicks -H 'Authorization: Bearer dev-key' \
  -d '{"event_id":"e-1","occurred_at":"2026-10-08T12:00:00Z","page":"/pricing"}'
```

```text
{"accepted":1,"rejected":null}
```

Ohne Google-Cloud-Zugangsdaten wird der Batch wiederholt und landet in der Dead
Letter – siehe [Wenn die Sink verweigert](#when-the-sink-refuses).

Das `-slim`-Image enthält nur die Sinks `auto_table`, `files` und `postgres`;
das `pubsub` dieses Beispiels und die übrigen sind im vollen Image. Ein Slim-Image
mit einer Sink, die es nicht enthält, wird beim Start verweigert, mit dem Grund.

## Die Datei ist der Vertrag

Es gibt keinen Code zu schreiben außer den Hooks. Streams, Pfade, Formate,
Identität, Retry, Ziel und Dead Letter – alles im YAML, und jedes Feld wird
verweigert, wenn es sich nicht einhalten lässt.

```yaml
name: events_gateway

listen:
  addr: :8080
  max_body: 1MiB
  idle_timeout: 90s                  # the default; it cannot be turned off
  max_conn_age: 60s                  # off unless set — see below
  auth:
    type: bearer
    keys_from: BREVIS_INGEST_KEYS   # the NAME of the variable, never the keys

metrics:
  addr: :9090                        # its own port, never the ingest one

streams:
  - name: clicks
    path: /v1/clicks
    format: json                     # json | array | ndjson — declared, never sniffed

    identity:
      provider: web
      entity: click
      source_key: event_id
      record_ts: occurred_at

    buffer:
      flush: {every: 1s, records: 500}

    sink:
      type: pubsub
      project: acme-prod
      topic: clicks

    dead_letter:
      type: files
      path: /var/dead-letter/
```

`format` wird deklariert und nie erraten: Raten ist der Weg, auf dem aus einem
Batch von tausend eine Zeile wird, die ein Array hält.

## Die `ingestion_id`, um die es geht

Jeder Datensatz verlässt das Gateway mit einer `ingestion_id`: eine
**unveränderliche UUID v5** über `provider|entity|source_key|record_ts`.

Es ist **dieselbe Id**, die ein Batch-Fetcher für denselben Datensatz berechnet.
Eine Zeile, die das Gateway landet, und eine Zeile, die eine
[SDK](/docs/sdk/)-Pipeline landet, sind *eine Zeile*, ohne Abgleich dazwischen.

Zwei praktische Folgen:

- **Ein wiederholter `POST` ist derselbe Datensatz.** Ein Client kann ohne Sorge
  erneut senden; ein Ziel mit `merge` absorbiert die Wiederholung, statt eine
  zweite Zeile zu schreiben.
- **Die Formel ist unveränderlich.** Alle vier Felder sind erforderlich, und
  eines wegzulassen erzeugt eine *andere* Id statt einer schwächeren. Die Datei
  verweigert die unvollständige Konfiguration, statt sie zu akzeptieren.

Ingestion-Werkzeuge bewegen Bytes sehr gut und haben keine Meinung dazu, was ein
Datensatz *ist*. Diese Meinung fügt das Gateway hinzu.

## Die Anfrage wartet nie auf die Sink

Es dekodiert, führt den Hook aus, berechnet die Identität, hängt an einen Puffer
an und gibt zurück. **Das ist alles.** Ein voller Batch geht an einen Worker-Pool,
und das Veröffentlichen, das `COPY`, die Retries und die Dead Letter geschehen
alle dort.

Das ist nicht kosmetisch. Bevor es das gab, führte die Anfrage, die zufällig einen
Batch füllte, die Zustellung inline aus – ein Aufrufer von je `flush.records`
zahlte also die volle Round-Trip-Zeit, und bei ausgefallener Sink das gesamte
Retry-Fenster. Ein p99, das davon geprägt ist, welcher Aufrufer Pech hatte, ist
kein p99, auf das jemand reagieren kann.

```yaml
buffer:
  flush:
    every: 1s         # age
    records: 500      # count
    size: 8MiB        # bytes — whichever is crossed FIRST sends the batch
    max_age: 30s      # and this one sends it REGARDLESS
  workers: 4          # batches delivered at once
  queue: 64           # full batches that may wait for a worker
  max_records: 10000  # events held in memory before the gateway says no
  max_bytes: 256MiB   # the same ceiling in bytes
```

### Die Flush-Uhr gehört dem Deployment

`buffer.flush.every` ist ein `time.AfterFunc` in **jeder Replica**. Die Load-Jobs,
die eine Tabelle erhält, sind daher eine Funktion der Replica-Anzahl, nicht des
Datenverkehrs:

```
jobs / day / table  =  replicas × 86400 / every
```

Bei BigQuery sind gegenüber 1.500 pro Tabelle und Tag **zwei Replicas bei 60s
bereits 192 % einer Quote, die sich nicht erhöhen lässt** – bevor ein HPA
irgendetwas tut. Und die Richtung ist entgegengesetzt zur Intuition: Das Gateway
skaliert für HTTP-Nebenläufigkeit aus, während die Sink Konzentration will.

```yaml
buffer:
  flush:
    every: 60s
    max_age: 300s
    claim: true
```

Mit `claim` nimmt eine Replica vor dem Flush auf dem Timer ein Set-if-absent für
Stream und Fenster vor – und der Timer wacht an der **Fenstergrenze** auf, sodass
alle Replicas nach demselben Schlüssel fragen. Ein Flush pro Fenster, gleich
welche Replica-Anzahl. Und es stellt sich selbst ein: Eine Replica wartet nur,
wenn tatsächlich eine andere konkurriert, sodass das Einskalieren die geringere
Latenz wiederherstellt, ohne dass jemand eine Konfiguration bearbeitet.

Gemessen mit zwei Replicas und einem Redis, 5s-Fenster: **aus 8 Load-Jobs wurden
6, einer pro Fenster, abwechselnd 3/3** – kein Fenster mit zwei.

**Es braucht ein geteiltes Backend.** Mit `memory` gewinnt jede Replica ihren
eigenen Claim, und nichts koordiniert. Und es **verlangt `max_age`**, was die
Konfiguration ohne verweigert: Der Claim macht `every` zu einem Ziel, und ein Ziel
ohne Obergrenze ist Latenz ohne Begrenzung.

**Ein nicht erreichbarer Metastore flusht trotzdem.** Koordination zu verlieren
kostet Load-Jobs; den Flush zu verweigern hält Daten zurück. Verifiziert durch das
Killen von Redis mitten im Lauf: 40 von 40 Events landeten, nichts wurde
zurückgehalten.

**`outcome="unreachable"` ist die, die einen Alarm wert ist.** Das Backend ließ
sich nicht befragen, der Claim schlug also fail-open fehl: Die Koordination ist
aus, und die Load-Jobs vervielfachen sich wieder mit der Replica-Anzahl – still,
weil jeder Flush weiterhin wie ein Flush aussieht.

Und der Claim gibt **Exklusivität, keine Fairness**: Wer zuerst fragt, gewinnt.
Ein echter Lauf mit zwei Replicas maß 4/1, keine gleichmäßige Aufteilung.
`max_age` verhindert, dass die Benachteiligte verhungert.

Mit `claim: true` und `metastore: memory` warnt das Gateway **einmal beim
Start**: Jede Replica gewinnt ihren eigenen Claim, sodass alles richtig aussieht
und nichts koordiniert.

### `every` ist ein Ziel; `max_age` ist ein Versprechen

Die drei obigen Trigger entscheiden, wann ein Batch zu gehen **versucht**.
`max_age` entscheidet, wann er **unabhängig davon** geht.

Heute ist der Unterschied wirkungslos, weil nichts das Ziel aufschiebt. Er beginnt
zu zählen, sobald die Flush-Uhr nicht mehr pro Prozess läuft:
`buffer.flush.every` ist ein `time.AfterFunc` in jeder Replica, die Anzahl der
Load-Jobs, die eine Tabelle erhält, ist also eine Funktion der **Replica-Anzahl**,
nicht des Datenverkehrs – `replicas × 86400 / every`. Bei BigQuery sind
gegenüber 1.500 pro Tabelle und Tag **zwei Replicas bei 60s bereits 192 % der
Quote**.

Sobald eine Replica ein Fenster an eine andere abgeben kann, füllt sie weiter,
und `max_age` verhindert, dass jemand Daten unbegrenzt zurückhält, während er auf
seinen Zug wartet.

Es regelt auch den Drain: Was ein Puffer bei `SIGTERM` **halten** kann, wird
dadurch begrenzt, und das `terminationGracePeriodSeconds`, das das Gateway beim
Start ausgibt, wird davon abgeleitet.

Eine Obergrenze **unter** dem Ziel wird beim Laden verweigert – sie würde
bedeuten, dass das Ziel nie gilt.

### Warum es einen Größen-Trigger gibt

Die anderen beiden sind **Zählungen**, und eine Zählung kann 500 Events zu 2 KB
nicht von 500 zu 2 MB unterscheiden – 1 MB gegen 1 GB, dieselbe Zahl in der
Datei. `max_records: 10000` hat denselben blinden Fleck: 20 MB oder 20 GB, und
nichts im YAML könnte sagen, welches.

Ein Produzent, der anfängt, das ganze Dokument statt seiner Id zu senden, reißt
den Pod um, und bevor es das gab, **hätte kein Feld die Grenze ausdrücken
können**.

Die Größe wird an dem gemessen, was **ankam** – nicht an dem, was das Event im
Speicher wiegt (eine `map[string]any` ist ein Mehrfaches ihres JSON) und nicht
nach einem Hook, der es aufbläht. Eine Näherung, und die richtige: Sie ist beim
Dekodieren kostenlos, sie bewegt sich mit der Payload, und wogegen sie schützt,
ist ein Datensatz, der auf das Zehnfache wuchs.

**Bei BigQuery ist `size` eine Obergrenze, kein Ziel.** Dieses Ziel erlaubt 1.500
Load-Jobs pro Tabelle und Tag, weshalb `every` eine Untergrenze von 60 Sekunden
hat. Aber die Untergrenze regelt den **Timer**: Eine Größe, die alle paar
Sekunden feuert, läuft geradewegs daran vorbei und verbraucht dieselbe Quote.
Nichts verweigert das beim Laden, weil die Ankunftsrate dort nicht bekannt ist.
Stattdessen gibt es Sichtbarkeit:

```
brevis_gateway_flushes_total{stream,trigger}   trigger = time | records | size
```

Ein steigendes `trigger="size"` bei einem BigQuery-Stream sagt Ihnen, dass nicht
das Fenster das Batching bestimmt.

Dieser Zähler gilt pro Stream, und die Quote gilt pro **Tabelle**: Mit
`auto_table` wird eine Route zu vielen Tabellen, und 94 von ihnen, die eine Zahl
melden, sagen über keine einzelne etwas. Hinter `BREVIS_INGESTION_METRICS` gibt
es einen, der sie benennt:

```
brevis_gateway_table_flushes_total{stream,table,trigger}
```

Ein Flush einer Tabelle ist ein Load-Job für sie, dies ist also die Serie, bei
der man bei 1.200 der 1.500, die BigQuery erlaubt, alarmieren sollte. Die
bedingungslose Hälfte ist eine Zeile beim Start, die sagt, was `flush.every` pro
Tabelle und Tag kostet, bevor ein Event ankommt.

**Es ist begrenzt, nicht fire-and-forget.** Eine Goroutine pro Batch würde aus
einem Sink-Ausfall unbegrenzten Speicher machen. Sind die Queue und der Puffer
beide voll, lautet die Antwort **`503` mit `Retry-After`**, statt einer `202` für
ein Event ohne Ziel. Ein akzeptiertes Event, das nie zugestellt wird, ist das eine
Ergebnis, das es dieser Dienst nicht geben soll.

Und dieses `503` ist **sicher zu wiederholen**, auf eine Weise, die fast kein
Dienst für sich beanspruchen kann: Die `ingestion_id` ist eine unveränderliche
Funktion des Events selbst, derselbe erneut gesendete Body ist also der *gleiche
Datensatz*.

## Eine Verbindung entscheidet, welche Replica die Last bekommt

Hinter einem Kubernetes-`ClusterIP`-Service wählt kube-proxy ein Backend **einmal
pro TCP-Verbindung**, nicht pro Anfrage. Die meisten Clients halten Verbindungen
offen und verwenden sie wieder, sodass ein Produzent mit einer Handvoll davon
alles an dieselbe oder zwei Replicas sendet, wie viele auch laufen – und eine
Replica, die der HPA hinzufügt, erhält nichts, weil niemand sie anwählt.

```yaml
listen:
  idle_timeout: 90s     # closes a connection nobody came back to
  max_conn_age: 60s     # after this, the next response asks the client to redial
```

**`max_conn_age` ist standardmäßig aus**, und das ist Absicht: Wie lange ein
Client eine Verbindung halten darf, ist Richtlinie, und sie tauscht einen
Handshake im Cluster pro Verbindung und Alter gegen Last, die sich verteilt. Die
Anfrage selbst wird immer bedient – der Header geht an die Antwort, nichts wird
wiederholt und kein Batch geht verloren.

**`idle_timeout` hat einen Standard und lässt sich nicht abschalten.** Ohne einen
zu bedienen heißt, einen File Descriptor pro verlassenem Client zu halten, bis der
Prozess neu startet. Setzen Sie einen langen, wenn Sie das wollen; dann ist es
eine Zahl, die jemand gewählt hat.

Die beiden sind unabhängig: Eine Verbindung trifft auf die Grenze, die sie zuerst
trifft, und eine Verbindung erreicht das Alter nur, indem sie genutzt wird – also
genau der langlebige Produzent, den das Alter neu verteilen soll.

## Wenn die Sink verweigert

Der Batch wird wiederholt – standardmäßig vier Versuche über etwa sieben
Sekunden, mit Jitter verdoppelt – und geht dann in die **Dead Letter**, mit dem
Grund an jedem Datensatz:

```json
{
  "event_id": "dl-9",
  "ingestion_id": "84aaee4b-66af-5cc0-b97b-6856dec95b25",
  "_dead_letter_reason": "rpc error: code = NotFound desc = Topic not found",
  "_dead_letter_sink": "pubsub:acme-prod/clicks",
  "_dead_letter_at": "2026-09-25T02:02:32Z"
}
```

Der Grund reist **am Datensatz** und nicht nur in einem Log, denn wer diese Datei
später findet, hat die Events und nicht das Log – und *Warum ist das hier?* ist
seine erste Frage.

**Ein Stream ohne `dead_letter` wird beim Laden verweigert.** Es standardmäßig auf
Schweigen zu setzen würde die Entscheidung dorthin legen, wo sie niemand trifft,
und ein verweigerter Batch, der nur eine Logzeile ist, verliert Daten still.

## Der Hook ist Go, einkompiliert

```go
hooks := gateway.NewHooks()
hooks.MustRegister("enrich_clicks", enrichClicks)
gateway.Main(hooks, gateway.WithSinks(sinks))

func enrichClicks(e map[string]any) (map[string]any, error) {
    host, _ := e["host"].(string)
    e["tenant"] = strings.Split(host, ".")[0]
    return e, nil     // returning nil DROPS the event, on purpose
}
```

Das YAML nennt einen Hook; es trägt keinen. Vor der Wahl gemessen: Eine Go-Funktion
kostet **93 ns/op** gegenüber 959 bei Starlark und 1.281 bei yaegi, ohne dass dem
Binary etwas hinzugefügt wird – und `plugin.Open` ist gar keine Option, weil es
unter `CGO_ENABLED=0`, wie jedes Artefakt hier gebaut wird, `plugin: not
implemented` zurückgibt.

Was es kostet, dort gesagt, wo es jemand liest: **Einen Hook hinzuzufügen ist ein
Rebuild und ein Deployment, keine Konfigurationsänderung.** Das ist der richtige
Tausch, solange die Hooks von den Leuten geschrieben werden, die das Binary
ausliefern, und der falsche an dem Tag, an dem ein Kunde einen ohne Release ändern
muss.

Ein Fehler in einem Hook schickt *dieses* Event auf den Dead-Letter-Pfad und lässt
nie den Batch daneben fehlschlagen. Ebenso eine Panic: Sie wird pro Event
abgefangen, weil ein fehlerhafter Datensatz keinen Prozess reißen darf, der jeden
anderen Stream bedient.

## Wenn ein Event zu groß ist

```yaml
oversize:
  larger_than: 256KiB
  archive: {type: files, path: gs://acme-oversize/clicks/}
  hook: strip_heavy_clicks
```

Das Event wird **vollständig** in `archive` geschrieben, und eine reduzierte
Version läuft in den Stream weiter und trägt einen Verweis zurück:

```json
{
  "event_id": "big-1",
  "ingestion_id": "bd8ac083-9796-50f5-bab6-77a4e2e57ad0",
  "_oversize": true,
  "_oversize_archive": "files:gs://acme-oversize/clicks/",
  "_oversize_bytes": 2149
}
```

Das ist das **Claim-Check**-Muster, und es schlägt ein schlichtes `413`, weil ein
`413` das Event verliert – und eine überdimensionierte Payload ist meist die
interessanteste, die jemand hat: Es ist die Anfrage mit dem ganzen angehängten
Dokument, der Fall, den zu debuggen sich lohnt.

`larger_than` misst ein einzelnes **Event**, nach dem Hook. `listen.max_body` ist
etwas anderes: Es begrenzt eine ganze **Anfrage** und verweigert mit `413`, bevor
ein Byte geparst wird.

## Was es zählt

Elf Prometheus-Serien, auf **eigenem Port** – nie dem der Ingestion, und diese
Regel zählt hier mehr als in der Engine: Der Ingest-Port ist von Konstruktion
öffentlich, dort senden Clients ihre `POST`s, ein `/metrics` darauf würde jeden
Stream-Namen, Pfad und jedes Ziel jedem veröffentlichen, der den Pfad fand.
Dieselbe Adresse für beides zu nennen, wird beim Laden verweigert.

```
brevis_gateway_events_received_total{stream,format}     counter
brevis_gateway_events_rejected_total{stream,reason}     counter  per event, after decode
brevis_gateway_requests_refused_total{stream,reason}    counter  per request: unauthorized|body_too_large|malformed|empty|saturated
brevis_gateway_batches_total{stream,sink,outcome}       counter  delivered|retried|buried
brevis_gateway_reconciled_total{stream,sink}            counter  landed after the send window; also delivered
brevis_gateway_flushes_total{stream,trigger}            counter  time|records|size
brevis_gateway_flush_windows_total{stream,outcome}      counter  won|yielded|ceiling|unreachable
brevis_gateway_ingested_bytes_total{stream,table}        counter  opt-in
brevis_gateway_ingested_events_total{stream,table}      counter  opt-in
brevis_gateway_table_flushes_total{stream,table,trigger} counter  opt-in
process_start_time_seconds                              gauge    unprefixed, on purpose
brevis_gateway_saturated_total{stream}                  counter  the 503s
brevis_gateway_delivery_seconds{stream,sink}            histogram
brevis_gateway_buffer_records{stream}                   gauge
brevis_gateway_queue_batches{stream}                    gauge
```

`process_start_time_seconds` hat **kein** Präfix `brevis_`, und das ist Absicht:
Collectors suchen genau diesen Namen. Er sagt, wann der Prozess gestartet ist, und
ohne ihn verankert ein Collector, der eine Startzeit-Anpassung vornimmt – der
OpenTelemetry-Prometheus-Receiver, auf dem Google Managed Prometheus aufbaut –,
jede kumulative Serie an ihrem **ersten Scrape** und verbraucht den Wert dieses
Scrapes als Basislinie. Das Ergebnis: Jeder Zähler liest zu niedrig, mit
richtigen Deltas und falschen Summen.

Die letzten drei sind die, nach denen niemand fragt, bis der erste Vorfall da war.
`saturated_total` ist die einzige Zahl, die sagt, dass einem Client gesagt wurde,
er solle zurückweichen; `buffer_records` gegenüber `buffer.max_records` ist die
einzige, die es **vorhersagt**.

`reason` ist eine geschlossene Menge und nie der Fehlertext: Ein Fehler-String
trägt einen Tabellennamen, eine Spalte, manchmal eine Zeile, und ein einziger
fehlerhafter Client würde pro Anfrage eine neue Serie erzeugen. Der Text bleibt in
der Dead Letter, am Datensatz.

## `202`, nicht `200`

Das Gateway hat die Events **akzeptiert**. Mit `durability: memory` – der einzigen
implementierten Stufe – ist das alles, was es ehrlich behaupten kann. Die Stufe,
die eine `200` verdient, ist die, die sie bereits aufgeschrieben hat, und die gibt
es nicht.

`disk` wird **namentlich** verweigert, nicht akzeptiert und als memory behandelt:
Wer `disk` schreibt, glaubt, seine Events überlebten einen Absturz, und
zuzustimmen, ohne zu liefern, ist genau der Fehler, den dieses Feld verhindern
soll.

Was bei einem Shutdown in einem Puffer liegt, wird zugestellt, nicht verworfen –
über denselben Weg, den ein voller Batch nimmt, wiederholt und begraben.

## Was es nicht tut

- **Es erscheint nicht in der Console.** Es teilt keine Datenbank mit der
  Oberfläche der Engine, es gibt also keine Seite `/ingestion` – und auch die
  Schema-Evolution, die `auto_table` vornimmt, taucht dort nicht auf. Das ist der
  nächste Schritt, und er ist nicht geschrieben.
- **Ein Stream mit einer `table` entwickelt das Schema nicht weiter.** Wenn Sie
  die Tabelle nennen, ist der Vertrag der der Tabelle: Ein Feld, das sie nicht
  hat, wird mit der Meldung verweigert, die es behebt, statt eine Tabelle zu
  ändern, die jemand geprüft hat. Von selbst eine Spalte wachsen zu lassen ist,
  was `auto_table` tut, und das ist der Unterschied zwischen beiden.
- **Ein Stream mit einer `table` legt weder Tabelle noch Index an.** Ein Dienst,
  der Tabellen anlegt, macht aus einem Tippfehler eine zweite Tabelle, die
  niemand liest – weshalb das Anlegen nur unter `auto_table` geschieht, wo
  `naming` begrenzt, welche Namen existieren dürfen, und `listen.auth` zwingend
  ist.
- **Das veröffentlichte Image hat keine Hooks**, was das ehrliche Artefakt für ein
  Design mit einkompilierten Hooks ist: Es bedient Streams, die kein `hook:`
  deklarieren.

## Eine Route, N Tabellen

`auto_table` leitet jedes Event in die Tabelle, die der **Envelope** nennt, legt
diese Tabelle an, wenn sie fehlt, und lässt eine Spalte wachsen, wenn ein neues
Feld auftaucht:

```json
{"table_name": "app_orders", "operation": "INSERT", "unique_key": "id",
 "data": {"id": "A-1", "total": 150, "customer": {"id": 7, "uf": "SP"}}}
```

Steuerfelder oben, der Datensatz innerhalb von `data`, und sieben
`brevis_*`-Tracking-Spalten in jeder Tabelle. Eine Route, N Tabellen, nichts
deklariert.

Es steht unter [Ingestion-Sinks](/docs/ingestion-sinks/#one-route-n-tables-nothing-declared).

## Wie es weitergeht

| Wenn Sie ... möchten | gehen Sie zu |
|---|---|
| die Ziele und wie jedes schreibt | [Ingestion-Sinks](/docs/ingestion-sinks/) |
| die `ingestion_id` von der anderen Seite | [Go-SDK](/docs/sdk/) |
| Dashboards und Alarme | [Observability](/docs/observability/) |
