---
title: White Label
description: Titel, Leitspruch, Logo und Palette der Oberfläche über eine einzige YAML-Datei ändern.
group: Betrieb
order: 17
slug: white-label
---

Die Oberfläche lässt sich über eine Datei anpassen. Farben überschreiben die
CSS-Variablen **zur Laufzeit**, sodass eine Änderung des Themes eines Kunden
nichts neu kompiliert.

## Die Datei

Kopieren Sie `brand.example.yaml` nach `brand.yaml` im Wurzelverzeichnis, oder
zeigen Sie mit `BREVIS_BRAND_FILE` darauf:

```yaml
titulo: Acme Data
subtitulo: Data platform

frase: |
  Measure before deciding.
  Decide before scaling.

cores:
  fundo:    "#f4efe4"
  tinta:    "#21180f"
  destaque: "#aa8450"
```

**Jedes Feld ist optional.** Was Sie weglassen, behält den Standard, sodass eine
Datei mit nur `titulo:` bereits gültig ist.

Der Zeilenumbruch in `frase` bleibt erhalten: Er bestimmt den Rhythmus des Satzes
in der Seitenleiste.

## Logo

```yaml
logo: https://example.com/mark.svg
```

Akzeptiert eine URL (`https://` oder `http://`) oder einen internen Pfad, der mit
`/` beginnt. Fehlt es, wird das eingebaute Symbol verwendet, das der Themenfarbe
folgt.

:::warning Ein anderswo gehostetes Logo hängt von diesem Host ab
Fällt er aus, oder hat der Cluster keinen ausgehenden Internetzugang, lädt das
Bild nicht. Der Rest der Oberfläche funktioniert weiter, aber wägen Sie das bei
der Wahl ab – der Rest der UI, Schriften und Bundles eingeschlossen, wird gerade
deshalb aus dem Binary selbst ausgeliefert, um nicht vom Netzwerk abzuhängen.
:::

## Validieren

Ein falscher Hex-Wert würde erst auffallen, wenn der Container hochkommt, und die
Meldung käme über das Pod-Log an – weit entfernt von dem, der die Datei bearbeitet
hat. Daher ein Subcommand:

```bash
brevis marca brand.yaml
```

```
  ok    Acme Data · Data platform
        logo      /assets/logo.svg  (built-in symbol)
        accent    (the product's own)
        Powered by Brevis
```

Rufen Sie ihn in der CI der Installation auf, und der Fehler kommt im Pull
Request zurück.

:::note Abwesenheit bedeutet Verschiedenes
In `serve` bedeutet eine fehlende Datei „die Standardidentität verwenden“ – und
die API startet. In `marca` ist eine fehlende Datei ein **Fehler**: Wer darum
bat, einen Pfad zu validieren, erwartet, gesagt zu bekommen, dass er nicht
existiert.
:::

## Verhalten beim Start

Ein inhaltlicher Fehler – eine ungültige Farbe, kaputtes YAML – **hindert die
Oberfläche nicht am Starten**. Der Prozess loggt eine Warnung und verwendet das
Standard-Theme. Die API wegen einer Farbe herunterzunehmen wäre schlimmer, als
sie mit dem Standard-Theme auszuliefern.

```
level=WARN msg="visual identity ignored" file=brand.yaml error="brand.yaml: colour accent: \"#gggggg\" is not a hex value (#rgb, #rrggbb or #rrggbbaa)"
```

## Was sich nicht anpassen lässt

Die Zeile **„Powered by Brevis“** in der Fußzeile kommt nicht aus der
Konfiguration – sie kommt aus dem Code.

## Auf Kubernetes

Hängen Sie die Datei aus einer ConfigMap ein:

```yaml
volumes:
  - name: brand
    configMap: {name: brevis-brand}
containers:
  - name: api
    volumeMounts:
      - {name: brand, mountPath: /etc/brevis}
    env:
      - name: BREVIS_BRAND_FILE
        value: /etc/brevis/brand.yaml
```
