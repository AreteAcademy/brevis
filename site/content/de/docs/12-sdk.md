---
title: Go-SDK
description: Einen Fetcher schreiben – aus einer Quelle extrahieren, transformieren und in ein Ziel laden.
group: SDK und Bibliotheken
order: 12
slug: sdk
---

Wenn die Aufgabe eines Steps darin besteht, **Daten abzurufen und irgendwohin zu
laden**, gibt Ihnen das Go-SDK den ganzen Fetcher in wenigen Zeilen. Es ist ein
eigenes Modul, unabhängig von der Engine versioniert.

```bash
go get github.com/AreteAcademy/brevis/sdk@latest
```

Erfordert Go 1.23 oder neuer – das SDK streamt Zeilen als `iter.Seq2`.

:::warning Verwenden Sie nicht `v0.1.0`
Sie wurde mit einer `go.mod` ausgeliefert, die eine Revision festschreibt, die
nicht existiert, und der Go-Modul-Proxy ist unveränderlich. Beginnen Sie bei
`v0.1.1`.
:::

## Drei Schritte

```go
import (
	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to/bigquery"
)

rows, err := sdk.Extract(ctx, sdk.Source{
	From: from.HTTP{URL: "https://api.example.com/v1/events"},
})

rows = sdk.Transform(rows, sdk.Accept("id", "created_at", "amount"))

res, err := sdk.Load(ctx, rows, sdk.Target{
	To:      bigquery.Table{Dataset: "bronze", Name: "events"},
	Columns: []string{"id", "created_at", "amount"},
})
```

`Extract` liest, `Transform` formt um, `Load` schreibt. Jedes nimmt eine Sequenz
entgegen und gibt eine zurück – nichts wird auf einmal im Speicher materialisiert.

![Der Ablauf des SDK: sdk.Extract liest mit from.HTTP aus einer HTTP-API, sdk.Transform behält die akzeptierten Felder, und sdk.Load schreibt die Zeilen in eine BigQuery-Tabelle. Die drei Aufrufe laufen innerhalb Ihres eigenen Steps.](/assets/flow-sdk.svg)

## Der Treiber ist ein Wert, keine Einstellung

`from.HTTP` trägt alles, was eine HTTP-Quelle braucht: URL, Header, Retry,
Paginierung und was eine Antwort bedeutet. `from.Files` trägt einen Pfad und ein
Format. Keines muss Platz für die Felder des anderen schaffen, es gibt also kein
Quell-Struct, das vierzig Optionen sammelt, von denen jeder Treiber sechs liest.

Er entscheidet auch, **was Sie kompilieren**. Go beschneidet Abhängigkeiten nach
importiertem Paket, nie nach verwendetem Feld:

| Was Sie importieren | Pakete | AWS | Google |
|---|---|---|---|
| `sdk` | 190 | nein | nein |
| `sdk` + `from` | 194 | nein | nein |
| `sdk` + `from` + `to` (Dateien) | 195 | nein | nein |
| `sdk` + `to/bigquery` | 456 | nein | **ja** |
| `sdk` + `from` + `store/s3` | 265 | **ja** | nein |

Eine ganze Datei-Pipeline – Lesen und Schreiben – kostet 195 Pakete und gar kein
Cloud-SDK. **Ein Treiber mit einem Anbieter-SDK dahinter lebt in einem eigenen
Paket**, weshalb BigQuery `to/bigquery` heißt und die Objektspeicher `store/s3`
und `store/gcs`.

## Quellen und Ziele

| Quelle | Paket |
|---|---|
| HTTP | `from.HTTP` |
| Dateien (lokal, S3, GCS) | `from.Files` |
| Postgres | `from/postgres` |
| MySQL | `from/mysql` |
| mehrere Quellen | `from.Many` |

| Ziel | Paket |
|---|---|
| Dateien | `to.Files` |
| BigQuery | `to/bigquery` |
| Postgres | `to/postgres` |
| MySQL | `to/mysql` |
| Redshift | `to/redshift` |
| Pub/Sub | `to/pubsub` |

Das Pfadschema bestimmt das Backend, und der `Store` wird **hineingereicht**,
statt im Treiber gewählt zu werden:

```go
from.Files{Path: "s3://bucket/day=1/*.ndjson", Store: s3.New(client)}
to.Files{Path: "gs://bucket/landing/", Store: gcs.New(client)}
```

Das ist es, was ein Programm mit lokalen Dateien davon abhält, eine einzige Zeile
AWS- oder Google-Code zu kompilieren.

### An ein Topic veröffentlichen

`to/pubsub` ist das eine Ziel, das keine Tabelle ist, und es verhält sich auf
eine Weise anders, die man vor der Nutzung kennen sollte: **Es fügt dem, was es
sendet, nichts hinzu.**

```go
Target: sdk.Target{
    To: pubsub.Topic{
        Project: "acme-prod",
        Name:    "orders",

        // Nil is the default, and it means NO attributes at all.
        Attributes: func(e sdk.Envelope) map[string]string {
            row, ok := e.Payload.(map[string]any)
            if !ok {
                return nil
            }
            return map[string]string{"orderId": fmt.Sprint(row["order_id"])}
        },
    },
},
```

Ein Subscriber erhält Ihre Payload, Byte für Byte, plus genau die Attribute, die
Sie benannt haben. Sonst nichts – keine `ingestion_id`, kein `provider`, keinen
Envelope irgendeiner Art.

Das ist eine Regel und kein Minimalismus. Eine Tabelle wird von der Pipeline
angelegt, die sie schreibt, das SDK darf also entscheiden, was ihre Spalten sind.
**Ein Topic nicht**: Es existiert, bevor Ihre Pipeline existiert, seine
Subscriber wurden zuerst geschrieben und ihre Filter ebenfalls. Ein von selbst
hinzugefügtes Attribut wäre Brevis, das den Vertrag eines anderen ändert, und der
Subscriber würde es um drei Uhr nachts herausfinden.

Daraus folgen drei Dinge:

- **Es legt nie ein Topic an.** Eine Pipeline, die eines anlegen kann, kann das
  falsche anlegen, und anders als bei einer falsch geschriebenen Tabelle bemerkt
  das niemand – die Nachrichten gehen irgendwohin, und der Subscriber, der sie
  hätte erhalten sollen, bleibt still.
- **`Schema`, `Dedup` und `PartitionBy` werden verweigert**, wobei die Option
  genannt wird. Sie existieren für Tabellen: Ein `Schema` ist die Art, wie ein
  Ziel eine anlegt, Deduplizierung braucht eine Zeile zum Ersetzen, eine
  Partition ist eine Idee der Tabelle. Das Nächste hier ist `OrderingKey`, was
  etwas anderes ist – es gruppiert Nachrichten, die in Reihenfolge *ankommen*
  müssen.
- **Ein Fehler ist partiell.** Veröffentlichen ist nicht transaktional, sodass
  48.000 Nachrichten, die bei 31.000 fehlschlagen, 31.000 *zugestellt* haben. Das
  Ergebnis meldet, was tatsächlich ging, und der Fehler sagt, dass ein erneuter
  Lauf eine Neuzustellung ist – der Subscriber muss idempotent sein, und Pub/Sub
  ist ohnehin at-least-once.

Lauffähig, gegen den Emulator:
[`examples/13-pubsub`](https://github.com/AreteAcademy/brevis/tree/master/examples/13-pubsub).

## Ein ganzer Fetcher

`sdk.Run` kümmert sich um Flags, `-dry-run`, Logging, Retry, Paginierung,
Provenienz, Tabellenanlage und den Exit-Code. In der Datei bleibt nur, was für
diese Quelle spezifisch ist:

```go
package main

import (
	"time"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
)

func main() {
	sdk.Run(sdk.Pipeline{
		Source: sdk.Source{
			From: from.HTTP{
				URL:     "https://api.example.com/v1/events",
				Timeout: 15 * time.Second,
			},
			Guard:  sdk.RejectIf("error"),
			Expand: sdk.ArrayAt("results"),
		},
		Target: sdk.Target{
			Provider: "example",
			Entity:   "events",
			Key:      sdk.Key("id"),
			When:     sdk.Field("created_at"),
		},
	})
}
```

```bash
go run ./fetcher -dry-run   # extracts, counts rows and errors, writes nothing
go run ./fetcher
```

## Der Run-Kontext

Wenn der Fetcher **als Workflow-Step** läuft, injiziert die Engine in die
Umgebung, was er sonst nicht wissen könnte: ob dies der erste Run ist, mit
welchen Parametern er gestartet wurde, welcher Run es ist. Das SDK liest das in
`Pipeline.Run`:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	if p.Run.Params["load_full"] == "true" {
		p.Source.From = from.HTTP{URL: base + "?full=1"}
	}
	return nil
},
```

Von Hand gestartet, kommt `Run` genullt zurück – es zu lesen ist optional, und es
zu ignorieren kostet nichts.

Ohne Historie lautet die Antwort auf „Ist das der erste Run?“ immer **nein**:
Eine Tabelle ohne Gewissheit anzulegen ist schlimmer, als sie nicht anzulegen.

### Die Uhr

`p.Run.Auto` ist das, was die Engine über diesen Run ermittelt hat – die
[Auto-Parameter](/docs/parameters/#auto-params). `Auto.Now()` ist die Uhr, die
statt `time.Now()` zu lesen ist: Bei einem geplanten Run ist es der Slot, er
bewegt sich also nicht, wenn der Run spät dran ist, und nicht, wenn der Run drei
Stunden später wiederholt wird.

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	start, end, ok := p.Run.Auto.Window()
	if !ok { // no schedule: fall back to a fixed window
		start, end = p.Run.Auto.Now().Add(-24*time.Hour), p.Run.Auto.Now()
	}
	p.Source.From = from.HTTP{URL: base +
		"?from=" + start.Format(time.RFC3339) +
		"&to=" + end.Format(time.RFC3339)}
	return nil
},
```

Von Hand gestartet, *ist* `Auto.Now()` die Wanduhr, und `Window()` liefert
`false`, sodass die lokale Entwicklung keinen Sonderfall braucht.

## Nicht mit den Client-Bibliotheken zu verwechseln

Dieses SDK ist ETL-Maschinerie in Go: Treiber, Paginierung, Provenienz,
Tabellenanlage. Die [Client-Bibliotheken](/docs/libraries/) sind etwas anderes –
schlanke Clients, die einem Step den Run-Kontext und die Uhr reichen, heute in
Python und später in Node.js und Rust. Ein Step mit pandas oder dbt will
Letztere, nicht dieses.

## Parameter, die die Pipeline aufbauen

`p.Run.Params` ist innerhalb der Pipeline lesbar, und das ist zu spät für einen
Wert, der entscheidet, was die Pipeline **ist** – eine Quelle pro Bundesstaat,
eine Tabelle pro Mandant. `Pipeline.Flags` wird innerhalb von `Run` geparst und
ist daher aus demselben Grund zu spät.

```go
func main() {
	sdk.Run(pipeline(sdk.ParamList("ufs")))
}
```

`sdk.Param` und `sdk.ParamList` lesen zuerst die Umgebung der Engine und
zweitens `-param name=value`:

```bash
./fetch-stations -param ufs=SP,RJ
```

Die Umgebung gewinnt – unter der Engine ist sie der Wert, und ein in einem
Manifest zurückgelassenes Flag darf nicht überschreiben, was der Operator
eingegeben hat. Ohne Engine ist das Flag das, was da ist, und das ist besser,
als bei jedem Run `BREVIS_RUN_PARAMS` als JSON zu schreiben.

## Das Landing-Layout

Eine Pipeline kann **dieselbe Tabelle landen, die ein Gateway landet** – dieselben
Spalten und dieselbe `brevis_ingestion_id` –, sodass die Wahl zwischen beiden
eine Deployment-Entscheidung ist und keine Schema-Entscheidung. Ein Team kann
beide zusammen lesen und von einem zum anderen wechseln, ohne Migration.

```go
const table = "landing.orders"

sdk.Run(sdk.Pipeline{
    Source:    sdk.Source{From: from.HTTP(/* ... */)},
    Transform: []sdk.Transformer{sdk.Landing(table, sdk.LandingKey("id"))},
    Target: sdk.Target{
        To:     postgres.Table{DSN: dsn, Name: table, CreateTable: true},
        Schema: sdk.LandingSchema(sdk.LandingOptions{}),
    },
})
```

`sdk.LandingSchema` sind neun Spalten: die acht, die das Layout besitzt, und die
eine, die der Produzent besitzt. `sdk.Landing` füllt sie und legt den Datensatz
vollständig in `data` ab. Hängen Sie eigene Spalten an das Schema an, wenn Sie
möchten, oder nehmen Sie `sdk.LandingControlColumns` und geben Sie den Feldern
des Datensatzes eigene Spalten.

### Zwei Formen: der Datensatz als Ganzes oder eine Spalte pro Feld

Oben steht die **document**-Form – der Datensatz geht als JSON in `data`, und die
Tabelle ist dieselbe, was auch immer der Datensatz enthält. Fügen Sie
`sdk.LandingColumns()` hinzu, und jedes Feld des Datensatzes erhält eine eigene
Spalte:

```go
sdk.Landing(table, sdk.LandingKey("source_key"), sdk.LandingColumns())
```

Die Tabelle besteht dann aus `sdk.LandingControlColumns` plus Ihren Spalten –
**nicht** aus `sdk.LandingSchema`, das `data` trägt:

```go
Target{
    To: postgres.Table{DSN: dsn, Name: table, CreateTable: true},
    Schema: append(sdk.LandingControlColumns(sdk.LandingOptions{Keyed: true}),
        sdk.Column{Name: "source_key", Type: sdk.TypeString},
        sdk.Column{Name: "series", Type: sdk.TypeString},
        sdk.Column{Name: "data", Type: sdk.TypeString},
        sdk.Column{Name: "valor", Type: sdk.TypeString},
    ),
}
```

**Diese Spalten deklarieren Sie.** Das SDK leitet nie ein Schema ab, und diese
Option ändert das nicht: Sie bestimmt, wie die *Zeile* gebaut wird, nie, was die
Tabelle ist. Ein Gateway leitet ab, weil sein Produzent ein Fremder ist, der
postet, was er hat; hier kennt der Autor der Pipeline die Form und schreibt sie
auf, in einem Diff, den jemand prüft.

Es ist dieselbe Form, die ein Gateway als
[`shape: columns`](/docs/ingestion-sinks/#two-shapes-one-contract) landet,
über denselben Code – dieselben Spalten, dieselben Werte, dieselbe
`brevis_ingestion_id`.

**Ein Feldname muss ein Spaltenname sein können**: ein Buchstabe oder Unterstrich,
dann Buchstaben, Ziffern und Unterstriche. Das ist die Regel von BigQuery, der
engsten der vier Zieltechnologien, sodass ein hier durchgehender Name überall
funktioniert. Ein Datensatz mit `my-field` wird namentlich verweigert, bevor
etwas geschrieben wird. Die document-Form hat keine solche Regel – ein Bindestrich
ist ein völlig gültiger JSON-Schlüssel.

**Standardmäßig wird nicht abgeflacht.** Ein verschachteltes Objekt wird zu EINER
`JSON`-Spalte unter dem Schlüssel, der es hielt: `customer` enthält
`{"id": 7, "uf": "SP"}`, nicht `customer_id` und `customer_uf`. Auch ein Array ist
`JSON`. Ein Datensatz, der eine Zeile *pro Array-Element* will, will
`sdk.ArrayAt` im `Expand` der Quelle – das läuft vor `Landing` und ist eine
andere Operation mit einem anderen Namen.
[`BREVIS_NORMALIZE_DATA`](#one-column-per-nested-field) ändert die erste Hälfte
davon für ein ganzes Deployment; ein Array bleibt in beiden Fällen `JSON`:

```go
Source: sdk.Source{From: from.HTTP(/* ... */), Expand: sdk.ArrayAt("results")},
```

**`null` bleibt `NULL`**, nie `""`. Ein als null gesendetes Feld und ein leer
gesendetes Feld sind verschiedene Tatsachen, und eine Spalte kann sie hinterher
nicht unterscheiden.

**`data` wird Ihr eigenes.** In dieser Form hat das Layout keine Spalte dieses
Namens, sodass ein Produzent mit einem Feld namens `data` – genau das, was die
Bacen-Reihe sendet – eine eigene Spalte mit seinem eigenen Wert erhält. Unter
`document` ist seines ein Schlüssel innerhalb des JSON des Layouts.

### Das Präfix `brevis_` können Sie ändern

```
BREVIS_LANDING_PREFIX=acme
```

und die acht Spalten des Layouts werden zu `acme_ingestion_id`,
`acme_record_key` und den übrigen. Ohne Angabe sind sie, was sie immer waren.

**Wählen Sie es, wenn die Tabelle angelegt wird, denn es später zu ändern ist
keine Umbenennung.** Nichts in diesem SDK löscht je eine Spalte, ein Stream, der
das Präfix wechselt, benennt also nicht acht um – er fügt acht hinzu und verlässt
acht. Jede Zeile danach hat NULLs im alten Satz, und

```sql
qualify row_number() over (partition by brevis_record_key
                           order by brevis_received_at desc) = 1
```

partitioniert über eine Spalte, die nichts mehr schreibt. Zeilen kommen an, das
Dashboard wird flach, und nichts loggt irgendetwas.

Eine Tabelle, die das Layout bereits unter einem Präfix trägt, **verweigert**
daher einen Load, der ein anderes deklariert, vor dem Extract und unter Nennung
beider. Richten Sie ihn auf eine neue Tabelle, oder benennen Sie die acht Spalten
zuerst selbst um.

**Was Sie eintippen, wird normalisiert**, sodass es eine Schreibweise gibt, bei
der man sich irren kann, statt vier:

| Sie setzen | Sie erhalten |
|---|---|
| nichts, oder leer | `brevis_` |
| `_NAME` | `name_` |
| `_NAME_` | `name_` |
| `NAME` | `name_` |

An beiden Enden `_` abschneiden, kleinschreiben, genau eines anfügen. Zwei Werte
werden verweigert, statt sie zu erraten: nur Unterstriche und sonst nichts, weil
das `ingestion_id` übrig ließe – genau die Spalte, die `sdk.IngestionID()` für
jede gewöhnliche Pipeline schreibt –, und alles, was nicht mit einem Spaltennamen
beginnen kann. **Ein Prozess mit einem unbrauchbaren Wert startet nicht**, was
das einzig ehrliche Ergebnis ist: Die Alternative sind Tabellen, die nach dem
Präfix benannt sind, das Sie ersetzen wollten.

**Es gilt pro PROZESS, und das ist der Punkt und keine Einschränkung.** Jeder
Stream in einem Gateway teilt es, zwei Sinks können sich also nicht
widersprechen – ebenso wenig wie das Schema, das eine Tabelle deklariert, und der
Transformer, der sie füllt.

**Die Regel zu reservierten Namen wandert mit.** Unter `acme_` wird ein
Produzent, der `acme_region` sendet, verweigert, weil jemand, der eine
Steuerspalte fälschen kann, eine echt aussehende fälscht. Und `brevis_region`
wird ein gewöhnliches Feld.

`data` trägt nie das Präfix. Es ist die Spalte des Produzenten, nicht die des
Layouts.

### Eine Spalte pro verschachteltem Feld

```
BREVIS_NORMALIZE_DATA=true
```

und die Felder eines verschachtelten Objekts erhalten eigene Spalten, wie es
`json_normalize` von pandas tut – eine Ebene:

```python
data = [
    {"id": 1, "name": {"first": "Coleen", "last": "Volk"}},
    {"name": {"given": "Mark", "family": "Regner"}},
    {"id": 2, "name": "Faye Raker"},
]
```

```
id  name_first  name_last  name_given  name_family  name
```

**Wählen Sie es vor der ersten Tabelle, denn es FÜGT nicht nur Spalten hinzu.** Es
benennt sie um, und nichts in diesem SDK löscht je eine – der alte Name bleibt
also bei den Zeilen, die schon darin liegen, während alles danach im neuen landet,
und die Abfrage, die jemand dagegen geschrieben hat, läuft weiter und sieht keine
neuen Zeilen mehr. Zwei Umbenennungen geschehen:

- `userName` wird zu `username`, ob etwas verschachtelt ist oder nicht
- `name`, das Objekte hielt, wird durch `name_first` und `name_last` ersetzt

Eine Tabelle, die bereits eine der beiden Formen trägt, **verweigert** einen Load
mit eingeschalteter Option, vor dem Extract und unter Nennung beider Spalten.
Richten Sie ihn auf eine neue Tabelle, oder benennen Sie die Spalten zuerst selbst
um.

**`name` kann sowohl abgeflacht als auch ganz vorkommen**, und das Beispiel oben
zeigt es: Der dritte Datensatz trägt `name` als String, die Tabelle hat also
`name_first`, `name_last` UND `name`. Ein Feld, das in einem Datensatz ein Objekt
und in einem anderen ein Skalar ist, erzeugt beides.

**Eine Ebene, und Arrays sind Werte.** `{"a": {"b": {"c": 1}}}` ergibt `a_b` mit
`{"c": 1}` – eine `JSON`-Spalte, nicht `a_b_c`. `{"a": [1, 2]}` ergibt `a`, ebenfalls
`JSON`. Ein Datensatz, der eine Zeile pro Array-Element will, will weiterhin
`sdk.ArrayAt`.

**Verbunden und kleingeschrieben, nicht nach camelCase getrennt.** `userName`
wird zu `username` und nicht zu `user_name`, weil die andere Lesart bei
Akronymen – `HTTPStatus`, `userID` – keine vereinbarte Antwort hat und jede
Bibliothek anders verfährt.

**Eine Kollision wird verweigert, nie aufgelöst.** `{"name_first": 1, "name":
{"first": 2}}` würde aus zwei Feldern eine Spalte machen, und die Map-Iteration
von Go ist randomisiert: Einen gewinnen zu lassen gäbe demselben Batch in einem
anderen Lauf eine andere Tabelle. Die Verweigerung nennt beide.

**Das reservierte Präfix wird an der Spalte geprüft, nicht am Feld.** Ein
Produzent, der `{"brevis": {"stream": "x"}}` sendet, würde sonst `brevis_stream`
durch eine Tür erzeugen, die die Feldprüfung nicht bewacht. Es wird verweigert
und nennt `brevis.stream`, damit das Feld auffindbar ist.

**Es gilt pro PROZESS**, wie das Präfix, und aus demselben Grund: Jeder Stream in
einem Gateway teilt es, zwei Sinks können sich also nicht über eine Tabelle
widersprechen, die beide schreiben könnten. Es gilt nur für die `columns`-Form –
unter `document` geht der Datensatz ganz in eine `JSON`-Spalte, und es gibt
nichts, in das man abflachen könnte.

### Text, der bereits JSON ist

Eine als `sdk.TypeJSON` deklarierte Spalte erhält einen JSON-**Wert** – ein
Objekt, ein Array, eine Zahl. Bei BigQuery wird die Zeile als Ganzes kodiert,
sodass ein Go-`string` dort zu einem JSON-String-Literal wird und die Spalte am
Ende den TEXT Ihres Objekts hält statt des Objekts:

```sql
SELECT JSON_TYPE(payload) ...;            -- "string", not "object"
SELECT JSON_VALUE(payload, '$.a') ...;    -- NULL
```

Die Spalte hat den richtigen Typ, und die Daten darin sind nicht erreichbar, was
das Schlimmste der drei Ergebnisse ist, weil nichts fehlschlägt. **Das
BigQuery-Ziel verweigert es** ab `v0.74.0` und nennt die Spalte.

Übergeben Sie das Objekt selbst, wo Sie eines haben. Wo das, was Sie halten,
bereits JSON-Text ist – eine aus einer anderen Datenbank gelesene Spalte, ein
Antwortkörper, den Sie nicht neu parsen wollen –, sagen Sie es:

```go
row["payload"] = sdk.JSONText(body)
```

`json.RawMessage` aus `encoding/json` funktioniert ebenfalls, aus demselben
Grund: Beide schreiben das JSON, das sie halten, statt eines zitierten Strings
davon.

**Postgres und MySQL sind nicht betroffen und werden nicht verweigert.** Ihr
Server parst einen String in eine JSON-Spalte, und hat es immer getan.

### Wenn ein Feld auftaucht, das die Tabelle nicht hat

Ein Produzent fügt ein Feld hinzu, und der Load stoppt und nennt es: Die Zeile
trägt eine Spalte, die nichts deklariert hat, und sie in ein Ziel zu schreiben,
das sie nie erwähnt hat, würde sie still verwerfen. Diese Verweigerung ist
standardmäßig richtig – aber bei einer Landing-Tabelle ist es die eine Änderung,
die keinen Leser brechen kann, und das Ziel kann sie vornehmen:

```go
postgres.Table{DSN: dsn, Name: table, Evolve: sdk.EvolveAdditiveFromPayload}
```

Der Batch **vervollständigt** dann Ihre Deklaration: Ein Feld, das er trägt und
die Deklaration nicht, wird zu einer Spalte, `STRING`, außer das Feld ist ein
Objekt oder ein Array, dann `JSON`. Es ist dieselbe Regel und derselbe Code, mit
dem ein Gateway `shape: columns` landet, beide erzeugen also dieselbe Tabelle.

Zwei weitere Werte, und der Unterschied zwischen ihnen ist, woher die Spalte
kommt:

| `Evolve` | fügt hinzu |
|---|---|
| `sdk.EvolveNone` (Standard) | nichts. Jeder Unterschied wird verweigert, beide Seiten werden genannt |
| `sdk.EvolveAdditive` | eine Spalte, die Ihre **Deklaration** hat und die Tabelle nicht |
| `sdk.EvolveAdditiveFromPayload` | das, plus eine Spalte, die der **Batch** trägt |

**Immer nur `ADD COLUMN`.** Eine Spalte, die aus der Quelle verschwindet, wird
nicht mehr geschrieben und bleibt in der Tabelle; nichts wird umbenannt, nichts
gelöscht, und kein Typ wird je verengt. Das ist keine Lücke – eine
Landing-Tabelle ist Historie, und die eine Änderung, die niemanden brechen kann,
der sie liest, ist eine neue Spalte, die NULL zulässt.

**Es vervollständigt eine Deklaration; es ersetzt keine.** Ohne Deklaration wird
es vor dem Extract verweigert, unter Nennung der Tabelle: Eine ganze Tabelle aus
einer Payload anzulegen ist eine andere Entscheidung, und das SDK trifft sie
nicht. Beim Landing-Layout wäre der Schaden konkret – `brevis_received_at` käme
als Text statt als Zeitstempel heraus, und `brevis_loaded_at` reist gar nicht in
der Zeile, sodass die Ende-zu-Ende-Latenzmessung schlicht fehlte.

**Das SDK leitet weiterhin nicht ab.** Der Typ stammt aus der FORM des Feldes,
nie aus seinem Wert: `21129` und `8.89` deklarieren beide `STRING`, sodass sich an
dem Tag, an dem die Reihe eine ganze Zahl veröffentlicht, nichts ändert. Ein aus
dem ersten Batch gelesener Typ ist der Fehler, den diese Regel verhindern soll.

**Und ein `null` entscheidet nichts – bleibt aber in der Zeile.** Das sind zwei
verschiedene Fragen, und sie mit einer Antwort zu beantworten, kostete v0.77.0.

Ein Null hat keine Form, das **Schema** hat also nichts zu lesen: Ein Feld, das
als `null` ankommt, deklariert keine Spalte, und die Spalte erscheint ab dem
ersten Datensatz, der einen WERT trägt, mit der Form, die dieser Wert hat. Andersherum
würde ein Null mangels Antwort zu `STRING`, und das erste Array danach ließe das
Ziel den ganzen Batch verweigern – ohne eine Migration von `STRING` zu `JSON`,
die das rückgängig machte.

Die **Zeile** trägt, was der Produzent gesendet hat, Null eingeschlossen. Sie
muss: Eine Spalte, die Sie in `Target.Schema` DEKLARIERT haben und die leer
ankam, muss in der Zeile sein, sonst liest die Prüfung, die die Deklaration
dagegen vergleicht, eine Kette, die sie nicht mehr erzeugt – und verweigert den
Load. Das tat v0.77.0, und 0.78.0 machte es rückgängig.

Den Rest regelt das Ziel: Ein Feld, das in JEDEM Datensatz des Batches null ist
und keine Spalte in der Tabelle hat, wird nirgends geschrieben und verweigert
nichts. Ein Null und ein fehlendes Feld landen als dasselbe `NULL`, es geht also
nichts verloren.

**Ein Feld, das seine Form ändert, wird verweigert, nicht aufgenommen.** Kam
`valor` als Skalar an und machte eine Textspalte, ist ein später eintreffendes
Objekt ein Wechsel der Art, und der Batch stoppt – mit dem eigenen Kommentar der
Spalte in der Meldung, damit Sie sehen, dass niemand es deklariert hat. Benennen
Sie die Spalte um, oder migrieren Sie sie selbst.

**Bei BigQuery sind zwei Schreibweisen eines Namens eine Spalte.** BigQuery
faltet die Groß- und Kleinschreibung von Spalten: Ein Batch, der in einem
Datensatz `nationalID` und in einem anderen `nationalId` trägt, deklariert
**eine** Spalte, nicht zwei, und die Schreibweise, die gewinnt, ist die erste in
sortierter Reihenfolge – `nationalID`, weil `D` vor `d` sortiert. Nicht die
zuerst eingetroffene: Derselbe wiederholte Batch darf keine anders geschriebene
Spalte erzeugen. Eine Deklaration, die `createdat` sagt, gegen eine Tabelle, deren
Spalte `createdAt` heißt, ist ebenfalls dieselbe Spalte, sodass nichts hinzugefügt
und nichts verweigert wird.

**Ein Datensatz, der beide Schreibweisen trägt, wird unter Nennung beider
verweigert.** Das Ziel hat eine Spalte, und einer der beiden Werte würde still
gewinnen, also stoppt der Load: Benennen Sie eine davon in `Transform` um, oder
bilden Sie beide auf ein einziges Feld ab. Zwei *Datensätze*, die je eine
Schreibweise tragen, sind in Ordnung – das ist gewöhnlicher CDC-Verkehr, und ihn
zu landen ist, wofür diese Regel existiert.

**Nichts wird migriert.** Eine Tabelle, die bereits zwei Spalten hat, die sich nur
in der Groß- und Kleinschreibung unterscheiden, behält beide; keine wird
umbenannt und keine gelöscht.

**Postgres und MySQL falten nicht.** Ein gequoteter Bezeichner ist dort eigen,
zwei Schreibweisen sind also zwei Spalten – mit Absicht, und derselbe Fetcher auf
BigQuery gerichtet landet sie in einer.

**Jede Spalte, die ein Batch angelegt hat, sagt das in der Tabelle.** Ein
Kommentar bei Postgres und MySQL, eine Feldbeschreibung bei BigQuery, mit dem
Datum:

```
brevis: added from a batch on 2026-09-29; the type is the landing rule
(scalar text, object and array json), not a decision
```

Sechs Monate später ist das das Einzige, was noch „Wann ist diese Spalte
aufgetaucht, und wer hat das entschieden?“ beantwortet. Eine Logzeile ist bis
dahin rotiert.

**Es ist kein Modus.** `Target.Schema` bleibt Ihre Deklaration dessen, was die
Tabelle ist – das hier reicht Ihnen ein bereits existierendes Layout, statt Sie
es tippen zu lassen. Nichts im SDK verhält sich anders, weil Sie es benutzt
haben, und eine Pipeline, die ihr eigenes Schema deklariert, ist unberührt.

**Schreiben Sie den Tabellennamen einmal.** Er geht an `Landing`, weil er der
zweite Slot der Id ist, und an das Ziel, weil dort die Zeilen hingehen. **Nichts
prüft, ob beide übereinstimmen**: Der `Writer` kennt seine eigene Tabelle, legt
sie aber nur über `Describe` offen, den Namen für Logs und Fehler, und eine darauf
gebaute Id würde jede bereits geschriebene Id an einen Log-String binden. Bringen
Sie beide aus dem Takt, landen die Zeilen korrekt, mit Ids, die für eine Tabelle
geprägt sind, in die niemand schrieb. Sie sehen gut aus. Sie stimmen nicht mit
denen des Gateways überein.

**Drei Spalten sind `NULL`, und das ist die ehrliche Antwort.** `brevis_stream`
und `brevis_gateway` benennen Dinge, die eine Pipeline nicht hat –
`brevis_gateway IS NULL` ist, wie man die Zeile einer Pipeline von der eines
Gateways unterscheidet. `brevis_received_bytes` ist ebenfalls `NULL`: Die des
Gateways zählt, was über die Leitung ankam, Envelope eingeschlossen, und das
eigene JSON des Datensatzes ist etwa 30 % kleiner. Eine Spalte mit zwei
Bedeutungen würde eine Summe über Zeilen beider Wege um den jeweiligen Anteil
verfälschen. `length(data)` beantwortet die Version der Pipeline exakt.

**Zusammenführen braucht `DedupKey`, und ohne ihn gibt es gar kein Merge.** Jeder
Treiber gleicht auf `ingestion_id` ab, sofern nichts anderes gesagt wird, und die
Identitätsspalte dieses Layouts ist `brevis_ingestion_id`:

```go
Target{
    To:       postgres.Table{DSN: dsn, Name: table, CreateTable: true},
    Schema:   sdk.LandingSchema(sdk.LandingOptions{UniqueID: true, Keyed: true}),
    Dedup:    sdk.DedupMerge,
    DedupKey: sdk.LandingColumnID,
}
```

`UniqueID` setzt die UNIQUE-Constraint auf die Id, und Postgres und MySQL
verweigern beide das Zusammenführen ohne eine – `ON CONFLICT` hat nichts, worauf
es abgleichen kann. Ein `DedupKey`, den Ihr `Schema` nicht deklariert, wird vor
dem Start verweigert, weil ein Merge auf eine Spalte, die die Tabelle nicht hat,
nichts trifft, und ein Merge, der nichts trifft, sieht genau so aus wie einer, der
alles trifft, was er soll.

**Pro Ziel, und heute kann MySQL es nicht.** BigQuery hat keine
Unique-Constraints und verweigert die Deklaration rundheraus. Das `string` von
MySQL ist `LONGTEXT`, und MySQL indiziert ein `LONGTEXT` nicht ohne Länge – eine
zusammenführende Landing-Tabelle lässt sich dort also gar nicht *anlegen*:

```
Error 1170 (42000): BLOB/TEXT column 'brevis_ingestion_id' used in key
specification without a key length
```

Postgres funktioniert, weil sein `string` `TEXT` ist. Landen Sie bei MySQL mit
`DedupNone` und lösen Sie die aktuelle Version nachgelagert auf, oder legen Sie
die Tabelle selbst mit `CreateSQL` und einer Id-Spalte mit Länge an.

**Die Id ist inhaltsadressiert.** Derselbe Datensatz erzeugt über beide Wege
dieselbe Id, und ein geänderter Datensatz erzeugt eine neue – wodurch ein
erneuter Lauf ein No-op statt eines Duplikats ist.

## Referenz

- [pkg.go.dev](https://pkg.go.dev/github.com/AreteAcademy/brevis/sdk) – die vollständige API
- [`examples/`](https://github.com/AreteAcademy/brevis/tree/master/examples) – zwölf lauffähige Beispiele
- [`CHANGELOG.md`](https://github.com/AreteAcademy/brevis/blob/master/CHANGELOG.md) – Historie Version für Version
