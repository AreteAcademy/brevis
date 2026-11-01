# FAQ

> Die Fragen, die zuerst kommen, mit kurzen Antworten und dem Verweis, wo es weitergeht.

*https://brevis.sh/de/docs/faq/ · brevis.sh docs (de)*

---

## Ist brevis.sh wirklich zu 100 % Open Source?

Ja. Die vollständige Laufzeitumgebung ist offen zum Lesen, Nutzen, Anpassen und Beitragen.

## Was bedeutet der Bezug auf Seneca für das Projekt?

De Brevitate Vitae inspiriert eine bewusstere Entwicklung: die Komplexität verringern, die Zeit verzehrt, und Raum für Arbeit mit Zweck bewahren.

Mehr unter [Philosophie](/de/docs/philosophy/index.md).

## Wie funktioniert die Pod-Isolation?

Jeder Step eines Workflows läuft als eigener Pod mit eigenem Image, sodass Abhängigkeiten und Ausführungskontext getrennt bleiben.

Mehr unter [Pod pro Step](/de/docs/pod-per-step/index.md).

## Welche Beziehung besteht zur Aretê Academy?

brevis.sh ist ein Projekt der Aretê Academy, die große Ideen der Philosophie an die realen Herausforderungen beim Bauen von Software heranführt.

## Kann ich nur einen der Bausteine verwenden?

Ja. Jeder Baustein hat ein eigenes Modul, eine eigene Version und ein eigenes Image: Das SDK ist eine Go-Bibliothek, die in jedem Programm läuft, das Gateway startet allein mit einer YAML-Datei und ohne Datenbank, und brevis-sql läuft als Container. Der Core orchestriert die anderen, wenn Sie es möchten – er ist für keinen von ihnen Voraussetzung.

Mehr unter [Ökosystem](/de/docs/ecosystem/index.md).
