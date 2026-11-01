# Kontext zwischen Steps

> Was ein Step dem nächsten mitteilt – zwei Umgebungsvariablen und eine Datei.

*https://brevis.sh/de/docs/context/ · brevis.sh docs (de)*

---

Ein Step veröffentlicht Werte; die Steps, die von ihm abhängen, lesen sie. Mehr
tut das Feature nicht.

```yaml
steps:
  - id: extract
    run: python fetch.py

  - id: transform
    run: python transform.py
    depends_on: [extract]
```

```python
# extract
from brevis import context
context.set(bucket="s3://landing/2026-09-07", rows=48213)
```

```python
# transform
from brevis import context
bucket = context.get("extract.bucket")
```

Der Schlüssel lautet immer `<step id>.<name>`. Ein Step liest, was **seine
eigenen Abhängigkeiten** veröffentlicht haben, und sonst nichts.

:::note Was das nicht ist
Keine Message Queue, kein Zustand zwischen Runs und kein Ort, um Daten
abzulegen. Es ist die Notiz, die ein Step für den nächsten hinterlässt – ein
Pfad, ein Watermark, eine Anzahl.
:::

## Jede Sprache, keine Bibliothek nötig

Der Vertrag besteht aus **zwei Umgebungsvariablen und einer Datei**, sodass auch
ein Step ganz ohne SDK mitmachen kann:

```bash
window=$(jq -r '.extract.watermark' <<< "$BREVIS_INPUT")
./publish --until "$window"
jq -n --arg n "$(date -Is)" '{published_at: $n}' > "$BREVIS_OUTPUT"
```

| | |
|---|---|
| `BREVIS_INPUT` | `{"<step id>": {…}}` – was die Abhängigkeiten dieses Steps veröffentlicht haben |
| `BREVIS_OUTPUT` | ein Pfad, in den **ein** JSON-Objekt geschrieben wird |

Unter Kubernetes ist dieser Pfad `/dev/termination-log`, den die Engine ohnehin
liest, um den Exit-Code zu erhalten. Es gibt also **keinen API-Aufruf, kein
Token und keinen Port** – und eine Bibliothek dafür ist ein JSON-Parse und ein
Schreibvorgang in eine Datei.

| Sprache | |
|---|---|
| Python | [`pip install brevis`](/de/docs/python/index.md) – enthält auch `brevis.run`, mit den [automatischen Parametern](/de/docs/parameters/index.md) |
| Go | `sdk/context`, Teil des SDK-Moduls, hängt von nichts ab |
| alles andere | die beiden Variablen oben |

## Isolation

Kein Step kann überschreiben, was ein anderer veröffentlicht hat. Schlüssel
tragen als Präfix die Id dessen, der sie geschrieben hat, und die Engine – nicht
der Step – bestimmt dieses Präfix. Zwei Steps, die `bucket` veröffentlichen,
erzeugen `extract.bucket` und `load.bucket`, und keiner verschwindet.

## Die Obergrenze von 4096 Byte

Was ein Step veröffentlicht, passt in **4096 Byte**. Das Limit ist nicht
willkürlich: Unter Kubernetes ist der Transport `/dev/termination-log`, und das
ist es, was das kubelet akzeptiert.

Eine Überschreitung ist ein **Step-Fehler**, wobei die Meldung angibt, wie viele
Byte geschrieben wurden. Die Alternative wäre Abschneiden – und abgeschnittenes
JSON ist ungültiges JSON, das der nächste Step entdeckt, der nicht wissen kann,
dass der Fehler nicht bei ihm liegt.

Passt ein Wert nicht hinein, ist er kein Kontext: Schreiben Sie ihn in einen
Bucket und veröffentlichen Sie den Pfad.

## Retries und fortgesetzte Runs

Bei einem neuen Versuch veröffentlicht der Step erneut, und der vorherige Wert
wird ersetzt. Ein Step, der bereits erfolgreich war, läuft weder erneut noch
veröffentlicht er erneut – was er gesagt hat, gilt weiter für die, die von ihm
abhängen.

## Einen Step von Hand ausführen

Die beiden Variablen sind der gesamte Vertrag, einen Run nachzustellen heißt also,
sie zu exportieren:

```bash
export BREVIS_INPUT='{"extract":{"bucket":"s3://landing/2026-09-07"}}'
export BREVIS_OUTPUT=/tmp/out.json
python transform.py
cat /tmp/out.json
```

## Nächste Schritte

- [Parameter](/de/docs/parameters/index.md) – was sich zwischen Runs ändert, und die automatischen
- [Step-Laufzeitumgebung](/de/docs/runtime/index.md) – was ein Step darüber deklariert, wo er läuft
- [Python](/de/docs/python/index.md) – die Client-Bibliothek und die anderen Sprachen
