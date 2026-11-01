# Pod pro Step

> Warum jeder Step in einem eigenen Pod läuft und was das bei Kosten und Isolation ändert.

*https://brevis.sh/de/docs/pod-per-step/ · brevis.sh docs (de)*

---

Auf Kubernetes wird **jeder Step eines Workflows zu einem Pod** mit dem Image,
das in diesem Step deklariert ist. Es gibt keinen generischen Worker, der auf
Arbeit wartet: Die Arbeit bringt ihre eigene Laufzeitumgebung mit.

![Diagramm: Der Core startet einen Pod pro Step – extract auf Go, prepare auf Python, load auf Go und transform auf dbt. Die Steps reichen einander Kontext weiter, und das SDK liest und schreibt ihn.](/assets/execution.svg)

```
scheduler                          cluster
─────────                          ───────
walks the graph
  for each ready step:
    builds the Pod  ───────────►  Pod: the step's image, the step's command
    follows status      ◄───────  Pending → Running → Succeeded/Failed
    follows the log     ◄───────  container stdout
    reads the exit code ◄───────  containerStatuses[0].terminated
    deletes the pod ───────────►  (gone)
```

## Was sich dadurch ändert

Ein monolithischer Worker zwingt das Image, alles zu enthalten, was irgendein
Step brauchen könnte. In der Praxis:

| | ein einziges Image | Pod pro Step |
|---|---|---|
| dbt-Step | 1,9 GB · 1Gi RAM | 1,9 GB · 1Gi RAM |
| Go-Fetcher daneben | **1,9 GB · 1Gi** | **12 MB · 32Mi** |
| die dbt-Version anheben | alles neu bauen | eine Zeile im YAML dieses Workflows |
| ein Step mit Speicherleck | reißt den Worker und seine Nachbarn mit | stirbt allein |

Die dritte Zeile ist die am wenigsten offensichtliche und im Alltag die
teuerste: Mit einem einzigen Image erzwingt der Wechsel von dbt 1.10 auf 1.11 in
einer Pipeline den Wechsel in allen.

## Images nach Rolle

Images gelten pro **Rolle**, nicht pro Projekt. Alles, was in einem Image
überflüssig ist, wird von jedem Node gezogen, der diesen Step ausführt:

| Rolle | Größe | Kaltstart |
|---|---|---|
| **Go** | **5,8 MB** | 0,18 s |
| **Python** | **118 MB** | 0,54 s |
| **dbt** | **620 MB** | 3,3 s |

Vorher: ein einziges Image mit 1,87 GB für alles.

Bei dbt läuft `dbt parse` zur Build-Zeit, und `target/partial_parse.msgpack`
reist im Image mit – **2,68 s weniger pro Pod**, bei jedem Aufruf.

## Wo jeder Step läuft

Die Entscheidung fällt einmal, beim Start, und hängt von `BREVIS_PODS` ab und
davon, ob es einen Cluster gibt:

| `BREVIS_PODS` | mit Cluster | ohne Cluster |
|---|---|---|
| `auto` (Standard) | ein Step mit `image:` wird zu einem Pod | alles als lokale Prozesse, mit einer Warnung im Log |
| `on` | ein Step mit `image:` wird zu einem Pod | **Startfehler** |
| `off` | alles als lokale Prozesse | alles als lokale Prozesse |

**Ein Step mit `host:` durchläuft nichts davon.** Er wird vor dem Pod und vor
dem Prozess geprüft und geht an eine Maschine, die bereits läuft – `BREVIS_PODS`
gilt für ihn nicht, und er fällt nie auf einen lokalen Prozess zurück. Das ist
Absicht: `image:` weicht mit einer Warnung auf lokale Ausführung aus, weil ein
Container und ein Prozess derselbe Befehl in zwei Hüllen sind, ein Host aber
nicht. Dort liegt die Lizenz, oder die GPU, oder die Daten.

`auto` ist der Standard, weil dasselbe Binary an beiden Orten läuft: Auf einem
Laptop ist kein Service Account eingehängt, und es fällt auf lokale Prozesse
zurück; in einem Cluster ist einer da, und es beginnt, Pods zu erzeugen.

`on` gibt es für das Deployment, das **nicht** still zu lokaler Ausführung werden
darf. Dort muss ein fehlender Cluster ein Startfehler sein.

:::warning `brevis run` erzeugt keine Pods
Der Befehl `run` baut nur den Prozess-Executor. Ein Step mit `image:` läuft auf
der Instanz selbst – und sagt das. Zu schweigen ließe es so aussehen, als sei der
Step auf dem deklarierten Image gelaufen, und das ist die Art Fehler, die erst
auftaucht, wenn das Ergebnis schon falsch ist.
:::

## Identität und Zugangsdaten

Mit welcher Identität Pods starten, ist eine Entscheidung der **Installation**,
nicht des Workflows – eine Pipeline darf nicht den Service Account wählen
können, unter dem sie läuft. Sie stammt deshalb aus der Umgebung:

```bash
BREVIS_POD_NAMESPACE=data
BREVIS_POD_SERVICE_ACCOUNT=brevis-runner
BREVIS_POD_PULL_SECRETS=registry-cred
BREVIS_POD_ENV_FROM_SECRETS=bigquery-cred,api-tokens
```

Im Pod-Modus stammt die Task-Umgebung aus Cluster-Secrets – nicht aus
`BREVIS_TASK_ENV`.

## Node-Selector und Tolerations

```bash
BREVIS_POD_NODE_SELECTOR='kubernetes.io/arch=arm64'
BREVIS_POD_TOLERATIONS='dedicated=data:NoSchedule'
```

In Tolerations wird nur der Operator `Equal` akzeptiert: `Exists` würde **jeden**
Taint mit diesem Schlüssel tolerieren, zu weit für eine Entscheidung, die aus
einer Umgebungsvariablen kommt.

Eine fehlerhafte Toleration wird **ignoriert**, statt zum Startfehler zu werden –
sie lässt den Pod in `Pending`, was sichtbar ist, während eine Startverweigerung
auch die Workflows stoppen würde, die diesen Pool nicht brauchen.

## Einen Fehler debuggen

```bash
BREVIS_POD_KEEP_ON_FAILURE=true
```

Behält den fehlgeschlagenen Pod für `kubectl logs` und `kubectl describe`.
Lassen Sie es in Produktion aus: Angehaltene Pods verbrauchen Cluster-Quote.

## Nächste Schritte

- [Kubernetes](/de/docs/kubernetes/index.md) – das vollständige Deployment
- [Konfiguration](/de/docs/configuration/index.md) – jede `BREVIS_POD_*`-Variable
- [Observability](/de/docs/observability/index.md) – Metriken aus beiden Prozessen
