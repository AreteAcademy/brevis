# Configuración

> Cada variable de entorno, con su valor por defecto, formato y qué ocurre cuando falta.

*https://brevis.sh/es/docs/configuration/ · brevis.sh docs (es)*

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
| `BREVIS_ENV` | `local` | `local` registra logs en texto y deja la interfaz abierta; cualquier otro valor exige credenciales y registra JSON |
| `BREVIS_HTTP_ADDR` | `:8080` | dirección de escucha |
| `BREVIS_LOG_LEVEL` | `info` | |
| `BREVIS_SHUTDOWN_TIMEOUT_SECONDS` | `15` | entero; un valor no numérico es un error de arranque |
| `BREVIS_BRAND_FILE` | `brand.yaml` | branding; ausente = valor por defecto |
| `BREVIS_UI_URL` | — | base del enlace al run en las alertas |
| `BREVIS_METRICS_ADDR` | — | puerto de métricas de Prometheus; vacío = no se expone. Ver [Observabilidad](/es/docs/observability/index.md) |
| `BREVIS_SLACK_WEBHOOK` | — | destino de las alertas de fallo. Vacío = nadie se entera |

## Autenticación

| variable | |
|---|---|
| `BREVIS_AUTH_USER` | usuario de la interfaz |
| `BREVIS_AUTH_PASSWORD_HASH` | hash `pbkdf2-sha256$…`, generado por `brevis hash` |
| `BREVIS_AUTH_SECRET` | 32+ bytes para firmar la sesión (`openssl rand -base64 48`) |

Las tres van juntas, o ninguna.

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
