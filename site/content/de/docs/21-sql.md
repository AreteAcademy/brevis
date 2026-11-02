---
title: SQL
description: Einfache .sql-Dateien werden in Abhängigkeitsreihenfolge zu Tabellen und Views – als Workflow-Step oder eigenständig.
group: Modellierung in SQL
order: 21
slug: sql
---

Kein Templating: Ein Modell ist eine Datei, die gültiges SQL bleibt, und die Reihenfolge ergibt sich aus dem Lesen des SQL selbst. Postgres und BigQuery, unter derselben Konformitätssuite.

![Der Ablauf von SQL: brevis-sql liest raw.orders, eine Tabelle, die das SDK oder das Gateway gelandet hat. Das Modell staging.stg_orders wird zu einem View darüber und marts.orders zu einer Tabelle über diesem View; die Reihenfolge ergibt sich aus dem Lesen des SQL. Dieselben Modelle bauen auf Postgres oder BigQuery.](/assets/flow-sql.svg)

## Sechs Befehle

| Befehl | Was er tut |
|---|---|
| `brevis-sql compile` | parst jedes Modell und löst jede Kante auf, **ohne sich mit irgendetwas zu verbinden** |
| `brevis-sql graph` | gibt die abgeleiteten Kanten aus, damit eine falsche gesehen und nicht entdeckt wird |
| `brevis-sql build` | legt jedes Modell an oder ersetzt es, in Abhängigkeitsreihenfolge |
| `brevis-sql test` | führt die Tests jedes Modells aus; jeder ist ein SELECT, das nichts finden darf |
| `brevis-sql serve` | beantwortet schreibgeschützte Vorschauen und Abfragen über HTTP, sodass eine Console keine Warehouse-Zugangsdaten braucht |
| `brevis-sql generation bump` | hält fest, dass eine Tabelle neu gebaut wurde, sodass die gecachten Antworten von `brevis-sql-api` darüber veralten – `build --generations-from` tut das für jedes Modell, das es baut |

`--select orders+` verengt jeden davon auf ein Modell und alles, was ihm nachgelagert ist.


## `brevis-sql serve` – ein Warehouse ohne Zugangsdaten lesen

Die Bildschirme Daten und SQL der Console zeigen, was gelandet ist und was darin steht. Keiner erreicht ein Warehouse: Sie fragen diesen Dienst, der neben ihnen läuft und die Zugangsdaten hält, die die Engine bewusst nicht hält.

```bash
brevis-sql serve --connections brevis.yaml --addr 127.0.0.1:8088
```

Eine Obergrenze ist kein Budget: `--max-bytes` begrenzt eine Abfrage, und `--budget` begrenzt die Summe pro Verbindung und Stunde – gegen den Kostenvoranschlag geprüft, sodass eine Verweigerung kommt, bevor das Geld ausgegeben ist. Es begrenzt nur Warehouses, die Bytes melden, also BigQuery und nicht Postgres; das Start-Banner sagt, welche Haltung gilt, auch dass keine gilt.

**Erreichbar ist nur, was die Datei deklariert**, und standardmäßig ist nichts, was es tut, ein Schreibvorgang: Jede Anweisung wird vor der Ausführung klassifiziert, ein Lesevorgang, der mehr als `--max-bytes` scannen würde, wird bepreist und verweigert, bevor er etwas kostet, und das Warehouse selbst wird gebeten, Schreibvorgänge zu verweigern. Die Audit-Zeile trägt einen Hash der Anweisung und nie die Anweisung.

**Es braucht kein eigenes Service Account mehr.** `brevis-sql build` legt Tabellen an, seine Rolle kann also schreiben, und ein Dienst, der jede solche Rolle verweigerte, bedeutete ein zweites Konto, das anzulegen, zu pflegen und zu rotieren ist. Die Rolle ist nicht mehr das Einzige, das die Linie hält: Postgres führt jede Anweisung innerhalb einer Transaktion aus, die `READ ONLY` geöffnet wurde – wo `SELECT nextval('s')`, ein Schreibvorgang, den kein Klassifizierer im Inneren sieht, von Postgres selbst verweigert zurückkommt –, und BigQuery meldet beim Dry Run, der es bepreist, welche Art Anweisung es ist, sodass ein `CREATE TABLE` oder ein `EXPORT DATA` auf das eigene Urteil des Warehouse hin verweigert wird. Ein Warehouse, das keins von beidem bieten kann, verweigert weiterhin Zugangsdaten, die schreiben können.

Außerhalb von `BREVIS_ENV=local` ist ein Token erforderlich, und der Dienst startet ohne eines nicht.

### Ein Entwicklungs-Warehouse, das jemand ändern darf

```bash
BREVIS_SQL_SERVE_WRITES=warehouse_dev
```

Es **benennt Verbindungen**. Ein Dienst erreicht mehrere Warehouses, und das eine freizuschalten, gegen das Sie entwickeln, darf nicht das daneben freischalten. Bei einer benannten Verbindung treten alle Schichten zurück, und dieser Dienst ist genauso mächtig wie seine Zugangsdaten – die Zeilengrenze begrenzt einen Bildschirm und die Byte-Grenze begrenzt Geld, und keine begrenzt, was eine Anweisung ändert.

Ein Token ist überall erforderlich, wo es gesetzt ist, und `BREVIS_ENV=local` ist keine Ausnahme. Das Start-Banner nennt jede freigeschaltete Verbindung, die Console zeichnet ein Banner über den Editor, und `brevis_sql_writable_connection` erscheint in den Metriken. **Lassen Sie es in Produktion ungesetzt**; kein Manifest in diesem Repository setzt es.

Tokens können **benannt** werden: `BREVIS_SQL_SERVE_TOKEN_CONSOLE=…` deklariert einen Aufrufer namens `console`, und jede Audit-Zeile, die dieser Bearer erzeugt, trägt `"caller":"console"`. Die Identität ist die Zugangsberechtigung und kein Header, den ein Aufrufer ausfüllt, sodass das Log nichts sagen kann, was der Inhaber dieses Tokens nicht hielt. Auch ein verweigertes Token hinterlässt eine Zeile und hält nie fest, was vorgelegt wurde.

## Ein Modell ist eine Datei, die gültiges SQL bleibt

Die Konfiguration ist ein einleitender Blockkommentar, sodass die Datei in einem
Editor mit Syntaxhervorhebung öffnet, von Hand in einer Console läuft und
`psql -f` sie ausführt.

```sql
/* brevis
materialized: table
tests:
  - not_null: [order_id]
  - unique: [order_id]
  - relationships: {column: customer_id, to: staging.stg_customers, field: customer_id}
*/
select * from staging.stg_orders
```

**Kein Templating.** `models/<schema>/<name>.sql` ist `<schema>.<name>`, und
Abhängigkeiten ergeben sich aus dem Lesen des SQL statt aus einem
Funktionsaufruf, den Sie schreiben müssten. `brevis-sql graph` gibt aus, was
abgeleitet wurde; `depends_on:` im Header ist die explizite Überschreibung für den
seltenen Fehlgriff.

## Von compile bis test

Zwei Modelle: ein View, der `raw.orders` bereinigt, und eine Tabelle, die von ihm abhängt.

`models/staging/stg_orders.sql`:

```sql
/* brevis
materialized: view
*/
select order_id, customer_id, amount, ordered_at
from raw.orders
where amount is not null
```

`models/marts/orders.sql`:

```sql
/* brevis
materialized: table
tests:
  - not_null: [customer_id]
  - unique: [customer_id]
*/
select customer_id, count(*) as orders, sum(amount) as revenue
from staging.stg_orders
group by customer_id
```

`compile` und `graph` verbinden sich mit nichts:

```bash
docker run --rm -v ./project:/project:ro brevissh/brevis-sql:0.6.0 \
  compile --project /project
```

```text
2 models, 2 to build, 0 function(s)
  staging.stg_orders                 view  0 test(s)
  marts.orders                       table 2 test(s)
```

```bash
docker run --rm -v ./project:/project:ro brevissh/brevis-sql:0.6.0 \
  graph --project /project
```

```text
staging.stg_orders
  └─ raw.orders (source)
marts.orders
  ├─ staging.stg_orders
```

`build` und `test`, gegen eine Postgres:

```bash
docker run --rm -v ./project:/project:ro \
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db brevissh/brevis-sql:0.6.0 \
  build --project /project --dsn-from BREVIS_SQL_DSN
```

```text
  staging.stg_orders                 view  new   4ms
@brevis:{"type":"landed","target":"postgres://postgres/staging/stg_orders"}
  marts.orders                       table new   4ms
@brevis:{"type":"landed","target":"postgres://postgres/marts/orders","rows":2}
2 models built on postgres
```

```bash
docker run --rm -v ./project:/project:ro \
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db brevissh/brevis-sql:0.6.0 \
  test --project /project --dsn-from BREVIS_SQL_DSN
```

```text
2 tests passed on postgres
```

Die echte Ausgabe, vom veröffentlichten Image – compile und graph verbinden sich mit nichts. Gegen eine Postgres enden build und test mit „2 models built“ und „2 tests passed“.

### Wenn ein Test fehlschlägt

Bei einer Bestellung ohne Kunde in `raw.orders` besteht `build`, der Test `not_null` aber nicht. Der Fehler nennt das Modell, die Spalte, wie viele Zeilen und **die Abfrage** – die Sie in eine Console einfügen und immer weiter eingrenzen:

```text
FAIL  marts.orders                   not_null         customer_id          1 row(s)
      SELECT COUNT(*) FROM (SELECT customer_id FROM marts.orders WHERE customer_id IS NULL) AS v
brevis-sql: 1 of 2 tests failed
```

Der Prozess endet mit Exit-Code 1, und der Workflow-Step schlägt damit fehl.

## Ein inkrementelles Modell und sein Lookback

`materialized: incremental` braucht einen `unique_key` und einen `watermark`. Jeder Build
liest nur die Zeilen oberhalb des größten Watermarks, der schon im Ziel liegt, und
führt sie per MERGE über den Schlüssel ein. Dieser Filter ist strikt: Eine Zeile, die
mit dem Watermark gleichauf liegt, aber erst nach dem Build committet wird, oder die
verspätet mit einem älteren Zeitstempel eintrifft, wird bis zum `--full-refresh` nicht
gesehen. Ein optionaler `lookback` behebt das:

```sql
/* brevis
materialized: incremental
unique_key: [order_id]
watermark: updated_at
lookback: 2h
*/
```

Der Wert ist eine ganze Zahl mit `s`, `m`, `h` oder `d`; alles andere wird unter
Nennung des Modellnamens abgelehnt. Der Filter lautet dann `updated_at >= max - 2h`,
und das MERGE nimmt die wiederholten Zeilen auf. Der Watermark muss eine
TIMESTAMP-Spalte sein, sobald ein Lookback gesetzt ist. Ohne `lookback` bleibt das SQL
unverändert.

## Tests sind SELECTs, die verletzende Zeilen zurückgeben

`not_null`, `unique`, `accepted_values`, `relationships`. Null Zeilen ist ein Bestehen, und es gibt nichts weiter zu interpretieren.

## Es sagt, was es geschrieben hat

Jedes materialisierte Modell gibt die `@brevis:`-Zeile aus, die die Brevis-Engine
ohnehin liest, sodass es neben allem anderen auf `/data` erscheint (siehe die
Ausgabe von `build` oben). Ein View meldet keine Zeilenanzahl statt null: Er hält
keine, und eine Null dort ist nicht von einer Tabelle zu unterscheiden, die
wirklich leer ist.

## Zwei Warehouses, eine Suite

PostgreSQL und BigQuery (`--dialect bigquery`), und beide führen dieselbe
Test-Suite aus – geschrieben, als es nur eine Implementierung gab, damit die
zweite nicht unbemerkt abweichen konnte.

BigQuery authentifiziert sich mit Application Default Credentials: Bei einer
Workload Identity ist das die eigene Identität des Pods, und `--dsn-from` nennt
die Variable, die die Projekt-Id hält.

## 13 MB

Distroless, nonroot, keine Shell. Der offizielle BigQuery-Client hat 31 MB und
526 Pakete in einem leeren Binary, daher spricht dieses stattdessen über den
REST-Endpoint mit BigQuery – etwa 200 Zeilen, mit einem Test für jede Art, auf
die es unbemerkt falsch liegen könnte. Ein Gate im Repository verweigert diesen
Client namentlich, damit die Entscheidung ein routinemäßiges Dependency-Update
übersteht.

## Gegen dbt verifiziert

jaffle-shop, von diesem Image gebaut auf denselben Seed-Daten, die dbt verwendete:

|  | Ergebnis |
|---|---|
| Zeilen und md5 jeder Zeile, pro Modell | **identisch, 13 von 13** |
| Spalten und Typen, pro Modell | **identisch, 13 von 13** |
| 13 Modelle, erster Build | 1,56 s |

## Was es nicht tut

**Keine dbt-Kompatibilität**, und das ist eine Entscheidung und keine Lücke. Etwa
ein Drittel der Modelle eines echten dbt-Projekts nutzt nichts außer `ref`,
`source` und `config`; der Rest nutzt Makros, und Pakete sind Templating by
Design. Eine halbe Kompatibilität, die niemand vorhersagen kann, ist schlechter
als keine.

## Wie es weitergeht

| Wenn Sie ... möchten | gehen Sie zu |
|---|---|
| sehen, wo SQL unter den vier Bausteinen sitzt | [Ökosystem](/docs/ecosystem/) |
| brevis-sql als geplanten Step ausführen | [Core](/docs/core/) |
| die Daten landen, die es modellieren wird | [Go-SDK](/docs/sdk/) oder [Ingestion](/docs/ingestion/) |
