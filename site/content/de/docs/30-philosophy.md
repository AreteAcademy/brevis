---
title: Philosophie
description: Woher brevis.sh kommt: Seneca, Aretê Academy und was davon im Code entschieden wird.
group: Das Projekt
order: 30
slug: philosophy
serif: true
---

brevis.sh entsteht innerhalb der Aretê Academy: Disziplin, Zweck und Exzellenz, angewandt auf das Bauen von Software.

## De brevitate vitae

> Nicht dass wir wenig Zeit hätten, sondern dass wir viel davon verlieren.

Senecas Maxime leitet die Frage hinter brevis.sh: Welche Komplexitäten verdienen wirklich die Zeit eines Teams? Jeder Workflow läuft in isolierten Pods, damit Fehler, Versionen und Abhängigkeiten klare Grenzen haben.

Vier Entscheidungen der Laufzeitumgebung folgen aus dieser Frage:

- **Image pro Step** – Jeder Step deklariert die Laufzeitumgebung, die er braucht. Eine Version anzuheben zwingt Sie nicht, die anderen anzufassen.
- **Logs pro Run** – Die Ausgabe gehört zu dem Run, der sie erzeugt hat. Einen Fehler zu untersuchen heißt nicht, einen verlorenen Kontext zu rekonstruieren.
- **persistente Retries** – Der Versuch überdauert den Prozess. Ein Neustart löscht nicht, was über die Arbeit bereits bekannt war.
- **Isolation der Abhängigkeiten** – Was ein Step mitbringt, sickert nicht in seine Nachbarn. Eine klare Grenze macht einen Fehler lesbar.

## Was das Projekt vorschlägt

Zwischen *Philosophie ohne Praxis* und *Technologie ohne Richtung*: **Klarheit, das Wesentliche zu tun.**

## Werkzeuge prägen die Art, wie wir denken.

> „Nachhaltiger Code ist nicht bloß Code, der weiterläuft. Es ist Code, der Menschen Raum zum Nachdenken lässt.“ – Philosophie, angewandt auf Software

> „Klarheit ist nicht das Gegenteil von Raffinesse. Sie ist das Kriterium, das verhindert, dass Raffinesse zu Rauschen wird.“ – Open Source aus Prinzip
