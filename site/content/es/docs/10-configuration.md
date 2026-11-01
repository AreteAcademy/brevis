---
title: Configuración
description: Cada variable de entorno, con su valor por defecto, formato y qué ocurre cuando falta.
group: Referencia
order: 10
slug: configuration
---

Toda la configuración viene del **entorno**, leído una sola vez al arrancar. Nada
consulta el entorno después: un proceso cuya configuración cambia
a mitad de ejecución es un proceso cuyo comportamiento nadie puede reproducir.

## Obligatoria

| variable | |
|---|---|
| `BREVIS_DATABASE_URL` | Postgres. Ausente = **error de arranque** en los cinco subcomandos que usan la base de datos |

## Proceso

| variable | valor por defecto | |
|---|---|---|
| `BREVIS_ENV` | `production` | `local` registra logs en texto y deja la interfaz abierta; cualquier otro valor, incluso si no está definida, exige credenciales y registra JSON. Define `BREVIS_ENV=local` para desarrollo |
| `BREVIS_HTTP_ADDR` | `:8080` | dirección de escucha |
| `BREVIS_LOG_LEVEL` | `info` | |
| `BREVIS_SHUTDOWN_TIMEOUT_SECONDS` | `15` | entero; un valor no numérico es un error de arranque |
| `BREVIS_DB_MAX_CONNS` | driver_dd | límite del pool de Postgres de cada proceso; un valor no numérico o negativo es un error de arranque |
| `BREVIS_CONSOLE_STATEMENT_TIMEOUT_SECONDS` | `0` (desactivado) | solo la consola (`brevis serve`): Postgres aborta cualquiera de sus sentencias que dure más, para que una consulta de listado desbocada no retenga una conexión. `30` es el valor recomendado donde el pool acepta parámetros de inicio; está desactivado por defecto porque un PgBouncer en modo transaction rechaza la conexión en caso contrario. Se envía como parámetro de inicio de la conexión, así que un PgBouncer en modo transaction necesita `ignore_startup_parameters = statement_timeout` |
| `BREVIS_RETENTION_TRIM_DAYS` | `0` (desactivado) | limpieza programada opcional, ejecutada por `brevis scheduler`: vacía el log, las fases y la salida de los runs más antiguos que este valor. Mismo efecto que `brevis prune --trim-after`. `0` = el scheduler nunca limpia |
| `BREVIS_RETENTION_PURGE_DAYS` | `0` (nunca) | junto con el anterior, también elimina los runs más antiguos que este valor. Debe ser como mínimo el valor de trim |
| `BREVIS_RETENTION_INTERVAL_HOURS` | `24` | con qué frecuencia se ejecuta la limpieza programada (una vez al arrancar, y después cada N horas) |
| `BREVIS_BRAND_FILE` | `brand.yaml` | branding; ausente = valor por defecto |
| `BREVIS_UI_URL` | — | base del enlace al run en las alertas |
| `BREVIS_METRICS_ADDR` | — | puerto de métricas de Prometheus; vacío = no se expone. Ver [Observabilidad](/docs/observability/) |
| `BREVIS_SLACK_WEBHOOK` | — | destino de las alertas de fallo. Vacío = nadie se entera |

## Autenticación

| variable | |
|---|---|
| `BREVIS_AUTH_USER` | usuario de la interfaz |
| `BREVIS_AUTH_PASSWORD_HASH` | hash `pbkdf2-sha256$…`, generado por `brevis hash` |
| `BREVIS_AUTH_SECRET` | 32+ bytes para firmar la sesión (`openssl rand -base64 48`) |

Las tres van juntas, o ninguna.

| variable | valor por defecto | |
|---|---|---|
| `BREVIS_AUTH_CLIENT_IP_HEADER` | — | cabecera que tu proxy rellena con la dirección del cliente, p. ej. `X-Forwarded-For`. Se usa la **última** entrada (la que añadió tu proxy). Vacío = la dirección de la conexión |

El inicio de sesión se limita por dirección de cliente (IPv6 por su /64): tras tres fallos la dirección se bloquea 2 segundos, y el tiempo se duplica hasta 15 minutos. **Detrás de un ingress o balanceador, define la variable** (el chart de Helm lo hace cuando `api.ingress.enabled` está activo; se sobrescribe con `auth.clientIPHeader`): sin ella todos los clientes comparten la dirección del proxy, así que una persona que adivine contraseñas bloquearía a todos. En ese caso, fuera de `BREVIS_ENV=local`, el bloqueo se limita a 30 segundos y se registra una advertencia al arrancar. Define la variable solo si la cabecera la escribe un proxy de confianza y el cliente no puede falsificarla. Solo se ejecuta una comprobación de contraseña por dirección a la vez, así que dos personas detrás de un mismo NAT que inicien sesión en el mismo segundo pueden ver un 429 por un instante.

:::danger A medio configurar es un error de arranque
Quien rellenó el nombre de usuario cree que la puerta está cerrada. La interfaz
dispara pipelines: un `POST /workflows/<slug>/trigger` ejecuta un `dbt build` que
escribe en el warehouse. Abierta a internet, es un control remoto del
warehouse, y una advertencia en el log no deshace esa creencia, porque nadie lee
el log de un proceso que funciona.
:::

Fuera de `BREVIS_ENV=local`, arrancar sin credenciales se rechaza:

```
error: BREVIS_ENV=production requires a credential: set BREVIS_AUTH_USER,
BREVIS_AUTH_PASSWORD_HASH (generate one with `brevis hash`) and BREVIS_AUTH_SECRET
```

:::note Los nombres antiguos siguen funcionando
Cuatro variables se renombraron el 2026-09-10, cuando los últimos nombres en portugués salieron
del lado del operador del proyecto: `BREVIS_AUTH_USUARIO`,
`BREVIS_AUTH_SENHA_HASH`, `BREVIS_AUTH_SEGREDO` y
`BREVIS_POD_MANTER_EM_FALHA`. Una instalación que define el nombre antiguo sigue
funcionando y registra una advertencia con el nombre nuevo.

Se añade el nombre nuevo, se despliega y luego se quita el antiguo; el nombre nuevo gana cuando ambos
están definidos. Los nombres antiguos desaparecerán en una versión mayor.

En el SDK, `BREVIS_SDK_LIMITE_INLINE` pasó a ser `BREVIS_SDK_INLINE_LIMIT` bajo
la misma regla.
:::

## Entorno de la tarea

| variable | valor por defecto | |
|---|---|---|
| `BREVIS_TASK_ENV` | vacío | lo que recibe cada step |

Una tarea **no** hereda el entorno del orquestador. El motivo es
concreto: el proceso lleva `BREVIS_DATABASE_URL` con el usuario y la contraseña de Postgres,
y un workflow es un comando arbitrario escrito por otra persona: heredar
entregaría las credenciales de la base de datos a cada step de cada pipeline.

Lo que la tarea necesita se declara:

```bash
BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE   # forwards these two from the process
BREVIS_TASK_ENV=STAGE=prod                # sets a literal
BREVIS_TASK_ENV='*'                       # forwards everything EXCEPT BREVIS_*
```

`PATH` y `HOME` siempre se incluyen: sin `PATH` ningún comando se resuelve, y
el error sería un «not found» que no explica nada.

Un nombre ausente **no** se convierte en un string vacío: `GOOGLE_PROJECT_ID=""` haría
que dbt fallara con un mensaje peor que el de una variable ausente.

## Kubernetes

Decisiones de la **instalación**, no del workflow.

| variable | valor por defecto | formato |
|---|---|---|
| `BREVIS_PODS` | `auto` | `auto`, `on` u `off`; cualquier otro valor es un error de arranque |
| `BREVIS_POD_NAMESPACE` | el del clúster | |
| `BREVIS_POD_SERVICE_ACCOUNT` | — | |
| `BREVIS_POD_PULL_SECRETS` | — | lista separada por comas |
| `BREVIS_POD_ENV_FROM_SECRETS` | — | lista separada por comas |
| `BREVIS_POD_ENV_FROM_CONFIGMAPS` | — | lista separada por comas |
| `BREVIS_POD_ALLOWED_SECRETS` | — | lista separada por comas |
| `BREVIS_POD_CREDENTIAL_PATH` | — | ruta montada en el pod |
| `BREVIS_POD_CREDENTIAL_PVC` | — | PVC que contiene las credenciales |
| `BREVIS_POD_NODE_SELECTOR` | — | `key=value,other=value` |
| `BREVIS_POD_TOLERATIONS` | — | `key=value:effect,…`; solo el operador `Equal` |
| `BREVIS_POD_KEEP_ON_FAILURE` | `false` | `true` conserva el pod para inspección |

En las listas, las entradas vacías se ignoran: `a,,b` es un error tipográfico, y un nombre de secret
vacío haría que el servidor rechazara el pod completo.

## Pruebas

| variable | |
|---|---|
| `BREVIS_TEST_DATABASE_URL` | base de datos para las pruebas de integración; sin ella, se omiten |

## Endpoints de salud

| | |
|---|---|
| `GET /health` | liveness — **no** consulta la base de datos |
| `GET /ready` | readiness — consulta, y nombra la dependencia que falló |

La separación es deliberada: una sonda de liveness que depende de un servicio
externo hace que Kubernetes **mate el pod** cuando la base de datos titubea, en lugar de
simplemente sacarlo del balanceador de carga.

## Un ejemplo completo

```bash
# required
export BREVIS_DATABASE_URL='postgres://brevis:secret@db:5432/brevis?sslmode=require'

# production requires credentials
export BREVIS_ENV=production
export BREVIS_AUTH_USER=operator
export BREVIS_AUTH_PASSWORD_HASH="$(brevis hash < password.txt)"
export BREVIS_AUTH_SECRET="$(openssl rand -base64 48)"

# what the tasks need, and nothing more
export BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE,DBT_KEYFILE

# pods
export BREVIS_PODS=on
export BREVIS_POD_NAMESPACE=data
export BREVIS_POD_SERVICE_ACCOUNT=brevis-runner
export BREVIS_POD_ENV_FROM_SECRETS=bigquery-cred

# alerts
export BREVIS_SLACK_WEBHOOK='https://hooks.slack.com/services/...'
export BREVIS_UI_URL='https://brevis.example.com'
```
