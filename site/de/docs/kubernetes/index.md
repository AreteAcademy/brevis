# Kubernetes

> Wie Sie die Engine deployen, mit welchen Berechtigungen und was Sie danach prüfen.

*https://brevis.sh/de/docs/kubernetes/ · brevis.sh docs (de)*

---

Eine typische Installation hat **zwei Deployments** desselben Binarys – die API
und den Scheduler – plus eine Postgres.

## Mit Helm installieren

Das Chart liegt im Repository, unter `deployments/helm/brevis`. Vier Werte sind
erforderlich, alles andere hat einen funktionierenden Standard:

```bash
helm install brevis ./deployments/helm/brevis \
  --namespace data --create-namespace \
  --set database.url="postgres://brevis:pw@postgres/brevis?sslmode=require" \
  --set auth.user=admin \
  --set auth.passwordHash="$(brevis hash)" \
  --set auth.secret="$(openssl rand -base64 48)"
```

Das ist die ganze Installation: Die Migrationen laufen zuerst als Hook, beide
Deployments starten mit dem jeweils richtigen Image, und der Scheduler erhält
einen ServiceAccount, der Pods anlegen darf, während die Task-Pods einen
erhalten, der nichts darf.

Um die Oberfläche zu erreichen:

```bash
helm upgrade brevis ./deployments/helm/brevis --reuse-values \
  --set api.ingress.enabled=true \
  --set api.ingress.host=brevis.example.com \
  --set api.ingress.tls.enabled=true \
  --set api.ingress.tls.secretName=brevis-tls
```

Halten Sie in Produktion den Connection-String aus dem Release heraus: Legen Sie
ihn in ein Secret und übergeben Sie `--set database.existingSecret=brevis-db`.
Inline übergeben, landet er im gespeicherten Manifest des Releases, das jeder
lesen kann, der in diesem Namespace Secrets lesen darf.

`helm show values ./deployments/helm/brevis` listet jeden Wert mit der Begründung
daneben auf.

### Was das Chart verweigert

Alles, was es nicht sicher machen kann, schlägt zur Render-Zeit fehl, mit einer
Meldung, die sagt, was kaputtginge – nicht um 3 Uhr nachts, in einem Run, der
doppelte Zeilen erzeugt hat:

| Verweigert | Weil |
|---|---|
| `scheduler.replicas` überhaupt | zwei würden dieselben Slots materialisieren. `FOR UPDATE SKIP LOCKED` hindert sie daran, dasselbe Queue-Element zu beanspruchen; nichts hindert sie daran, denselben geplanten Run anzulegen, und ein `dbt build` würde zweimal über ein Fenster laufen, ohne dass etwas fehlschlägt |
| fehlende Zugangsdaten außerhalb von `local` | die Engine verweigert den Start ohne sie, das Chart würde Ihnen also einen CrashLoopBackOff liefern |
| `auth.secret` unter 32 Byte | es signiert das Sitzungs-Cookie, und die Engine erzwingt dasselbe Minimum |
| ein Ingress ohne `host` | er würde jede Anfrage treffen, die den Controller erreicht – in einem geteilten Cluster würde er für den Hostnamen eines anderen antworten |
| `alerts.enabled` ohne Webhook | der Pod würde die Outbox leeren und nirgends zustellen, was schlimmer ist, als nicht zu laufen: Der Fehler sähe dann gemeldet aus |
| ein Wert, den das Chart nicht definiert | ein Tippfehler in `--set` bewirkt sonst still nichts |

`.github/scripts/helm-check.sh` prüft all das an der gerenderten Ausgabe, plus
die obigen Invarianten – ein Scheduler, `pods/log` vorhanden, Migrationen
zuerst, der Metrik-Port nicht am Service.

## Ohne Helm

Sieben Manifeste liegen in `deployments/kubernetes/`, bereit für
`kubectl apply -f`, nachdem Sie den Namespace und den Image-Tag bearbeitet haben:

```bash
kubectl apply -f deployments/kubernetes/rbac.yaml
kubectl apply -f deployments/kubernetes/api.yaml
kubectl apply -f deployments/kubernetes/scheduler.yaml
```

Sie tragen dieselben Entscheidungen wie das Chart, mit der Begründung in
Kommentaren, und außerdem `alert.yaml`, `report.yaml` und `publish-job.yaml`. Die
folgenden Abschnitte gehen durch, was jedes enthält.

## Die zwei Rollen

| Rolle | Befehl | Image | Replicas |
|---|---|---|---|
| API + Oberfläche | `serve` | `:0.11.2` (distroless) | so viele, wie Sie möchten |
| Scheduler + Queue | `scheduler` | `:0.11.2-worker` (alpine) | **eine** |

Die API ist distroless, weil sie nur HTTP bedient: Sie führt nichts aus, braucht
also keine Shell. Der Worker ist alpine, weil `run:`-Steps eine brauchen.

:::warning Eine Scheduler-Replica
Zwei Prozesse, die denselben Zeitplan materialisieren, erzeugen doppelte Runs.
Brauchen Sie Verfügbarkeit, nutzen Sie `replicas: 1` mit `strategy: Recreate`.
:::

## Migrationen

Führen Sie sie vor dem Deployment als Job aus, nie beim Start von `serve`:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: brevis-migrate
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: migrate
          image: brevissh/brevis:0.40.0
          args: ["migrate", "up"]
          envFrom:
            - secretRef: {name: brevis-db}
```

## API-Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: brevis-api
spec:
  replicas: 2
  selector:
    matchLabels: {app: brevis-api}
  template:
    metadata:
      labels: {app: brevis-api}
    spec:
      serviceAccountName: brevis
      containers:
        - name: api
          image: brevissh/brevis:0.40.0
          args: ["serve"]
          ports: [{containerPort: 8080}]
          envFrom:
            - secretRef: {name: brevis-db}
            - secretRef: {name: brevis-auth}
          env:
            - name: BREVIS_ENV
              value: production
          livenessProbe:
            httpGet: {path: /health, port: 8080}
          readinessProbe:
            httpGet: {path: /ready, port: 8080}
          resources:
            requests: {cpu: 100m, memory: 128Mi}
            limits: {memory: 256Mi}
```

:::note Warum sich die beiden Probes unterscheiden
`/health` fragt die Datenbank **nicht** ab; `/ready` tut es und nennt die
Abhängigkeit, die ausgefallen ist. Hinge die Liveness von Postgres ab, ließe ein
Wackler der Datenbank Kubernetes die API-Pods **killen**, statt sie nur aus dem
Load Balancer zu nehmen – und die Erholung würde genau dann langsamer, wenn das
System ohnehin unter Stress steht.
:::

## Scheduler-Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: brevis-scheduler
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector:
    matchLabels: {app: brevis-scheduler}
  template:
    metadata:
      labels: {app: brevis-scheduler}
    spec:
      serviceAccountName: brevis
      containers:
        - name: scheduler
          image: brevissh/brevis:0.40.0-worker
          args: ["scheduler", "--interval", "10s", "--concurrency", "5", "--max-pods", "10"]
          envFrom:
            - secretRef: {name: brevis-db}
            - secretRef: {name: brevis-auth}
          env:
            - name: BREVIS_ENV
              value: production
            - name: BREVIS_PODS
              value: "on"
            - name: BREVIS_POD_NAMESPACE
              value: data
            - name: BREVIS_POD_SERVICE_ACCOUNT
              value: brevis-runner
            - name: BREVIS_POD_ENV_FROM_SECRETS
              value: bigquery-cred
```

`BREVIS_PODS=on` **verlangt** einen Cluster: Ist kein Service Account
eingehängt, schlägt der Start fehl, statt still auf lokale Ausführung
zurückzufallen.

## Berechtigungen

Der Scheduler legt Pods im Ausführungs-Namespace an, beobachtet und löscht sie.
Nichts darüber hinaus:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: brevis-runner
  namespace: data
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["create", "get", "list", "watch", "delete"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
```

`pods/log` erlaubt es, die Ausgabe eines Steps bis in die Oberfläche zu
verfolgen. Ohne sie funktioniert der Run, und das Log bleibt leer.

## Nach dem Deployment

```bash
kubectl -n data get pods -l app=brevis-api
kubectl -n data exec deploy/brevis-api -- brevis version
curl -fsS https://brevis.example.com/ready
```

Lösen Sie in der Oberfläche einen einfachen Workflow aus und bestätigen Sie, dass
der Pod des Steps erscheint und wieder verschwindet:

```bash
kubectl -n data get pods -w
```

## Wenn ein Step in Pending bleibt

Es ist fast immer eines dieser drei:

| Ursache | So bestätigen Sie sie |
|---|---|
| Taint ohne Toleration | `kubectl describe pod` zeigt `node(s) had untolerated taint` |
| `nodeSelector` ohne Node | `describe` zeigt `didn't match Pod's node affinity` |
| Ressource nicht verfügbar | `describe` zeigt `Insufficient cpu` oder `memory` |

Um den Pod eines fehlgeschlagenen Steps zu untersuchen, statt ihn verschwinden zu
sehen:

```bash
BREVIS_POD_KEEP_ON_FAILURE=true
```

Lassen Sie es in Produktion aus – angehaltene Pods verbrauchen Quote.

## Nächste Schritte

- [Pod pro Step](/de/docs/pod-per-step/index.md) – das Ausführungsmodell
- [Konfiguration](/de/docs/configuration/index.md) – jede `BREVIS_POD_*`-Variable
- [Observability](/de/docs/observability/index.md) – was abzufragen ist, und aus welchem Prozess
