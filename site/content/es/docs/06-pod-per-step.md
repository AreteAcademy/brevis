---
title: Pod por step
description: Por qué cada step corre en su propio pod, y qué cambia eso en costo y aislamiento.
group: Conceptos
order: 6
slug: pod-per-step
---

En Kubernetes, **cada step de un workflow se convierte en un pod** con la imagen declarada en
ese step. No hay un worker genérico esperando trabajo: el trabajo trae su propio
runtime.

![Diagrama: el Core inicia un pod por step — extract en Go, prepare en Python, load en Go y transform en dbt. Los steps se pasan contexto entre sí, y el SDK es quien lo lee y lo escribe.](/assets/execution.svg)

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

## Qué cambia esto

Un worker monolítico obliga a que la imagen contenga todo lo que cualquier step pueda necesitar.
En la práctica:

| | imagen única | pod por step |
|---|---|---|
| step de dbt | 1,9 GB · 1Gi RAM | 1,9 GB · 1Gi RAM |
| fetcher en Go a su lado | **1,9 GB · 1Gi** | **12 MB · 32Mi** |
| subir la versión de dbt | reconstruir todo | una línea en el YAML de ese workflow |
| un step con fuga de memoria | derriba al worker y a sus vecinos | muere solo |

La tercera fila es la menos obvia y la más cara en el día a día: con una
sola imagen, pasar dbt de 1.10 a 1.11 en un pipeline obliga a hacerlo
en todos.

## Imágenes por rol

Las imágenes son por **rol**, no por proyecto. Todo lo que sobra en una imagen lo
descarga cada nodo que ejecuta ese step:

| rol | tamaño | arranque en frío |
|---|---|---|
| **Go** | **5,8 MB** | 0,18 s |
| **Python** | **118 MB** | 0,54 s |
| **dbt** | **620 MB** | 3,3 s |

Antes: una única imagen de 1,87 GB para todo.

Para dbt, `dbt parse` se ejecuta en tiempo de build y `target/partial_parse.msgpack`
viaja en la imagen: **2,68 s menos por pod**, en cada invocación.

## Dónde se ejecuta cada step

La decisión se toma una vez, al arrancar, y depende de `BREVIS_PODS` y de si
hay un clúster:

| `BREVIS_PODS` | con clúster | sin clúster |
|---|---|---|
| `auto` (por defecto) | un step con `image:` se convierte en pod | todo como procesos locales, con una advertencia en el log |
| `on` | un step con `image:` se convierte en pod | **error de arranque** |
| `off` | todo como procesos locales | todo como procesos locales |

**Un step con `host:` no pasa por nada de esto.** Se evalúa antes que el
pod y antes que el proceso, y va a una máquina que ya está en marcha:
`BREVIS_PODS` no le aplica, y nunca cae a un proceso local.
Es deliberado: `image:` degrada a ejecución local con una advertencia porque
un contenedor y un proceso son el mismo comando en dos envoltorios, y un host no
lo es. Es donde vive la licencia, o la GPU, o los datos.

`auto` es el valor por defecto porque el mismo binario corre en ambos lugares: en un portátil
no hay ninguna service account montada y cae a procesos locales; en un clúster
sí la hay, y empieza a crear pods.

`on` existe para el despliegue que **no debe** convertirse en silencio en ejecución
local. Ahí, no tener clúster tiene que ser un error de arranque.

:::warning `brevis run` no crea pods
El comando `run` construye solo el ejecutor de procesos. Un step con `image:` corre en
la propia instancia, y lo dice. Callar haría parecer que el
step se ejecutó en la imagen declarada, el tipo de error que solo aflora
cuando el resultado ya es incorrecto.
:::

## Identidad y credenciales

La identidad con la que arrancan los pods es una decisión de la **instalación**, no del
workflow: un pipeline no debe poder elegir la service account con la que corre. Por eso
viene del entorno:

```bash
BREVIS_POD_NAMESPACE=data
BREVIS_POD_SERVICE_ACCOUNT=brevis-runner
BREVIS_POD_PULL_SECRETS=registry-cred
BREVIS_POD_ENV_FROM_SECRETS=bigquery-cred,api-tokens
```

En modo pod, el entorno de la tarea viene de Secrets del clúster, no de
`BREVIS_TASK_ENV`.

## Node selector y tolerations

```bash
BREVIS_POD_NODE_SELECTOR='kubernetes.io/arch=arm64'
BREVIS_POD_TOLERATIONS='dedicated=data:NoSchedule'
```

En las tolerations solo se acepta el operador `Equal`: `Exists` toleraría
**cualquier** taint con esa clave, demasiado amplio para una decisión que viene de una variable
de entorno.

Una toleration mal formada se **ignora** en lugar de convertirse en error de arranque: deja
el pod en `Pending`, que es visible, mientras que negarse a arrancar también detendría
los workflows que no necesitan ese pool.

## Depurar un fallo

```bash
BREVIS_POD_KEEP_ON_FAILURE=true
```

Conserva el pod fallido para `kubectl logs` y `kubectl describe`. Déjese
apagado en producción: los pods detenidos consumen cuota del clúster.

## Siguientes pasos

- [Kubernetes](/docs/kubernetes/) — el despliegue completo
- [Configuración](/docs/configuration/) — cada variable `BREVIS_POD_*`
- [Observabilidad](/docs/observability/) — métricas de ambos procesos
