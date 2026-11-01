# Observability

> Prometheus-Metriken aus beiden Prozessen, was zu beobachten ist und was bewusst fehlt.

*https://brevis.sh/de/docs/observability/ · brevis.sh docs (de)*

---

Brevis stellt Prometheus-Metriken auf einem eigenen Port bereit, aus **beiden**
Prozessen.

```
GET http://<pod>:9090/metrics
```

Der Port stammt aus `BREVIS_METRICS_ADDR`. Er ist mit Absicht vom HTTP-Port
getrennt: Metriken laufen nicht durch die Authentifizierung der Oberfläche, und
beides von einer Stelle auszuliefern würde zu einer Wahl zwischen einem
authentifizierten Scrape und einem offenen Dashboard zwingen.

## Zwei Prozesse, zwei Endpoints

Das ist der Teil, den die meisten Setups falsch machen, indem sie nur die API
abfragen.

| | liefert | meldet |
|---|---|---|
| `brevis serve` | die UI und die HTTP-API | Queue-Tiefe |
| `brevis scheduler` | nichts über HTTP | Queue-Tiefe, Claim-Latenz, Slots, Orphans, Runs, Steps |

Der Scheduler ist es, der ausführt, daher kommt fast alles von ihm. Ein Dashboard,
das nur auf der API aufbaut, zeigt die Queue wachsen und **nichts** darüber, was
sie leert.

## Die Metriken, die ein Step veröffentlicht

Über das hinaus, was die Engine misst, kann ein Step eigene veröffentlichen – wie
viele Zeilen gelandet sind, wie viele der Anbieter abgelehnt hat:

```python
from brevis import metrics
metrics.set("rows_loaded", 48213)
```

Sie erscheinen auf demselben `/metrics` des Schedulers, mit Präfix und Labels der
Engine:

```
brevis_step_rows_loaded{workflow="daily_sales",step="load"} 48213
```

Der Step öffnet keinen Port: Er schreibt eine Zeile nach stdout, und die Engine
hält sie fest – ein Pod, der vierzig Sekunden lebt, ist nicht abfragbar. Siehe
[Python](/de/docs/python/index.md).

## Was zu beobachten ist

| Signal | Warum |
|---|---|
| Queue-Tiefe steigt, ohne zu fallen | der Scheduler ist ausgefallen, oder `--concurrency` ist für die Last zu niedrig |
| Claim-Latenz wächst | Konkurrenz auf der Datenbank, oder eine für das Intervall zu große Queue |
| Orphans > 0 | Runs, die von einem Prozess beansprucht wurden, der vor dem Abschluss starb |
| Slots nicht materialisiert | die Scheduler-Schleife ist stehen geblieben, und die Zeitpläne mit ihr |

Die ersten beiden allein unterscheiden „hohe Last“ nicht von „toter Prozess“ –
deshalb zählen die Slots. Eine steigende Queue **mit** neu angelegten Slots ist
Last; eine steigende Queue **ohne** Slots ist eine stehengebliebene Schleife.

## Kubernetes

```yaml
env:
  - name: BREVIS_METRICS_ADDR
    value: ":9090"
ports:
  - name: metrics
    containerPort: 9090
```

Mit dem Prometheus Operator ein `PodMonitor` pro Rolle – und es muss tatsächlich
einer pro Rolle sein, weil die beiden Deployments unterschiedliche Labels tragen
und unterschiedliche Mengen melden.

## Was bewusst fehlt

**Traces.** Es gibt noch kein OpenTelemetry. Ein Step läuft als eigener Pod, und
was an ihm zählt – Start, Ende, Versuch, Exit-Code, Log – steht bereits in der
Datenbank und auf dem Bildschirm. Der nützliche Trace wäre der innerhalb des
Steps, und der gehört in den Code des Steps, nicht in den Orchestrator.

Das gilt, solange der Step die Einheit ist. An dem Tag, an dem ein Step einen
anderen Dienst aufruft und die Frage lautet „Wo sind die 40 Sekunden geblieben?“,
beginnt der Trace sich zu lohnen.

## Nächste Schritte

- [Kubernetes](/de/docs/kubernetes/index.md) – beide Prozesse deployen
- [Konfiguration](/de/docs/configuration/index.md) – `BREVIS_METRICS_ADDR` und der Rest
