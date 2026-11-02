---
title: Ingestion-Sinks
description: Die sechs Ziele, wie jede Datenbank einen Datensatz schreibt und warum das Image nur trägt, was Sie nutzen.
group: Ingestion
order: 19
slug: ingestion-sinks
---

Sechs Ziele, und die Liste ist bewusst explizit – ein Connector, der *geplant*
ist, und ein Connector, der *läuft*, sind für jemanden, der hier wählt,
verschiedene Dinge.

| Sink | Stand | Was ein Schreibvorgang ist | Erprobt gegen |
|---|---|---|---|
| `pubsub` | **läuft** | ein Publish pro Batch, Attribute und Ordering-Key aus den eigenen Feldern des Events | den Emulator |
| `postgres` | **läuft** | `COPY FROM STDIN` oder ein gestagtes `INSERT` in einer Transaktion | eine echte Postgres |
| `mysql` | **läuft** | mehrzeiliges `INSERT` in einer Transaktion oder `INSERT IGNORE` | eine echte MySQL |
| `files` | **läuft** | NDJSON-Objekte in einem Verzeichnis, `gs://` oder `s3://` | das Dateisystem und S3 |
| `bigquery` | **läuft** | Zeilen geladen oder gestagt und per `MERGE` auf `ingestion_id` zusammengeführt | **nur Konfiguration – es gibt keinen Emulator** |
| `redshift` | **läuft** | der Batch nach S3, dann `COPY`, dann `MERGE` | **nur Konfiguration – es gibt keinen Emulator** |

**Zwei der sechs sind Ende-zu-Ende unerprobt**, und die Tabelle sagt das, statt
das Wort „läuft“ eine Behauptung tragen zu lassen, die niemand geprüft hat.
BigQuery und Redshift haben keinen lokalen Emulator, getestet werden also die
Konfiguration, die Verweigerungen und der darunterliegende Treiber – den die
eigene Suite des SDK bereits abdeckt. Die erste Produktionsnutzung ist jeweils der
echte Beweis und sollte ein Stream mit geringem Risiko sein.

Ein unbekanntes `type:` wird **beim Start verweigert**, wobei genannt wird, was
*dieses Binary* trägt. Ein Gateway, das auf einer Sink startet, die es nicht hat,
verwirft Events aus einem Grund, den niemand sehen kann.

`gs://` und `s3://` sind keine eigenen Connectors: `files` liest alle drei
Pfadformen, sodass die Dead Letter ein Ordner auf einem Laptop und ein Bucket in
Produktion ist, ohne dass sich hier etwas ändert.

## Wie jede schreibt

`write` ist bei den vier Zielen, die in eine Tabelle schreiben, **erforderlich und
wird nie vorbelegt**, weil die beiden Modi wirklich verschieden sind und nur der
Besitzer der Tabelle weiß, welcher es ist:

| | `append` | `merge` |
|---|---|---|
| **postgres** | `COPY FROM STDIN` | `INSERT … ON CONFLICT (ingestion_id) DO NOTHING` |
| **mysql** | mehrzeiliges `INSERT` pro Block | `INSERT IGNORE` |
| **bigquery** | Zeilen geladen (über GCS oberhalb des Inline-Limits) | `MERGE … WHEN NOT MATCHED THEN INSERT` |
| **redshift** | `COPY` aus S3 | `COPY` in eine Staging-Tabelle, dann `MERGE` |

`merge` bedeutet **in allen vieren dasselbe**: das idempotente Einfügen, und die
**erste Zustellung gewinnt**. Eine erneute Zustellung wird ignoriert, nicht
angewendet – was für ein Event richtig ist, das einmal geschah und sich nicht
ändert, und für eine Zeile mit veränderlichem Zustand falsch.

Postgres und MySQL **verlangen für `merge` einen `UNIQUE`-Index auf
`ingestion_id`** und verweigern ohne ihn, statt still anzuhängen:

```sql
CREATE UNIQUE INDEX CONCURRENTLY ON landing.orders (ingestion_id);
```

Ohne den Index hat `ON CONFLICT` nichts, worauf es abgleichen kann, und jede
erneute Zustellung würde in eine Tabelle duplizieren, deren Besitzer das Gegenteil
verlangt hat. Die Verweigerung ist kein verlorener Batch: Er wird wiederholt und
dann in der Dead Letter begraben, mit dem Grund und dem `CREATE INDEX` am
Datensatz – niemand verliert Events, während jemand den Index anlegt.

**`upsert` wird namentlich verweigert.** `ON CONFLICT DO UPDATE`, bei dem die
*letzte* Zustellung gewinnt, ist ein echter Modus, der **nicht implementiert**
ist. Das Wort zu akzeptieren und sich wie `merge` zu verhalten ist genau der
Fehler, den dieses Feld verhindern soll, also sagt die Konfiguration es stattdessen.

## Jede mit dem, was nur für sie gilt

```yaml
# Postgres: schema-qualified name, and the table has to exist.
sink: {type: postgres, dsn_from: BREVIS_DSN, table: landing.clicks, write: merge}

# MySQL: no COPY, so throughput is an order below Postgres on the same
# hardware. Flush wider here.
sink: {type: mysql, dsn_from: BREVIS_DSN, table: landing.clicks, write: merge}

# BigQuery: no dsn_from. It authenticates with the pod's own credentials, which
# is what a workload identity is for. The name has no dots — the project and
# the dataset are their own fields.
sink: {type: bigquery, project: acme-prod, dataset: landing, table: clicks, write: merge}

# Redshift: two hops, not one. It is columnar, a row-by-row INSERT pays the cost
# of a block, and the only workable load is COPY from S3. So every batch becomes
# an object in the staging prefix and then a COPY — a stream flushing every
# second writes 86,400 objects a day. Flush much wider here.
sink:
  type: redshift
  dsn_from: BREVIS_RS_DSN
  table: landing.clicks
  staging: s3://acme-staging/gateway/
  iam_role: arn:aws:iam::123456789012:role/redshift-copy
  write: merge
```

`dsn_from` nennt die **Umgebungsvariable**, die den Connection-String hält, nie
den String: Ein DSN trägt ein Passwort, und diese Datei liegt in git. Es ist
dieselbe Trennung, die `secrets:` in einem Workflow vornimmt.

`iam_role` ist eine Rolle, und der Treiber **akzeptiert keinen Schlüssel**: Ein
Schlüssel in der URL eines `COPY` landet im Query-Log des Clusters, das
erstaunlich viele Leute lesen.

## Keine Spaltendeklaration

Das Gateway sendet keine Spaltenliste. Eine Pipeline deklariert ihre eigene, weil
ihre Zeilen eine Form haben, die sie kontrolliert; der Batch eines Gateways ist,
was *N* Clients in einem Flush-Fenster gepostet haben, und die Formen dürfen
abweichen.

Der Treiber ermittelt die Liste aus der **Tabelle selbst**, geschnitten mit dem,
was der Batch trägt, **bevor** er den Server berührt. Ein Feld, das die Tabelle
nicht hat, wird mit der Meldung verweigert, die es behebt, statt mitten im `COPY`
mit `column "x" of relation "y" does not exist` zu scheitern; ein Feld, das ein
Event weglässt, wird als `NULL` geschrieben.

## Die Importliste ist die Auswahl

Die Sinks sind **einkompiliert**, wie die Hooks, und das Binary registriert, was
es hat:

```go
func main() {
    sinks := gateway.NewSinks()
    sinks.MustRegister(postgres.Sink, postgres.New)
    sinks.MustRegister(files.Sink, files.New)
    gateway.Main(nil, gateway.WithSinks(sinks))
}
```

Der Go-Linker beschneidet, was nichts referenziert, sodass ein Binary, das den
BigQuery-Treiber nie importiert, kein BigQuery trägt – auch kein Arrow und keine
Storage Write API. So hat `database/sql` schon immer funktioniert.

Es ist so viel wert, wie es klingt:

| Build | Pakete | Binary |
|---|---|---|
| alle sechs Sinks, beide Objektspeicher, beide Metastores | etwa 900 | 55,0 MB |
| `postgres` + lokales `files` | etwa 240 | **10,2 MB** |

Objektspeicher sind eine **zweite** Registry, weil ein Schema kein Ziel ist:
`files` ist eine Sink, die in ein Verzeichnis, nach `gs://` und nach `s3://`
schreibt, und ein Build, der nur lokal schreibt, sollte dafür nicht das AWS-SDK
tragen.

### Zwei veröffentlichte Images

| Image | trägt | Pull (amd64) |
|---|---|---|
| `brevissh/brevis-gateway:0.32.0` | sechs Sinks, S3, GCS, Redis, memcached | 19,1 MB |
| `brevissh/brevis-gateway:0.32.0-slim` | `postgres`, lokales `files`, `auto_table` | **5,2 MB** |

Beide aus einem Build eines Baums, die beiden Tags sind also immer derselbe
Commit. Es gibt kein `latest-slim` – `latest` ist bereits ein Tag, das niemand
deployen sollte.

**Sie wollen ein anderes Paar?** `cmd/gateway-slim` hat fünfzehn Zeilen, und der
Import-Block ist seine gesamte Konfiguration. Kopieren, die Imports ändern, bauen.
Wer einen Hook will, kompiliert ohnehin sein eigenes Binary, die Wahl der Sinks
kostet ihn also nichts zusätzlich.

### Die Verweigerung sagt, welchen Build Sie in der Hand halten

```
sink type "bigquery" is not one this binary carries (it has: files, postgres).
Sinks are compiled in, so this is a build that left it out rather than a
destination that does not exist.
```

Eine feste Liste würde diese Person nach einem Konfigurationsfehler suchen lassen,
den sie nicht gemacht hat. Dasselbe gilt für einen Objektspeicher: Eine
`s3://`-Dead-Letter in einem Binary ohne S3-Backend wird **beim Start**
verweigert, wobei das Schema genannt wird, statt beim ersten Batch, den sie
begraben müsste.

## Eine Route, N Tabellen, nichts deklariert

```yaml
sink:
  type: auto_table
  shape: columns
  naming: {pattern: '^(app|svc)_[a-z0-9_]{2,40}$', allow: [app_, svc_], max_new_per_hour: 20}
  metastore: {type: redis, addr_from: BREVIS_METASTORE_ADDR, ttl: 60s}
  into: {type: bigquery, project: acme-prod, dataset: landing, write: merge}
```

`auto_table` **leitet und schreibt nicht**: `into` ist, was schreibt, und es
trägt keine `table` – die Tabelle kommt aus jedem Event. Ein `into` mit einer
`table` wird beim Start verweigert, weil eine feste Tabelle unter einem Router
still gewänne und jedes Event darin landen würde.

### Der Envelope

Der Body ist ein **Envelope**: Steuerfelder oben, der Datensatz innerhalb von
`data`.

```json
{
  "table_name": "app_orders",
  "description": "App orders",
  "operation": "INSERT",
  "unique_key": "id",
  "data": {
    "id": "A-1",
    "total": 150,
    "customer": {"id": 7, "uf": "SP"},
    "items": [{"sku": "X", "qty": 2}]
  }
}
```

| Feld | | Was es ist |
|---|---|---|
| `table_name` | **erforderlich** | wohin dies landet |
| `data` | **erforderlich** | der Datensatz, und nur er |
| `unique_key` | Standard `id` | welches Feld von `data` den Datensatz identifiziert – **bei `merge` erforderlich** |
| `operation` | Standard `INSERT` | `INSERT`, `UPDATE` oder `DELETE` |
| `description` | optional | **akzeptiert und noch nicht genutzt** – siehe unten |

`table_name` neben `total` und `customer` war ein Feld des **Transports**, das sich
als Feld des **Datensatzes** ausgab. Sie zu trennen ist, wie jedes CDC-Format
geformt ist – und es macht möglich, ein Präfix zu reservieren, weil es nun genau
eine Stelle gibt, die nur der Produzent schreibt.

### Der Schlüssel gehört zu `merge`, nicht zum Envelope

`unique_key` ist optional, und ob er **erforderlich** ist, hängt vom Schreibmodus
ab – weil die beiden Modi Verschiedenes wollen:

| | |
|---|---|
| `write: merge` | **erforderlich.** Der Modus existiert, um eine Zeile pro Datensatz zu halten, und `brevis_record_key` ist, was „pro Datensatz“ bedeutet: Das `qualify`, das die aktuelle Version auflöst, partitioniert danach. Eine Merge-Tabelle voller Zeilen, die keinen Datensatz benennen, ist eine Tabelle, die niemand auflösen kann. |
| `write: append` | **optional.** Eine Append-Tabelle ist ein Log, und ein Log-Eintrag muss nicht *von* einem Datensatz handeln: eine Audit-Zeile, ein Webhook, ein Metrik-Sample. Dort eine `id` zu verlangen, ließe Produzenten eine erfinden, was schlimmer ist als `NULL`, weil es echt aussieht. |

Unter `append` wird die Spalte nullable angelegt, und eine Zeile ohne Schlüssel
trägt `NULL` – nicht `""`, was eine andere Tatsache ist. Und `brevis_ingestion_id`
existiert weiterhin: Ohne Schlüssel **ist der Inhalt der Schlüssel**, dasselbe
Dokument zweimal ist also dieselbe Id.

**Ein Feld zu nennen, das nicht in `data` steht, ist in beiden Modi ein Fehler.**
„Sie haben nichts gesagt“ und „Sie haben etwas gesagt, das nicht da ist“ sind
verschiedene Fehler, und nur der erste geht durch:

```json
{"accepted":0,"rejected":["event 0: \"unique_key\" names \"pedido_id\" as this
 record's identity and \"data\".\"pedido_id\" is missing or empty. Drop
 \"unique_key\" to fall back to \"id\", or send the field"]}
```

**`description` wird gelesen und führt heute nirgendwohin.** Es steht im Vertrag
für die Ingestion-Seite der Console, die nicht geschrieben ist. Es wird hier
gesagt, weil ein still akzeptiertes Feld ein Feld ist, von dem jemand glaubt, es
werde gespeichert.

### Acht feste Spalten, plus was der Datensatz trägt

| Spalte | | |
|---|---|---|
| `brevis_ingestion_id` | `STRING` | die Identität und der Merge-Schlüssel |
| `brevis_record_key` | `STRING` | `data[unique_key]` – von welchem **Datensatz** es handelt; `NULL` unter `append` |
| `brevis_operation` | `STRING` | `INSERT`, `UPDATE` oder `DELETE` |
| `brevis_received_at` | `TIMESTAMP` | Ankunft, auf **unserer** Uhr – die Partitionsspalte |
| `brevis_loaded_at` | `TIMESTAMP` | der Schreibvorgang, vom **Ziel** gestempelt |
| `brevis_stream` | `STRING` | welche Route ihn geschrieben hat |
| `brevis_gateway` | `STRING` | welches Deployment |
| `brevis_received_bytes` | `INT64` | wie groß das Event **ankam**, Envelope eingeschlossen |

Diese acht gehören **dem SDK**, nicht dieser Sink: `sdk.LandingSchema` deklariert
sie und `sdk.LandingID` prägt die Identität, sodass eine Pipeline dieselbe Tabelle
mit denselben Ids landen kann. Eine Definition, weil zwei auseinanderdriften
würden. Siehe [das Landing-Layout des SDK](/docs/sdk/#the-landing-layout).

**`brevis_` ist reserviert.** Ein `data`, das einen Schlüssel mit diesem Präfix
trägt, wird **pro Event** verweigert – sonst fälscht ein Produzent ein Steuerfeld,
und ein gefälschtes `brevis_received_at` ist schlimmer als keines, weil es echt
aussieht.

**`brevis_loaded_at` ist ein `DEFAULT` der Datenbank**, kein Wert, den das Gateway
sendet. Das Gateway kennt den Zeitpunkt der **Auslieferung**, das Ziel kennt den
Zeitpunkt des **Schreibens**, und die Lücke zwischen beiden Spalten ist die echte
Ende-zu-Ende-Latenz, pro Zeile, ohne dass etwas instrumentiert wäre.

**`brevis_received_at` ist die Partitionsspalte, nie die Uhr eines Clients**: Die
eines Produzenten kann falsch sein oder fehlen, und eine Partitionsspalte, die der
Client kontrolliert, ist ein Client, der ins Jahr 2035 schreiben kann. Eine
unpartitionierte Landing-Tabelle ist eine Abfragerechnung, die nur wächst. Das
Clustering erfolgt nach `brevis_record_key`, weil die Historie eines Datensatzes
zu lesen ist, was jeder mit einer solchen Tabelle tut.

Eine Deklaration bedient jedes Ziel, weil der DDL-Generator des SDK jeden Typ
übersetzt: `STRING` wird zu `TEXT` bei Postgres, `LONGTEXT` bei MySQL und `STRING`
bei BigQuery; `JSON` wird zu `JSONB`, `JSON` und `JSON`.

### Volumen ohne Infrastruktur, um Volumen zu messen

`brevis_received_bytes` ist **kostenlos**: Das Gateway zählt diese Bytes beim
Dekodieren ohnehin, um `buffer.flush.size` zu steuern, und bisher starb die Zahl
dort. Den Datensatz allein zu messen würde ein `json.Marshal` pro Event kosten –
2,0 µs gegenüber den 3,1 µs, die das Parsen ohnehin braucht, **67 % mehr auf dem
Hot Path** –, um eine Zahl zu verfeinern, deren Aufgabe Trend und Zuordnung ist.

Es beantwortet, was die Metriken **strukturell nicht können**. Jede
`brevis_gateway_*`-Serie gilt pro *Stream* und trägt bewusst kein Label `table`:
Mit `auto_table` wird eine Route zu N Tabellen, und dieses Label wählt der
Produzent. „Welche Tabelle wächst, und seit wann“ hatte also nirgends eine Antwort.

```sql
select date(brevis_received_at) day,
       count(*) events,
       sum(brevis_received_bytes)/1e9 gb
from app_orders
group by 1 order by 1 desc
```

Ohne die Spalte bedeutet dieselbe Frage, die ganze JSON-Spalte zu scannen –
`sum(length(to_json_string(data)))` –, etwa **das Fünfzigfache der gescannten
Bytes**, jedes Mal, wenn jemand fragt. Die Spalte zahlt sich bei der ersten
Abfrage aus.

**Es ist Ingress, nicht Speicher.** Das Ziel hält die Zeile typisiert und
komprimiert: 400 Byte JSON können auf der Platte 80 sein. Die Summe liefert, was
*ankam*, nie, was für das Aufbewahren berechnet wird.

Und **der Produzent kann sie nicht schreiben.** Das Gateway stempelt den Wert nach
dem Hook und überschreibt, was der Envelope trug – ein Client, der das Feld
sendete, würde sein eigenes Volumen melden, und die Summe würde nichts mehr
bedeuten.

### Die Volumenserien sind Opt-in

```bash
BREVIS_INGESTION_METRICS=true
```

```
brevis_gateway_ingested_bytes_total{stream,table}
brevis_gateway_ingested_events_total{stream,table}
```

**Standardmäßig aus**, und der Standard ist der Punkt: `table` ist ein Label, das
der *Produzent* wählt. Alles andere in dieser Datei wird nach dem gelabelt, was ein
Operator ins YAML geschrieben hat, die Anzahl der Serien ist also bekannt, bevor
der Prozess startet. Niemand sollte eine Metrik-Rechnung finden, weil er ein
Upgrade gemacht hat.

Ein Wert, den niemand parsen kann, liest sich als **aus**, nie als Fehler: Dies ist
Observability, und ein Tippfehler hier darf nicht der Grund sein, dass ein
Ingestion-Endpoint nicht startet.

Die Spalte ist in beiden Fällen da. Die Metrik kauft das *Jetzt*; die Spalte kauft
das *Seit wann* und *Von wem*.

### Zwei Formen, ein Vertrag

`shape` entscheidet, was der **Datensatz** zur Zeile beiträgt. Der Vertrag des
Produzenten ist in beiden Fällen derselbe – der Envelope ändert sich nie. Was sich
ändert, ist, wie die Tabelle aussieht, und das ist die Entscheidung des Operators,
nicht die des Produzenten.

```yaml
shape: document   # the default
shape: columns
```

Bei **`document`** geht `data` als Ganzes in eine `JSON`-Spalte:

```
 brevis_ingestion_id | brevis_record_key | … | data
 d7179bfe-…          | A-1               |   | {"id": "A-1", "total": 150, "customer": {…}}
```

Nach dem Anlegen gibt es nie DDL. Ein neues Feld ist ein neuer Schlüssel: keine
Quote für Schemaänderungen, kein Wiederöffnen des Write-Streams, kein Wettlauf
zwischen Replicas – und es ist an dem Tag abfragbar, an dem es ankommt. Dort ist
der Markt gelandet: Fivetran, Airbyte und Snowpipe landen alle in einer Raw-Schicht,
die nicht fehlschlagen kann, und modellieren nachgelagert.

Bei **`columns`** erhält jedes Feld des Datensatzes eine eigene Spalte:

```
 id   | total | customer              | items
 A-1  | 150   | {"id": 7, "uf": "SP"} | [{"qty": 2, "sku": "X"}]
```

**Skalare werden zu `STRING`, Objekte und Arrays werden zu `JSON`, und nirgends
wird abgeleitet.** `150` kam als Zahl an und wurde der String `150`. Das ist eine
Wahl und keine Einschränkung: Ein Feld, das heute ganz und morgen gebrochen
ankommt, würde den Typ einer Spalte ändern, ohne dass jemand etwas schreibt, und
die Zeile, die nicht mehr passt, geht in die Dead Letter. Einen Typ zu vergeben
ist der Weg der **Beförderung** – im YAML geschrieben und in einem Diff geprüft.

**Ein `null` wird zu gar keiner Spalte.** Es hat keine Form, es gibt also nichts zu
lesen: Das Feld erscheint in der Tabelle ab dem ersten Event, das einen WERT trägt,
mit der Form dieses Werts. Das Event, das `null` sendete, landet genauso als
`NULL`, was es bedeutete – und wo die Spalte bereits existiert, weil Sie sie
deklariert haben oder ein anderes Event sie angelegt hat, wird das `null` in sie
geschrieben, statt weggelassen zu werden.

**Und eine `JSON`-Spalte erhält einen JSON-WERT, nie einen String davon.** Das
klingt nach Detail und ist der Unterschied zwischen einer abfragbaren Tabelle und
einer Tabelle, die nur richtig aussieht:

```sql
SELECT JSON_TYPE(customer) FROM bronze.orders;     -- object
SELECT JSON_VALUE(customer, '$.uf') ...;           -- SP
```

Antwortet `JSON_TYPE` mit `string` und `JSON_VALUE` mit NULL, wurde das Objekt
doppelt kodiert – die Spalte hält den TEXT des Objekts statt des Objekts.
Gateway-Versionen bis einschließlich **0.15.0** taten das bei BigQuery, in
**beiden** Formen; siehe das [Gateway-Changelog](https://github.com/AreteAcademy/brevis/blob/master/gateway/CHANGELOG.md)
für die Ein-Zeilen-Prüfung und die Reparatur. Postgres und MySQL waren nie
betroffen: Ihr Server parst den Text.

Ein Feldname muss die Regel von BigQuery erfüllen, die engste der vier. Postgres
würde fast alles akzeptieren, wenn es gequotet ist, und das ist die Falle: Die
Tabelle wird dort angelegt und bricht an dem Tag, an dem jemand einen Stream auf
BigQuery richtet.

Beide Formen haben ein SDK-Äquivalent, und es ist derselbe Code und kein
passender: `sdk.Landing(table, sdk.LandingKey("id"))` landet `document`, und mit
[`sdk.LandingColumns()`](/docs/sdk/#two-shapes-the-record-whole-or-one-column-per-field)
landet es `columns`. Dieselben Spalten, dieselben Werte, dieselbe
`brevis_ingestion_id` – ein Stream kann also ohne Migration zwischen einem Gateway
und einer Pipeline wechseln, und die beiden lassen sich als eine Tabelle lesen.

Die Pipeline kann dieselbe Spalte auf dieselbe Weise wachsen lassen: Setzen Sie
[`Evolve: sdk.EvolveAdditiveFromPayload`](/docs/sdk/#when-a-field-the-table-does-not-have-shows-up)
am Ziel, und ein Feld, das der Batch trägt, wird nach dieser Regel, durch diesen
Code, zu einer Spalte.


**Das Präfix ist konfigurierbar, und es ist eine Entscheidung für den ganzen
Prozess.** `BREVIS_LANDING_PREFIX=acme` macht daraus `acme_ingestion_id` und die
übrigen – für jeden Stream in diesem Gateway, sodass zwei Sinks sich nicht über
eine Tabelle widersprechen können, die beide schreiben könnten. Die oben genannte
Regel zu reservierten Namen wandert mit: Unter `acme_` wird ein Produzent, der
`acme_region` sendet, verweigert, und `brevis_region` ist ein gewöhnliches Feld.

**Wählen Sie es, bevor die erste Tabelle angelegt wird.** Es später zu ändern
fügt acht Spalten hinzu und verlässt acht – nichts löscht eine Spalte –, sodass
eine Tabelle, die das Layout bereits unter einem Präfix trägt, einen Load
verweigert, der ein anderes deklariert. Siehe [die SDK-Seite](/docs/sdk/#the-brevis-prefix-is-yours-to-change)
für die Normalisierung und den Ausweg.

**Die Felder eines verschachtelten Objekts können eigene Spalten bekommen.**
`BREVIS_NORMALIZE_DATA=true` am Prozess des Gateways flacht eine Ebene ab –
`{"name": {"first": "x"}}` landet als `name_first` –, und jeder Stream teilt es,
sodass zwei Sinks sich nicht über eine Tabelle widersprechen können, die beide
schreiben könnten. Siehe [die SDK-Seite](/docs/sdk/#one-column-per-nested-field)
dafür, was es umbenennt – die Hälfte, die man vor dem Einschalten lesen sollte.

**Bei BigQuery sind `nationalID` und `nationalId` eine Spalte.** BigQuery faltet
die Groß- und Kleinschreibung von Spalten, sodass ein Flush, der eine Schreibweise
in einem `DELETE` und die andere in einem `INSERT` trägt, **eine** Spalte anlegt –
die erste in sortierter Reihenfolge –, statt das `CREATE` fehlschlagen zu lassen.
Ein einzelnes Event, das beide trägt, wird verweigert, wobei beide Felder genannt
werden. Postgres und MySQL behalten sie als zwei Spalten, was für sie korrekt ist.
Siehe [die SDK-Seite](/docs/sdk/) dafür, welche Schreibweise gewinnt und was nicht
migriert wird.

### `UPDATE` und `DELETE` werden festgehalten, nicht angewendet

`brevis_operation` ist eine Spalte. Eine Landing-Tabelle ist **Historie**: Ein an
Ort und Stelle angewendetes `UPDATE` verliert die vorherige Version, und der Tag,
an dem Sie diese Version wollen, ist der Tag, an dem etwas kaputtging. „Die
aktuelle Version jedes Datensatzes“ aufzulösen ist Aufgabe des nachgelagerten
Modells:

```sql
qualify row_number() over (partition by brevis_record_key
                           order by brevis_received_at desc) = 1
   and brevis_operation <> 'DELETE'
```

Das ist, was Debezium, Fivetran und Airbyte tun – und es heißt, dass CDC im
Schreibpfad **nichts** kostet: kein Upsert-Modus, keine Sperre, kein Löschen.

### Die Identität enthält keine Uhr

```
brevis_ingestion_id = uuid5(auto_table | table | data[unique_key] | sha256(canonical(data)))
```

In der Formel steht kein `occurred_at`, und das ist Absicht: Die Ankunft wurde
**unsere** Verantwortung, und unsere Uhr ist `time.Now()` – bei jeder Zustellung
anders. Eine Identität mit ihr darin würde jede erneute Zustellung zu einem neuen
Event machen, und `merge` würde nichts mehr absorbieren.

```
same record, same content, twice  →  same id, merge absorbs
same record, content changed      →  a different id, both versions land
```

Der Vorbehalt ist der dokumentierte: Zwei wirklich verschiedene Events mit
byte-identischem `data` fallen zu einem zusammen. Für CDC ist das **korrekt** –
zwei identische Updates desselben Datensatzes mit denselben Werten sind dieselbe
Tatsache.

Der Fingerabdruck wird über das Dokument in **kanonischer** Form gebildet –
Schlüssel auf jeder Ebene sortiert, Arrays in Reihenfolge belassen, alles
gequotet. Go randomisiert die Map-Iteration, sodass ein naiver Hash zwischen zwei
Zustellungen desselben Events abweichen würde, das eine, was er verhindern soll.

### Die Tabelle lässt von selbst eine Spalte wachsen

Ein `data`, das ein Feld trägt, das die Tabelle nicht hat, lässt die Spalte
entstehen. **Additiv, und nur additiv**: Nichts wird gelöscht, nichts verengt.

Bei BigQuery wurde das erst in **0.13.0** wahr. Davor deklarierte `auto_table`
`Evolve: additive` auf jedem Ziel, und nur die Treiber von Postgres und MySQL
lasen die Deklaration – dort war die Form also bei der Anlage eingefroren, und
nichts sagte es.

> **Wenn Sie von 0.11.0 oder 0.12.0 kommen**, brauchen Tabellen, die vor `0.11.0`
> angelegt wurden, eine einmalige Migration, weil die achte feste Spalte in die
> Deklaration ging und nicht in die Tabelle:
>
> ```sql
> ALTER TABLE `<dataset>.<table>` ADD COLUMN IF NOT EXISTS brevis_received_bytes INT64
> ```
>
> Postgres und MySQL waren nie betroffen.

Ein Batch, der das Rennen um das `ALTER` verliert, schlägt fehl, und der
gewöhnliche Retry der Pipe löst es auf – ein Batch, der auf DDL wartet, und ein
Batch, der auf einen Worker wartet, sind dasselbe, es gibt also keinen zweiten
Puffer. Über Replicas hinweg hält der Metastore einen *Debounce* von einer Sekunde
pro Tabelle und pro Form, damit zehn Replicas, die dasselbe neue Feld sehen, nicht
zu zehn `ALTER`s gegen die Quote von BigQuery werden – fünf Metadatenoperationen
pro Tabelle je zehn Sekunden.

**Ein Debounce und keine Sperre**: Nichts wird freigegeben und kein Lease erneuert.
Wer verliert, wird *wiederholt*, nicht blockiert, und eine Replica, die stirbt,
während sie einen hält, kostet die anderen eine Sekunde. So macht es der Markt –
Delta Lake und Iceberg committen optimistisch und wiederholen, Kafka Connect
bekommt Serialisierung aus der Partitionsreihenfolge, Fivetran hält einen Writer
pro Tabelle.

### Der Name ist die Angriffsfläche

`auto_table` macht aus einem String in einer Payload DDL, daher ist jedes Feld
unter `naming` eine Verweigerung:

- **`pattern`** hat den Standard `^[a-z][a-z0-9_]{2,48}$` – die Schnittmenge
  dessen, was Postgres, MySQL, BigQuery und Redshift ungequotet akzeptieren. Ein
  Name, der durchgeht, braucht nirgends Quoting und kann keine Injection tragen.
- **`allow`** verengt weiter, nach Präfix.
- **`max_new_per_hour`** (Standard 20) begrenzt **Anlagen** in einer gleitenden
  Stunde, nie Schreibvorgänge: Eine Tabelle, die bereits existiert, wird nie
  gedrosselt. Unter `metastore: memory` lebt das Budget im Prozess, und vier
  Replicas lassen das Vierfache zu; unter `redis` oder `memcached` teilen sie sich
  einen Zähler.

Ein Name außerhalb der Regeln wird **pro Event** verweigert, in der Antwort, und
jedes wohlgeformte Event derselben Anfrage landet weiterhin:

```json
{"accepted": 0, "rejected": ["event 0: the table name \"orders\" does not match ^(app|svc)_[a-z0-9_]{2,40}$"]}
```

Pro Event und nicht pro Batch, mit Absicht. Ihn erst beim Schreiben zu verweigern
ließe den ganzen Batch scheitern – ein fehlerhaftes Event eines Produzenten würde
die Events jedes anderen Produzenten im selben Flush-Fenster begraben, ohne dass
einer davon es erfährt.

**`auto_table` wird bei einem Endpoint ohne `listen.auth` verweigert**, auch unter
`BREVIS_ENV=local`. Ein Produzent, der eine Tabelle benennen kann, kann eine
anlegen, und braucht daher Authentifizierung, auch wo ein gewöhnlicher Stream sie
nicht bräuchte. Eine NetworkPolicy deckt das nicht ab: Sie begrenzt, wer den Port
erreicht, nicht, nach welchem Tabellennamen er fragt.

### Der Metastore ist ein Cache

Er existiert, damit ein Schreibvorgang pro Event nicht eine Abfrage pro Event ist,
und er hält drei Dinge: welche Tabellen existieren, den DDL-*Debounce* und den
Zähler hinter `max_new_per_hour`.

Er ist ein Cache und **nicht** die Quelle der Wahrheit. Das Ziel entscheidet, ob
eine Tabelle existiert, und dass *N* Replicas um das Anlegen einer Tabelle
wetteifern, ist der Normalfall: `AlreadyExists` ist Erfolg, und dies verringert nur
das Rennen. Die TTL ist, wie lange er **falsch** liegen darf.

| Backend | Wann |
|---|---|
| `memory` | der Standard, braucht nichts. Mit **einer** Replica ist er die richtige Antwort |
| `redis` | `addr_from` erforderlich. Ein `SETNX` ist der Claim, ein `INCR` der Zähler |
| `memcached` | `addr_from` erforderlich. Ein `Add` ist der Claim, und das Ablaufen ist **sekunden**genau |

**Redis ist der schnellere für einen Stream mit Routing-Key**, und der Unterschied
ist eine Zahl wert und kein Adjektiv. Mit dem Trigger pro Tabelle fragt ein Fenster
einen Claim pro Tabelle an: Redis beantwortet 94 davon in einem gepipelineten
Round Trip, und jedes andere Backend beantwortet sie parallel, begrenzt auf 32.
Gemessen gegen ein Backend, das 25 ms entfernt ist – 94 Claims:

| Backend | einer nach dem anderen | wie ausgeliefert |
|---|---:|---:|
| `redis` | 2,589 s | **29,8 ms** |
| `memcached` | 2,577 s | **89,4 ms** |

Drei Wellen gegen einen Round Trip. Beide liegen innerhalb der 500 ms, die ein
Claim bekommt, memcached ist also die langsamere Wahl und keine verweigerte.

Bei mehreren Replicas gibt `memory` jeder ihren eigenen: Der Debounce debounct
nichts, und `max_new_per_hour` begrenzt einen Prozess. Das ist der einzige Grund,
warum die anderen beiden existieren.

**Ein Metastore, der ausfällt, darf keinen Schreibvorgang mitreißen.** Ein `Get`,
das einen Fehler liefert, ist ein Miss, ein Claim, der einen Fehler liefert,
verhält sich als gewonnen, ein Zähler, der einen Fehler liefert, lockert das
Limit. Ein Cache, der die Ingestion stoppen kann, ist schlimmer als kein Cache.

**Und „ausgefallen“ schließt „langsam“ ein.** Der Claim läuft, während die Sperre
des Puffers gehalten wird, sodass ein Backend, das zwei Sekunden zum Antworten
braucht, zwei Sekunden bedeutet, in denen dieser Stream nichts annimmt. Jeder
Aufruf ist auf **500 ms** begrenzt und schlägt darüber hinaus fail-open fehl. Redis
befolgt das als Kontext des Aufrufers; `gomemcache` nimmt gar keinen Kontext, also
befolgt memcached es als Socket-Deadline – dieselbe Grenze auf zwei Wegen erreicht,
weil einer der beiden Clients sich nicht anhalten lässt.

**`ttl: 0` heißt nie**, und nichts zu sagen nimmt weiterhin den Standard von einer
Minute – drei Zustände, nicht zwei.

Die Uhr ist **nicht**, was sich von einem falschen Eintrag erholt. Ein
Schreibvorgang, den das Ziel verweigert, ruft `invalidate` auf, das den Eintrag
sofort verwirft und den eigenen Retry der Pipe gegen einen kalten Cache
zurückkommen lässt: Eine von Hand gelöschte Tabelle kostet **einen
fehlgeschlagenen Versuch**, keine Minute.

Das Ablaufen hat einen Preis. `sinkFor` belastet `naming.max_new_per_hour`, wenn es
eine Tabelle nicht **kennt** – ein Miss belastet also eine Tabelle, die seit Wochen
existiert, und ein Pod, der eine Minute nach dem letzten Event einer Tabelle neu
startet, erhält nichts von dem geteilten Backend, für das er zahlt. Bei
langlebigen Tabellen ist `ttl: 0` die Antwort.

> Bei `0.12.0` und `0.13.1` **parst** diese Schreibweise **nicht** – YAML liest
> eine nackte Null als int, und `0.13.2` ist die erste, die beides annimmt. Bei
> diesen Versionen schreiben Sie `ttl: 0s`.

`addr_from` nennt die **Umgebungsvariable**, die die Adresse hält, nie die Adresse:
Sie trägt oft genug ein Passwort.

### BigQuery hat eine Flush-Untergrenze

**BigQuery erlaubt 1.500 Load-Jobs pro Tabelle und Tag.** Das Standardfenster von
einer Sekunde sind 86.400 – das 57-Fache der Quote, in etwa fünfundzwanzig Minuten
aufgebraucht. Für Pub/Sub ist das kein Problem, und die Zahl würde hierher *aus*
einer Pub/Sub-Konfiguration kommen.

Ein Stream, der nach BigQuery schreibt, direkt oder über einen Router, wird daher
**beim Laden verweigert**, wenn sein Fenster unter 60s liegt. Die echte Antwort ist
die Storage Write API, die nicht geschrieben ist; bis dahin verweigert die
Konfiguration ein Fenster, das sie nicht einhalten kann, statt das erste
Deployment es herausfinden zu lassen.

`auto_table` kann nicht nach **Redshift** routen: Dieser Treiber legt keine
Tabellen an, sodass jeder neue Name beim Laden scheitern würde. Namentlich
verweigert, beim Start.

## Was es noch nicht gibt

`sqlite`, `dynamodb`, `kinesis`, `sqs`, `sns`, `kafka` und `rabbitmq` sind geplant,
und nichts davon ist geschrieben. (`redis` und `memcached` gibt es als
**Metastore**, was etwas anderes ist: ein Cache dessen, was über die Tabellen
bekannt ist, nie ein Ziel.) Die Tabelle am Anfang dieser Seite nennt den Stand
jedes einzelnen und wird aktualisiert, wenn sich der Stand ändert – nicht vorher.
