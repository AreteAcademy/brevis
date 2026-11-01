# Kubernetes

> Cómo desplegar el engine, con qué permisos y qué comprobar después.

*https://brevis.sh/es/docs/kubernetes/ · brevis.sh docs (es)*

---

Una instalación típica tiene **dos Deployments** del mismo binario —la API y
el scheduler— más un Postgres.

## Instalarlo con Helm

El chart está en el repositorio, en `deployments/helm/brevis`. Cuatro valores son
obligatorios y todo lo demás tiene un valor por defecto que funciona:

```bash
helm install brevis ./deployments/helm/brevis \
  --namespace data --create-namespace \
  --set database.url="postgres://brevis:pw@postgres/brevis?sslmode=require" \
  --set auth.user=admin \
  --set auth.passwordHash="$(brevis hash)" \
  --set auth.secret="$(openssl rand -base64 48)"
```

Esa es toda la instalación: las migraciones corren primero como hook, ambos Deployments
arrancan con la imagen correcta cada uno, y el scheduler recibe una ServiceAccount que
puede crear pods mientras que los pods de tareas reciben una que no puede hacer nada.

Para llegar a la interfaz:

```bash
helm upgrade brevis ./deployments/helm/brevis --reuse-values \
  --set api.ingress.enabled=true \
  --set api.ingress.host=brevis.example.com \
  --set api.ingress.tls.enabled=true \
  --set api.ingress.tls.secretName=brevis-tls
```

En producción, hay que mantener la cadena de conexión fuera del release: ponerla en un
Secret y pasar `--set database.existingSecret=brevis-db`. Pasada en línea,
queda en el manifiesto almacenado del release, que puede leer cualquiera que pueda leer Secrets en
ese namespace.

`helm show values ./deployments/helm/brevis` lista cada valor con la razón
al lado.

### Lo que el chart rechaza

Todo lo que no puede hacer seguro falla al renderizar, con un mensaje que dice qué
se rompería, no a las 3 a. m., en un run que produjo filas duplicadas:

| rechazado | porque |
|---|---|
| `scheduler.replicas` en absoluto | dos materializarían los mismos slots. `FOR UPDATE SKIP LOCKED` les impide reclamar el mismo elemento de la cola; nada les impide crear el mismo run programado, y un `dbt build` se ejecutaría dos veces sobre una ventana sin que nada falle |
| una credencial ausente fuera de `local` | el engine se niega a arrancar sin una, así que el chart te entregaría un CrashLoopBackOff |
| `auth.secret` de menos de 32 bytes | firma la cookie de sesión, y el engine impone el mismo mínimo |
| un Ingress sin `host` | coincidiría con todas las peticiones que llegan al controlador, respondiendo en un clúster compartido por el hostname de otra persona |
| `alerts.enabled` sin webhook | el pod vaciaría el outbox y no entregaría en ningún lado, lo cual es peor que no ejecutarse: el fallo parecería entonces anunciado |
| un valor que el chart no define | un error tipográfico en `--set` de otro modo no hace nada, en silencio |

`.github/scripts/helm-check.sh` verifica todo eso sobre la salida renderizada, además de
los invariantes de arriba: un scheduler, `pods/log` presente, migraciones primero, el
puerto de métricas fuera del Service.

## Sin Helm

Siete manifiestos están en `deployments/kubernetes/`, listos para `kubectl apply -f`
después de editar el namespace y el tag de la imagen:

```bash
kubectl apply -f deployments/kubernetes/rbac.yaml
kubectl apply -f deployments/kubernetes/api.yaml
kubectl apply -f deployments/kubernetes/scheduler.yaml
```

Llevan las mismas decisiones que el chart, con el razonamiento en comentarios, y
además `alert.yaml`, `report.yaml` y `publish-job.yaml`. Las secciones siguientes
recorren lo que contiene cada uno.

## Los dos roles

| rol | comando | imagen | réplicas |
|---|---|---|---|
| API + interfaz | `serve` | `:0.11.2` (distroless) | las que se quiera |
| scheduler + cola | `scheduler` | `:0.11.2-worker` (alpine) | **una** |

La API es distroless porque solo sirve HTTP: no ejecuta nada, así que
no necesita shell. El worker es alpine porque los steps `run:` necesitan una.

:::warning Una sola réplica del scheduler
Dos procesos que materializan la misma programación crean runs duplicados. Si se necesita
disponibilidad, usar `replicas: 1` con `strategy: Recreate`.
:::

## Migraciones

Ejecutarlas como un Job antes del despliegue, nunca al arrancar `serve`:

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
          image: areteacademy/brevis:0.40.0
          args: ["migrate", "up"]
          envFrom:
            - secretRef: {name: brevis-db}
```

## Deployment de la API

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
          image: areteacademy/brevis:0.40.0
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

:::note Por qué difieren las dos sondas
`/health` **no** consulta la base de datos; `/ready` sí, y nombra la
dependencia que falló. Si el liveness dependiera de Postgres, un titubeo de la base de datos
haría que Kubernetes **matara** los pods de la API en lugar de simplemente sacarlos del
balanceador de carga, y la recuperación sería más lenta justo cuando el sistema ya
está bajo estrés.
:::

## Deployment del scheduler

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
          image: areteacademy/brevis:0.40.0-worker
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

`BREVIS_PODS=on` **requiere** un clúster: sin service account montada, el arranque
falla en lugar de volver en silencio a la ejecución local.

## Permisos

El scheduler crea, observa y elimina pods en el namespace de ejecución.
Nada más allá de eso:

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

`pods/log` es lo que permite seguir la salida de un step hasta la interfaz. Sin
él, el run funciona y el log queda vacío.

## Después del despliegue

```bash
kubectl -n data get pods -l app=brevis-api
kubectl -n data exec deploy/brevis-api -- brevis version
curl -fsS https://brevis.example.com/ready
```

Dispara un workflow simple desde la interfaz y confirma que el pod del step
aparece y desaparece:

```bash
kubectl -n data get pods -w
```

## Cuando un step se queda en Pending

Casi siempre es una de estas tres:

| causa | cómo confirmarlo |
|---|---|
| taint sin toleration | `kubectl describe pod` muestra `node(s) had untolerated taint` |
| `nodeSelector` sin nodo | `describe` muestra `didn't match Pod's node affinity` |
| recurso no disponible | `describe` muestra `Insufficient cpu` o `memory` |

Para inspeccionar el pod de un step fallido en lugar de verlo desaparecer:

```bash
BREVIS_POD_KEEP_ON_FAILURE=true
```

Déjese apagado en producción: los pods detenidos consumen cuota.

## Siguientes pasos

- [Pod por step](/es/docs/pod-per-step/index.md) — el modelo de ejecución
- [Configuración](/es/docs/configuration/index.md) — cada variable `BREVIS_POD_*`
- [Observabilidad](/es/docs/observability/index.md) — qué scrapear, y de qué proceso
