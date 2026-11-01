# Console

> Die Weboberfläche des Core: was läuft, was laufen wird und was mit jedem Run geschehen ist.

*https://brevis.sh/de/docs/console/ · brevis.sh docs (de)*

---

Die Oberfläche wird im Binary mitgeliefert – kein separater Dienst, den man installieren und am Laufen halten müsste. Erfolgsquote, durchschnittliche Dauer, die Queue, die Versuchshistorie und der Zeitplan dessen, was als Nächstes kommt.

Die Console startet mit `brevis serve`, auf demselben Port wie die HTTP-API
(`BREVIS_HTTP_ADDR`, standardmäßig `:8080`). Im [Schnellstart](/de/docs/quickstart/index.md) öffnet sie sich unter `localhost:8080`.

Die folgenden Bildschirme sind in SVG aus der Console nachgebaut, mit Beispieldaten –
nie Screenshots: Die echte Console verarbeitet in Produktion Kundendaten.

## Übersicht

![Die Brevis-Console auf dem Übersichtsbildschirm: eine Erfolgsquote von 94,2 Prozent über 344 Runs, ein Balkendiagramm der Runs pro Stunde mit durchschnittlicher Dauer, ein Verteilungsring, was gerade läuft, und die nächsten geplanten Runs.](/assets/console.svg)

Eine Übersicht mit Metriken und Diagrammen. Erfolgsquote, durchschnittliche Dauer, was gerade läuft und die nächsten geplanten Runs, auf einem Bildschirm.

## Workflows

![Die Workflow-Liste: Suche, Filter nach Zustand und nach Tag und zwölf Workflows mit einem Pausenschalter, dem Cron-Ausdruck, dem nächsten und dem letzten Run, Tags und einem Button, um jeden sofort auszuführen.](/assets/console-workflows.svg)

Eine Workflow-Liste mit Suche, Filtern, Pause und Ausführen. Jede Zeile trägt den Cron, den nächsten und den letzten Run sowie die Tags.

## Ein Workflow

![Die Seite eines Workflows: seine vier Steps, drei Abhängigkeiten, Cron und Tags; ein Formular, um ihn mit Parametern auszuführen; eine Erfolgsquote von 96,1 Prozent über 176 Runs; der DAG mit dem Zustand jedes Steps; und ein Kalender eines Jahres, in dem jedes Quadrat ein Tag ist, gefärbt nach dem schlechtesten Ergebnis dieses Tages.](/assets/console-workflow.svg)

Die Steps und Abhängigkeiten, ein Formular zum Ausführen mit Parametern, der DAG mit dem Zustand jedes Steps und ein Jahreskalender, in dem jeder Tag die Farbe seines schlechtesten Ergebnisses annimmt.

## Runs

![Die Run-Historie: 1.284 Runs mit ihrem Zustand, Workflow, Auslöser, dem Slot, zu dem sie gehören, wie lange jeder dauerte und wann er erstellt wurde. Einige zeigen, welcher Versuch erfolgreich war.](/assets/console-runs.svg)

Run-Historie, mit dem Versuch jedes Runs. Zustand, Workflow, Auslöser, der Slot, zu dem der Run gehört, Dauer und Erstellungszeitpunkt.

## Ein Run

![Ein abgeschlossener Run: sein Zustand, Auslöser und Slot; der Graph mit Befehl, Image und Dauer jedes Steps; die neun Auto-Parameter, die die Engine für jeden Run ermittelt – die Uhr, die statt now() zu lesen ist, das Partitionsdatum, wie spät der Versuch startete und das Fenster, das er abdeckt; und die Ausgabe jedes Steps.](/assets/console-run.svg)

Der DAG eines Runs live und die Auto-Parameter, die die Engine dafür ermittelt hat. Der Graph mit Befehl, Image und Dauer jedes Steps sowie die Ausgabe jedes Steps.

## Projekte

![Zwei Projekte, analytics und platform, jedes mit einem eigenen Namensraum für seine Workflows – beide können ein daily_ingest haben, ohne zu kollidieren.](/assets/console-projects.svg)

Projekte, damit zwei Teams nicht über einen Namen kollidieren. Jedes Projekt gibt seinen Workflows einen eigenen Namensraum: Zwei können je ein `daily_ingest` haben.

## Sie wird im Binary mitgeliefert

Schriften und Bundles werden aus dem Binary ausgeliefert – funktioniert ohne Internet.
