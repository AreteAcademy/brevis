---
title: CLI
description: Los doce subcomandos del engine, con flags, argumentos y lo que requiere cada uno.
group: Referencia
order: 8
slug: cli
---

El engine es un solo binario con subcomandos, no varios binarios: la API, el scheduler
y los workers son roles del mismo sistema, y un binario mantiene una versión, una
imagen y un camino de build.

```
brevis [command] [flags]
```

| comando | base de datos | credenciales fuera de `local` | rol |
|---|---|---|---|
| [`serve`](#serve) | **sí** | **obligatorias** | API + interfaz |
| [`scheduler`](#scheduler) | **sí** | **obligatorias** | ambos bucles |
| [`migrate`](#migrate) | **sí** | **obligatorias** | esquema |
| [`publish`](#publish) | **sí** | **obligatorias** | almacena workflow y programación |
| [`backfill`](#backfill) | **sí** | **obligatorias** | reprocesa un rango |
| [`cancel`](#cancel) | **sí** | **obligatorias** | detiene un run |
| [`run`](#run) | no | — | ejecuta ahora, en la propia instancia |
| [`validate`](#validate) | no | — | valida el YAML de workflows |
| [`brand`](#brand) | no | — | valida el YAML de branding |
| [`hash`](#hash) | no | — | genera el hash de la contraseña |
| [`version`](#version) | no | — | versión, commit, build |

Los primeros seis cargan la configuración, que **falla al arrancar** sin
`BREVIS_DATABASE_URL`. Los otros cinco no, y por eso `validate` funciona en
CI, donde no hay Postgres.

Esos mismos seis heredan la regla de credenciales: con `BREVIS_ENV` distinto de
`local`, arrancar sin las tres variables de autenticación es un **error de
arranque**.

---

## serve

Levanta la API HTTP y la interfaz. Sin flags: todo viene del
entorno.

```bash
brevis serve
```

Se apaga con `SIGINT`/`SIGTERM` de forma ordenada, para que un despliegue no corte las peticiones
en curso. Este proceso **no materializa programaciones**: para eso hace falta un
`brevis scheduler` a su lado.

`CMD` de la imagen `api` (distroless, sin shell).

---

## scheduler

Ambos bucles: el scheduler materializa las programaciones en runs, el dispatcher toma
de la cola y ejecuta.

```bash
brevis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| flag | tipo | valor por defecto | |
|---|---|---|---|
| `--interval` | duration | `10s` | intervalo entre ciclos |
| `--concurrency` | int | `5` | **runs** simultáneos |
| `--max-pods` | int | `5` | **steps** simultáneos en total |

`CMD` de la imagen `worker` (alpine con shell, porque los steps `run:` necesitan
una).

---

## alert

Entrega las alertas que registró el scheduler. Un tercer proceso junto a `serve` y
`scheduler`.

```bash
brevis alert --interval 1s --max-attempts 6
```

| flag | tipo | valor por defecto | |
|---|---|---|---|
| `--interval` | duration | `1s` | intervalo entre ciclos de entrega |
| `--max-attempts` | int | `6` | intentos antes de que una alerta se registre como no entregada |

**Existe por el lugar donde se escribe una alerta, no porque la entrega merezca
un proceso.** El scheduler registra la alerta en la misma transacción que el
fallo que la justifica: o el run se quedó sin intentos y la alerta existe, o ninguna de las dos cosas ocurrió. Algo tiene que vaciar entonces esa tabla, y tiene
que sobrevivir tanto a una caída de Slack como a su propio reinicio. Antes de esto, un webhook
que fallaba significaba una línea de log y una alerta que simplemente se perdía.

Se niega a arrancar sin canal configurado (`BREVIS_SLACK_WEBHOOK`). Un
proceso de entrega sin adónde entregar vacía el outbox hacia «no entregadas»
tan rápido como se llena, y esas filas se ven exactamente como si Slack las rechazara.

Una alerta a la que renuncia se **conserva**: «generada, no entregada, 4 intentos, 403
de Slack» es la fila que importa, porque es el caso en que alguien
espera un mensaje que no llegará.

`CMD` de la imagen `worker`.

---

## report

Envía el resumen periódico de lo que se ejecutó.

```bash
brevis report --window 168h              # sends it
brevis report --window 168h --dry-run    # prints it, sends nothing
```

| flag | tipo | valor por defecto | |
|---|---|---|---|
| `--window` | duration | `168h` | cuánto hacia atrás mirar |
| `--dry-run` | bool | `false` | imprimir en lugar de enviar |

Un **comando**, no un bucle, y esa es la diferencia entre un reporte y una
alerta: se ejecuta desde un CronJob, así que no hay bucle nuevo, ni estado nuevo, ni
elección de líder; el clúster ya tiene un scheduler. `--dry-run` es como
alguien llega a confiar en un mensaje semanal: leyendo lo que habría dicho.

**No** pasa por el outbox de alertas. Ambos comparten un canal de entrega y
nada más: perder una alerta es una caída de la que nadie se entera, y perder
el resumen de una semana es esperar el de la semana siguiente.

El mensaje incluye runs, éxitos, fallos, los pipelines que más fallaron,
los que más tardaron, y las filas y bytes que informó el SDK. **No** incluye CPU ni memoria, y tampoco imprime cero para ellas: el
engine no los recolecta, y el mensaje dice dónde viven.

Una ventana vacía lo dice en lugar de informar 100%. Una tasa de éxito calculada sobre
nada es el número más tranquilizador que puede imprimir un reporte y el menos cierto:
una semana vacía suele significar que el scheduler estaba caído.

`deployments/kubernetes/report.yaml` lo ejecuta semanalmente.

---

## migrate

```bash
brevis migrate up      # applies what is missing
brevis migrate down    # reverts the last one
brevis migrate status  # shows the state
```

Recibe exactamente un argumento. Las migraciones están incorporadas en el binario y se aplican con
su propio subcomando; **`serve` nunca altera el esquema**.

---

## validate

Valida uno o más archivos de workflow. Acepta un archivo **o un directorio**.

```bash
brevis validate examples/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

No toca ninguna base de datos ni inicia ningún servidor, así que corre en el editor y en CI.
Sale con código distinto de cero y cuenta los fallos.

---

## run

Ejecuta un workflow **ahora, en la propia instancia**: sin cola, sin base de datos, sin
scheduler.

```bash
brevis run examples/hello.yaml
brevis run wf.yaml --param load_full=true --retries 3 --timeout 5m
```

| flag | tipo | valor por defecto | |
|---|---|---|---|
| `--param` | repetible | — | `key=value` para un parámetro declarado |
| `--workdir` | string | el directorio del archivo | directorio de trabajo de los steps |
| `--retries` | int | `1` | intentos por step (`1` = sin reintento) |
| `--timeout` | duration | `0` | timeout por step (`0` = sin límite) |

`--param` sin `=` es un **error**, no una advertencia: `--param load_full`,
olvidando el valor, se ejecutaría con el valor por defecto, y el operador
creería que se aplicó el valor.

:::warning Cuatro límites
`run` solo opera con `BREVIS_ENV=local`; un step con `image:` corre en la
propia instancia y lo dice; no hay registro de tareas de Go, así que un `action:` de
una tarea no registrada falla nombrando las disponibles; y `run` no lee
`BREVIS_HOSTS`, así que un step con `host:` no puede ejecutarse aquí en absoluto: falla nombrando
el host, incluso con la variable definida.
:::

---

## publish

Almacena workflows y sus programaciones en la base de datos. Acepta un archivo o un
directorio.

```bash
brevis publish examples/hello.yaml
brevis publish workflows/ --project acme --prune
```

| flag | tipo | valor por defecto | |
|---|---|---|---|
| `--project` | string | `default` | slug del proyecto |
| `--prune` | bool | `false` | elimina del proyecto los workflows ausentes de la lista |

Con `--prune`, el historial de los workflows eliminados se conserva.

---

## backfill

Materializa slots pasados de un workflow ya publicado. **Encola, no
ejecuta.**

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

| flag | | |
|---|---|---|
| `--from` | **obligatorio** | fecha de inicio, `YYYY-MM-DD` |
| `--to` | **obligatorio** | fecha de fin, `YYYY-MM-DD` |
| `--param` | | se aplica a **cada** slot del rango |

`--to` incluye el día completo: internamente el final pasa a ser `23:59:59` de esa
fecha.

---

## cancel

Cancela un run que aún espera o que se está ejecutando. El feed de eventos muestra `cli`
como quien lo canceló.

```bash
brevis cancel 3f1c9a52-6d0e-4b7a-9c41-2b8e5d7f0a13
```

| respuesta | qué ocurrió |
|---|---|
| `canceled` | el run esperaba en la cola; salió de ella y no se ejecutará |
| `cancel requested` | el run se está ejecutando; su dispatcher detiene los steps dentro de `--cancel-poll` (2s) |
| `already ended: <status>` | el run ya había terminado; nada cambia |

Un run cancelado no se reintenta. Un id que no existe es un error.

---

## gateway

Cómo aparece un gateway en `/data`: su configuración, publicada como un acto
deliberado, nunca su tráfico, y nunca en tiempo de ejecución.

```bash
gateway describe gateway.yaml > manifest.json   # the gateway's own binary
brevis gateway publish manifest.json
brevis gateway unpublish events_gateway
```

`publish` verifica el manifiesto completo antes de escribir nada, nombrando el stream
y el rol del primer problema, y reemplaza los destinos de ese gateway en una sola
transacción. `unpublish` elimina un gateway dado de baja, y es un error cuando
no se publicó ningún gateway con ese nombre.

Cómo aparece un `brevis-sql-api` en Datos → Endpoints: lo que cada endpoint recibe
y lee, publicado como un acto deliberado, nunca su tráfico.

```bash
brevis-sql-api describe --config api.yaml --out manifest.json   # the API's own binary
brevis endpoints publish manifest.json
brevis endpoints unpublish metrics-api
```

`publish` verifica el manifiesto completo antes de escribir nada y reemplaza los endpoints de esa
API en una sola transacción; un nombre, valor por defecto o nota con forma de credencial rechaza todo.

Es un comando propio y no un modo de `brevis publish`, porque los workflows
y los gateways tienen semánticas de conjunto distintas: `publish --prune` nunca debe alcanzar las
filas de un gateway.

---

## prune

Aplica la política de retención. Es **lo único** en Brevis que borra
algo, y nunca se ejecuta por sí solo: un operador lo programa.

```bash
brevis prune --dry-run          # what would go, changing nothing
brevis prune
brevis prune --purge-after 0    # trim only, never delete a run
```

| flag | valor por defecto | |
|---|---|---|
| `--trim-after` | `720h` | vacía el log, las fases y la salida publicada |
| `--purge-after` | `8760h` | elimina el run por completo; `0` nunca elimina |

---

## brand

Valida un archivo de branding sin iniciar el servidor.

```bash
brevis brand brand.yaml
```

```
  ok    Brevis · Orchestration
        logo      /assets/logo.svg  (built-in symbol)
        accent    (the product's own)
        Powered by Brevis
```

Una diferencia de comportamiento respecto al arranque: **un archivo ausente es un error**. En
`serve`, la ausencia significa «usar la identidad por defecto»; quien pidió validar
una ruta espera que le digan que no existe.

---

## hash

Genera el valor de `BREVIS_AUTH_PASSWORD_HASH`.

```bash
brevis hash
```

La contraseña se lee del terminal **sin eco**, nunca de un argumento:
un argumento aparece en el `ps` de cualquier proceso y queda en el historial de la shell. Rechaza
contraseñas de menos de 12 caracteres.

El hash va solo a **stdout** y las etiquetas a **stderr**, de modo que
`brevis hash > hash.txt` guarda solo lo que importa.

---

## version

```bash
brevis version
```

```
brevis 0.11.2
  commit  bb832ff
  build   2026-09-05T12:00:00Z
  go      go1.25.7 darwin/arm64
```

Compilado directamente con `go build`, imprime `brevis dev` sin commit ni
fecha, y distinguirlo de un artefacto de release importa cuando alguien
reporta un comportamiento extraño.
