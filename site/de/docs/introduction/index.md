# Einführung

> Was brevis.sh ist, welches Problem es löst und wann es nicht die richtige Wahl ist.

*https://brevis.sh/de/docs/introduction/ · brevis.sh docs (de)*

---

brevis.sh ist eine **in Go geschriebene Laufzeitumgebung für Datenorchestrierung**.
Ein einziges Binary vereint, wofür man sonst vier Werkzeuge braucht:
deklarative Transformation, Workflow-Orchestrierung, eine persistente Queue,
einen Scheduler und eine Oberfläche, um all das zu betreiben.

Es ist Open Source unter der MIT-Lizenz, und das Projekt entsteht bei
[Aretê Academy](https://areteacademy.com.br/).

## Das Problem

Ein üblicher Datenstack kombiniert ein Transformationswerkzeug, einen
Orchestrator, eine Queue und eine Oberfläche. Jeder Baustein ist gut in dem, was
er tut – und die Integration zwischen ihnen wird zur Dauerbaustelle: vier
Versionierungsschemata, vier Arten der Konfiguration, vier Stellen, an denen man
nachsehen muss, wenn etwas fehlschlägt.

Diese Kosten tauchen in keinem Architekturdiagramm auf. Sie zeigen sich in der
Zeit, die ein Team damit verbringt, den Klebstoff zu pflegen, statt sich der
Fachlichkeit zu widmen, die nur es selbst kennt.

## Der Vorschlag

Ein Artefakt, mit dem Subcommand, den die jeweilige Rolle braucht:

```bash
brevis serve        # API + interface
brevis scheduler    # materialises schedules and drains the queue
brevis run wf.yaml  # runs now, no database
```

Ein Workflow ist eine versionierte YAML-Datei. Eine Pipeline zu prüfen heißt,
einen Diff zu prüfen, keine Klickstrecke.

```yaml
name: ingest
schedule: "0 5 * * *"

steps:
  - id: bronze
    image: registry.example/dbt:1.10.3
    run: dbt build --select bronze+

  - id: notify
    image: ghcr.io/example/notify:0.3
    run: /notify --channel data
    depends_on: [bronze]
```

## Was es anders macht

**Jeder Step läuft in einem eigenen Pod, mit einem eigenen Image.** Es gibt
keinen generischen Worker, der auf Arbeit wartet: Die Arbeit bringt ihre eigene
Laufzeitumgebung mit. Ein `dbt`-Step startet mit dem dbt-Image; ein Go-Fetcher
daneben startet mit 5,8 MB und 32Mi Speicher, statt die Größe seines größten
Nachbarn zu erben.

**Der Scheduler erzeugt Runs; die Queue führt sie aus.** Zwei unabhängige
Schleifen, und diese Trennung ist gewollt: Die eine kann ausfallen, ohne die
andere zu unterbrechen, und das Neuverarbeiten eines vergangenen Monats hängt
nicht von der Uhr ab.

**Die Validierung braucht keine Datenbank.** `brevis validate` läuft in der CI
neben den Tests, sodass ein YAML-Fehler im Pull Request scheitert und nicht im
Cluster.

## Drei Bausteine, bewusst getrennt

| Baustein | Was er ist | Version |
|---|---|---|
| **engine** | `brevis`, das Binary, um das es auf dieser Seite geht: orchestriert, plant, führt aus und betreibt | `0.15` |
| **SDK** | ein Go-Modul, um den Step zu schreiben, der extrahiert und lädt | `0.66` |
| **gateway** | ein HTTP-Endpoint, der Daten landet, die *eintreffen*, statt sie abzuholen | `0.8` |

Die Versionen sind unabhängig, weil es die Reifegrade sind. Das Gateway mit
`0.15.3` zu nummerieren, nur weil die Engine dort steht, würde eine Reife
behaupten, die es nicht hat – und eine Version lässt sich nicht
zurückziehen.

Was die drei zusammenhält, ist eine Zahl: die `ingestion_id`, eine
unveränderliche UUID v5 über `provider|entity|source_key|record_ts`. Eine Zeile,
die das [Gateway](/de/docs/ingestion/index.md) landet, und eine Zeile, die ein
[SDK](/de/docs/sdk/index.md)-Fetcher für denselben Datensatz landet, sind *eine Zeile*,
ohne Abgleich dazwischen.

## Wann brevis.sh nicht die richtige Wahl ist

Zu dokumentieren, was ein Werkzeug nicht kann, spart mehr Zeit, als zu
dokumentieren, was es kann:

- **Sie betreiben bereits Airflow oder Dagster und sind zufrieden.** Die
  Migration kostet mehr, als es spart, ein Binary weniger zu betreiben.
- **Ihre Workflows drehen sich nicht um Daten.** brevis.sh geht von DAGs für
  Transformation und Laden aus; ein Allzweck-Orchestrator dient Ihnen besser.
- **Sie brauchen einen Katalog fertiger Operatoren.** Hier ist ein Step ein
  Image und ein Befehl. Das ist einfach und begrenzt – es gibt keine hunderte
  Integrationen zum Einbinden.
- **Ihr Team nutzt Kubernetes nicht und will es auch nicht.** Der lokale Modus
  funktioniert, aber das Design setzt Pods voraus.

## Offener Code. Wählen Sie, wie Sie mitmachen.

brevis.sh ist zu 100 % Open Source. Keine proprietären Lizenzen, keine
eingeschränkte Edition, keine Sperren für das, was Sie bauen.

### 100 % offen

*Code · Open-Source-Lizenz*

Lesen, nutzen, anpassen und verbessern Sie die Laufzeitumgebung in Ihrem eigenen Betrieb. Vollständige Laufzeitumgebung · Prüfbarer Code · Offene Beiträge.

[Repository ansehen](https://github.com/AreteAcademy/brevis)

### Gemeinsam bauen

*Community · offene Issues* · **Hauptweg**

Beteiligen Sie sich an den Gesprächen, die entscheiden, was aus dem Projekt wird. Issues und Vorschläge · Austausch unter Gleichgesinnten · Öffentliche Roadmap.

[Das Projekt verfolgen](https://github.com/AreteAcademy/brevis/issues)

### Tiefgehend lernen

*aretê · Technologie mit Sinn*

Lernen Sie die Schule kennen, die philosophisches Denken und Softwareentwicklung verbindet. Originalinhalte · Kontinuierliches Lernen · Offener Kanal.

[Aretê besuchen](https://areteacademy.com.br/)

## Wie es weitergeht

| Wenn Sie ... möchten | gehen Sie zu |
|---|---|
| installieren und sehen, wie es läuft | [Installation](/de/docs/installation/index.md) und [Schnellstart](/de/docs/quickstart/index.md) |
| das Workflow-Format verstehen | [Workflows](/de/docs/workflows/index.md) |
| die Architektur verstehen | [Scheduler und Queue](/de/docs/scheduler-and-queue/index.md) |
| die Liste der Befehle und Flags | [CLI](/de/docs/cli/index.md) |
| einen Fetcher in Go schreiben | [SDK](/de/docs/sdk/index.md) |
| einen Wert von einem Step an den nächsten weitergeben | [Kontext zwischen Steps](/de/docs/context/index.md) |
| einen Step in Python schreiben | [Client-Bibliotheken](/de/docs/libraries/index.md) |
| Daten über HTTP empfangen, statt sie abzuholen | [Ingestion](/de/docs/ingestion/index.md) |
| Dashboards und Alarme bauen | [Observability](/de/docs/observability/index.md) |

:::note Zum Namen
*Brevis* ist lateinisch für „kurz, knapp“ – die Wurzel von *Brevität*. Er stammt
aus Senecas **De Brevitate Vitae**: Nicht dass wir wenig Zeit hätten, sondern
dass wir viel davon verlieren.
:::
