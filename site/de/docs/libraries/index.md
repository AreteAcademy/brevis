# Client-Bibliotheken

> Wie ein Step in jeder Sprache den Kontext und die Uhr des Runs liest – und welche Sprachen bereits eine Bibliothek haben.

*https://brevis.sh/de/docs/libraries/ · brevis.sh docs (de)*

---

Ein Step braucht vom Orchestrator zwei Dinge: **was der vorherige Step
veröffentlicht hat** und **welche Uhr dieser Run lesen soll**. Die
Client-Bibliotheken reichen diese beiden weiter, und sonst nichts.

## Der Vertrag kommt vor den Bibliotheken

Was die Engine definiert, sind **zwei Umgebungsvariablen und eine Datei**:

| | |
|---|---|
| `BREVIS_INPUT` | `{"<step id>": {…}}` – was die Abhängigkeiten dieses Steps veröffentlicht haben |
| `BREVIS_OUTPUT` | ein Pfad, in den **ein** JSON-Objekt geschrieben wird |

Dazu kommen die Variablen des Runs und die Auto-Parameter, alle nur lesbar:
`BREVIS_RUN_*` und `BREVIS_AUTO_*`. Unter Kubernetes ist `BREVIS_OUTPUT`
`/dev/termination-log`, den die Engine ohnehin liest, um den Exit-Code zu
erhalten.

Es gibt also **keinen API-Aufruf, kein Token und keinen Port**. Ein Step in jeder
Sprache nimmt mit einem JSON-Parse und einem Schreibvorgang in eine Datei teil:

```bash
window=$(jq -r '.extract.watermark' <<< "$BREVIS_INPUT")
./publish --until "$window"
jq -n --arg n "$(date -Is)" '{published_at: $n}' > "$BREVIS_OUTPUT"
```

Eine Bibliothek eröffnet hier keine neue Fähigkeit – sie erspart Ihnen die
Validierung, den Umgang mit fehlenden Werten und die Fehlermeldungen, die man
besser nicht neu schreibt.

## Welche es gibt

| Sprache | | |
|---|---|---|
| **Python** | `pip install brevis` | [verfügbar](/de/docs/python/index.md) – `0.2.1` auf PyPI |
| **Go** | `brevis/sdk/context` | verfügbar, innerhalb des SDK-Moduls |
| **Node.js** | — | geplant |
| **Rust** | — | geplant |
| alles andere | die Variablen oben | heute |

:::note Go lebt nicht in `lib/`
Die Kontextunterstützung für Go lebt im Modul `sdk/`, das das Projekt ohnehin
veröffentlicht, als Unterpaket, das für jeden, der es nicht importiert, auf nichts
zusammenschrumpft. Ein `lib/go-context` würde ein zweites Go-Modul für ein Paket
bedeuten, das bereits eines hat.

Die Regel lautet **ein Verzeichnis pro Sprache, die noch kein veröffentlichtes
Artefakt hat**.
:::

Jede Bibliothek ist ein eigenes Artefakt mit eigener Version und eigenem Tag: Ein
Fix im Python-Client erzwingt kein Engine-Release.

## Was sie sind und was nicht

Sie sind **schlanke Clients über dem Vertrag**, keine Portierungen des Go-SDK.

Eine Portierung der ETL-Maschinerie des SDK – Treiber, Paginierung, Checkpoints,
Ingestion-Ids – gehört nicht hierher und wohl nirgendwohin: Jede Sprache, auf die
das zielen würde, hat dafür bereits bessere Werkzeuge, und die Aufgabe von Brevis
ist es, sie auszuführen. Deshalb gibt es die Python-Bibliothek: damit ein Team
pandas, Polars oder dbt nutzen kann und dennoch das eine bekommt, wofür ein
Orchestrator da ist.

## Metriken

Die Bibliothek **gibt** Metriken **aus**, und die Engine macht sie verfügbar:

```python
from brevis import metrics

metrics.set("rows_loaded", 48213)      # a gauge: the last value wins
metrics.inc("vendor_rejected_total")   # a counter: it adds up
```

Sie kommen aus dem `/metrics` der **Engine**, auf dem Port des Schedulers, mit
Workflow und Step als Labels – siehe [Observability](/de/docs/observability/index.md).

**Das öffnet keinen Port, und es könnte keinen öffnen.** Ein Step läuft vierzig
Sekunden in einem eigenen Pod und endet; ein Port, den er öffnete, würde nie
abgefragt oder einmal durch Zufall. Die Zeile geht also nach stdout – dieselbe
Leitung, die die Engine ohnehin liest, um die Phasen einer Pipeline zu zeichnen –,
und die Engine hält sie fest. Nichts zu installieren, nichts zu betreiben, und die
Labels stammen von der Engine, weil ein Step den Slug seines eigenen Workflows
nicht kennen kann.

Die Details der API stehen unter [Python](/de/docs/python/#metrics-only-your-pipeline-knows).

## Einen Client für eine andere Sprache schreiben

Wenn Sie vor uns bei Node.js oder Rust ankommen, hier ist, was ein Client richtig
machen muss – und was die Python-Implementierung bereits auf die harte Tour
gelernt hat:

1. **Schlüssel, die nach Step qualifiziert sind.** `get("bucket")` ohne Step muss
   verweigert werden. Zwei Steps können `bucket` veröffentlichen, und einen per
   Suche auszuwählen ist Vorrang durch Zufall.
2. **Schreibvorgänge zusammenführen.** Zwei Aufrufe mit verschiedenen Schlüsseln
   behalten beide; derselbe Schlüssel zweimal behält den letzten.
3. **Nur beim Prozessende schreiben.** Ein Step, der hart stirbt, veröffentlicht
   nichts – was korrekt ist, denn die Ausgabe eines abgestürzten Steps beschrieb
   Arbeit, die nicht fertig wurde.
4. **Über 4096 Byte verweigern.** Die Plattform **schneidet ab**, statt zu
   verweigern, und ein in der Mitte durchgeschnittenes Objekt kommt beim nächsten
   Step als ungültiges JSON an.
5. **Außerhalb von Brevis funktionieren.** Ohne `BREVIS_INPUT` liefert `get()` den
   Standardwert, und `set()` nimmt an und verwirft, ohne eine Ausnahme zu werfen:
   Ein Skript, das sich nicht von Hand ausführen lässt, lässt sich nicht
   entwickeln.

## Nächste Schritte

- [Python](/de/docs/python/index.md) – die vollständige API
- [Kontext zwischen Steps](/de/docs/context/index.md) – das Konzept und die Obergrenze von 4096 Byte
- [Parameter](/de/docs/parameters/index.md) – die deklarierten und die automatischen
