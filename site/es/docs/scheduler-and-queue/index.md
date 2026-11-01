# Scheduler y cola

> Dos bucles independientes: quién crea los runs, quién los ejecuta y por qué están separados.

*https://brevis.sh/es/docs/scheduler-and-queue/ · brevis.sh docs (es)*

---

brevis.sh separa **crear** un run de **ejecutarlo**. Dos bucles corren
juntos y ninguno depende del otro.

```
scheduler                          queue
─────────                          ─────
reads the schedule
materialises the slot
creates the Run  ─────────────►  (pending in the database)
                                   claims it
                                   walks the graph
                                   executes each step
                                   records state and log
```

## Por qué separarlos

Un único bucle que programa y ejecuta a la vez tiene una mala propiedad: cuando
la ejecución se atasca, la programación se detiene con ella. Las próximas doce horas de slots simplemente
no existen, y nadie lo nota hasta que alguien busca datos que nunca
llegaron.

Con los dos por separado:

- el **scheduler** puede caerse y la cola sigue vaciando lo que se creó;
- la **cola** puede caerse y los slots se siguen materializando, para ejecutarse
  cuando vuelva;
- **el reprocesamiento no depende del reloj**: `backfill` crea runs pasados
  por el mismo camino que usaría cron.

## El proceso

Ambos bucles viven en el mismo comando:

```bash
brevis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| flag | valor por defecto | |
|---|---|---|
| `--interval` | `10s` | intervalo entre ciclos del scheduler |
| `--concurrency` | `5` | **runs** simultáneos |
| `--max-pods` | `5` | **steps** simultáneos en total |

`--concurrency` y `--max-pods` cuentan cosas distintas, deliberadamente: cinco runs
con tres steps paralelos cada uno serían quince pods si el único límite estuviera en
los runs.

:::note `serve` no materializa programaciones
El proceso de la API levanta el mismo scheduler, pero **sin el bucle**: está
ahí solo para atender los disparos manuales desde la interfaz, de modo que la regla «el
scheduler crea runs» tenga un único dueño. Para que las programaciones se disparen, debe
correr un `brevis scheduler` a su lado.
:::

## El snapshot de la definición

Cuando el scheduler crea un run, almacena **la definición del workflow dentro del
run**. La ejecución lee ese snapshot, no el YAML en disco.

El motivo es concreto: entre el disparo y la ejecución, alguien pudo haber editado
el archivo y desplegado. Sin el snapshot, un run creado a las 05:00 con una
definición se ejecutaría a las 05:02 con otra, y el log no tendría forma
de explicar la diferencia.

## Reintentos

Un run fallido se intenta **tres veces**, y la espera se **duplica**: con los
valores por defecto caen en **0s, 30s y 1m30s**.

```bash
brevis scheduler --max-attempts 3 --retry-backoff 30s --retry-backoff-max 1h
```

| flag | valor por defecto | |
|---|---|---|
| `--max-attempts` | `3` | intentos por run, contando el primero |
| `--retry-backoff` | `30s` | la primera espera, que se duplica en cada intento posterior |
| `--retry-backoff-max` | `1h` | techo para esa espera |

El proceso imprime el calendario al arrancar, de modo que nunca haya que deducirlo de
dos números:

```
scheduler and dispatcher are up  max_attempts=3 retry_backoff=30s retry_at="0s, 30s, 1m30s"
```

:::tip
**Un proveedor con límite de tasa necesita más tiempo.** Estos pipelines fallan por una razón
por encima de todas: un upstream transitorio, una API que responde 200 con «has
alcanzado tu límite de peticiones», un pico de cuota del warehouse. Eso tarda alrededor de un minuto
en despejarse, y un reintento que termina antes es un reintento que nunca ocurrió.
`--retry-backoff 1m` pone los intentos en 0s, 1m y 3m.
:::

El reintento es por **run**, y reejecuta solo los steps que fallaron: un
`dbt_build` que falló no vuelve a ejecutar el fetch anterior que tuvo éxito.

El intento se persiste, no es estado del proceso. Reiniciar el worker no
borra lo que ya se sabía de ese run.

El número de intento del **run** va en el nombre del pod. Sin él, un reintento
reiniciaría el run desde cero, se encontraría con el pod del intento anterior bajo el mismo nombre
y se quedaría en `Pending` para siempre.

## Backfill

Materializa slots pasados de un workflow ya publicado:

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

```
  31 backfill run(s) queued for daily (2026-01-01 to 2026-01-31)
  run `brevis scheduler` to execute them
```

**Encola, no ejecuta.** La ejecución es trabajo del `scheduler`: la
misma separación de nuevo. `--to` incluye el día completo.

## Iniciado por un landing

Un workflow puede ejecutarse porque llegaron datos, en lugar de porque lo dijo el reloj:

```yaml
trigger:
  on_landed:
    - bigquery://acme-prod/bronze/orders
    - bigquery://acme-prod/bronze/*
  debounce: 5m
```

Cada step ya declara lo que escribió, por el canal que transporta las fases;
eso es lo que llena `/data`. Un workflow se suscribe a uno de esos nombres y
arranca cuando algo aterriza en él.

Un `/*` al final suscribe a todo el dataset. Existe para `auto_table`,
que crea una tabla por ruta: el dataset es lo único que se puede
nombrar para tablas que nadie declaró. Coincide con `bronze/orders` y no con
`bronze_raw/orders`.

### La ventana, y por qué diez landings son un run

`debounce` colapsa una ráfaga. Diez tablas que aterrizan dentro de cinco minutos inician
**un** run, no diez, y el run dice cuáles:

```
BREVIS_AUTO_LANDED=bigquery://acme-prod/bronze/orders,bigquery://acme-prod/bronze/items
```

La lista exacta también está en `BREVIS_AUTO_PARAMS` como JSON, que es lo que hay que leer
cuando un destino podría contener una coma.

La ventana se corta desde **el momento en que el engine registró el landing**, no desde
el momento en que el step dijo que ocurrió. Un step que informa el instante en que empezó su consulta
informa algo cierto y algo viejo, y un reloj desfasado de otro modo
partiría una ráfaga en dos runs.

Si se omite `debounce`, cada landing inicia un run.

### Lo que no hará

**Un workflow nunca es iniciado por su propia escritura.** El engine descarta un landing
cuyo escritor es el workflow suscrito y registra por qué; sin eso, un workflow
que lee y escribe la misma tabla se dispararía a sí mismo para siempre.

Se rechaza **cuando se dispara** y no cuando se verifica el archivo, y eso es un
límite y no un descuido: `brevis validate` lee archivos y ningún archivo
declara lo que escribe un step. Un destino de warehouse lo decide la conexión,
no el YAML. Así que `validate` aprueba un workflow que será rechazado en
tiempo de ejecución, siempre, con el motivo en el log.

**El primer ciclo no inicia nada.** Un trigger empieza a contar desde el momento
en que entra en vigor, como una programación; de lo contrario, publicar uno dispararía un run por
cada landing del historial del catálogo.

## Alertas

```bash
export BREVIS_SLACK_WEBHOOK='https://hooks.slack.com/services/...'
export BREVIS_UI_URL='https://brevis.example.com'
```

Sin el webhook, el proceso avisa al arrancar que los fallos no se
reportarán. Una instalación que falla en silencio suele ser descubierta
por el cliente, no por el equipo.

`BREVIS_UI_URL` construye el enlace al run dentro de la alerta; sin él, la alerta
dice qué falló pero obliga al lector a buscar el run a mano.

## Siguientes pasos

- [Pod por step](/es/docs/pod-per-step/index.md) — dónde se ejecuta realmente cada step
- [CLI](/es/docs/cli/index.md) — cada flag de `scheduler` y `backfill`
