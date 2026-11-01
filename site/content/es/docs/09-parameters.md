---
title: Parámetros
description: Lo que cambia entre dos runs del mismo workflow, sin editar el archivo.
group: Referencia
order: 9
slug: parameters
---

Un workflow puede declarar **parámetros de run**: valores que cambian entre dos
disparos sin que el archivo cambie.

## Declarar

```yaml
params:
  - name: load_full
    type: boolean
    default: "false"

  - name: start_date
    type: string
    pattern: '^\d{4}-\d{2}-\d{2}$'

steps:
  - id: run
    run: dbt build --vars '{"load_full":"{{ .load_full }}"}' --select bronze+
```

| campo | | |
|---|---|---|
| `name` | **obligatorio** | el nombre usado en la plantilla y en `--param` |
| `type` | | `string`, `boolean`, `integer`, o `list|<type>` para varios valores |
| `default` | | se usa cuando el disparo no aporta ningún valor |
| `pattern` | | expresión regular que el valor debe cumplir |
| `title` | | la etiqueta que muestra el formulario, hasta 60 caracteres; si no, el nombre |
| `group` | | agrupa parámetros en el formulario, hasta 60 caracteres |
| `warn` | | lo que cuesta un valor distinto del valor por defecto, se muestra bajo el campo, hasta 200 caracteres |
| `uses` | | la clave del conector del que trata el parámetro (`bigquery`, `postgres`), para mostrar su logo |

Un parámetro **sin `default`** es obligatorio: un disparo que lo omite falla
antes de ejecutar cualquier step.

`title`, `group`, `warn` y `uses` son la forma en que el formulario presenta el parámetro,
y **ninguno cambia lo que un valor puede ser**. Cada uno es una línea; `uses` son
letras minúsculas, dígitos y `-`. El publish rechaza cualquier otra cosa, nombrando
el parámetro.

```yaml
params:
  - name: load_full
    type: boolean
    default: "false"
    title: Full load
    group: Load
    warn: reprocesses the whole table; it costs
    uses: bigquery
```

## Una lista de valores

`list|<type>` declara un parámetro que lleva muchos valores: las tablas a
cargar, las capas a reconstruir, las ventanas a rellenar:

```yaml
params:
  - name: tables
    type: list|string
    default: "users,orders"

  - name: layers
    type: list|string
    enum: [bronze, silver, gold]     # an enum restricts each ITEM
    default: "bronze"

  - name: days
    type: list|integer
    default: "1,7,30"
```

El elemento puede ser `string`, `integer` o `boolean`. Cada ítem se valida por
separado, así que `list|integer` con `1,x,30` falla nombrando el **ítem 2** en lugar de
el valor completo.

Es **un único string separado por comas** en todas partes: en la base de datos, en
`BREVIS_RUN_PARAMS` y en el comando. Eso es lo que hace que funcione en un step
sin ningún caso especial:

```yaml
run: dbt build --select {{ .tables }}      # dbt build --select users,orders
```

y es la razón por la que una coma no puede aparecer dentro de un ítem. Un ítem vacío y uno
repetido también se rechazan: una repetición duplica en silencio lo que el step haga
por ítem.

### Como arreglo, en un step

```go
for _, table := range p.Run.ParamList("tables") {
    load(table)
}
```

```python
from brevis import run

for table in run.param_list("tables"):
    load(table)
```

Ambos devuelven `[]` para un parámetro que nadie rellenó, en lugar de una lista con
un string vacío: iterar sobre «nada que hacer» no debe hacer nada. Los ítems
vuelven como strings —`list|integer` incluido— porque el step sabe mejor
que la biblioteca qué hacer con un número.

### En la interfaz

Una lista con `enum` se muestra como casillas de verificación, que no necesitan JavaScript y que
el navegador envía un valor por casilla marcada. Sin `enum` es un campo de texto
cuyos ítems se convierten en chips removibles, y con JavaScript desactivado sigue siendo un
campo con `users,orders`, que es exactamente lo que el servidor espera.

### En la línea de comandos

```bash
brevis run wf.yaml --param tables=events,sessions --param days=1,30
```

:::note Un engine más antiguo no lo leerá
El tipo se almacena con el workflow, y un engine anterior a esta funcionalidad
rechaza `list|string` en el publish como un tipo desconocido. Hay que actualizar el engine
antes de publicar un workflow que use uno.
:::

## Usarlo en el comando

El valor entra en `run` mediante una plantilla, entre llaves dobles con un punto:

```yaml
run: ./load.sh --since {{ .start_date }}
```

## Pasarlo por la línea de comandos

```bash
brevis run wf.yaml --param load_full=true
brevis run wf.yaml --param load_full=true --param start_date=2026-01-01
```

`--param` es repetible. Una entrada sin `=` es un **error**:

```
erro: --param "load_full": use chave=valor
```

Es deliberado. `--param load_full`, olvidando el valor, se ejecutaría con
el valor por defecto, y el operador creería que el backfill se hizo con el
parámetro que pretendía.

## En un backfill

Los valores se aplican a **cada** slot del rango. Este es el caso de uso central:
«reprocesar todo enero con `load_full=true`».

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

## En la interfaz

Un workflow con `params` obtiene un **formulario** en lugar del botón de disparo simple.
Los campos vienen de la declaración, y `pattern` se valida antes del envío.

## En el entorno del step

Los parámetros del run también entran en el **entorno** de cada step, con prefijo. Esto existe para
que un fetcher escrito con el [SDK](/docs/sdk/) pueda verlos sin recibir
nada como argumento:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    if p.Run.Params["load_full"] == "true" {
        p.Source.URL += "&full=1"
    }
    return nil
},
```

## Parámetros automáticos

Cada run también lleva **parámetros automáticos**: valores que el engine calcula por su cuenta.
Nadie los declara, todo run los tiene, y están en la pantalla del run.

El que importa es `adjusted_at`, y vale la pena nombrar el bug que elimina.
Un fetcher lee `now()`, resta su ventana y pide al proveedor las últimas
dos horas. En un run que arranca a tiempo eso es correcto. En un run que la cola
retrasó cuarenta minutos, está mal por cuarenta minutos, y esos cuarenta minutos
no pertenecen a **ningún run**, porque el siguiente slot también lee su propio `now()`.
Nada falla; los datos simplemente faltan, y se descubre semanas después.

Así que el engine entrega un reloj. Se lee `adjusted_at` donde se habría
leído `now()`:

| param | | |
|---|---|---|
| `adjusted_at` | siempre | el reloj que este run debe leer: el slot cuando lo hay, el inicio si no |
| `date` | siempre | `adjusted_at` como `YYYY-MM-DD`, UTC — la partición, la carpeta, el `WHERE` |
| `delay_seconds` | siempre | cuánto se retrasó este intento respecto de su slot; `0` en un run manual sin slot |
| `previous_error` | siempre | el run anterior a este no tuvo éxito |
| `scheduled_at` | con slot | el slot que pidió el cron |
| `started_at` | | cuándo empezó este intento; un reintento lo mueve |
| `interval_start` | con slot | el slot **anterior** |
| `interval_end` | con slot | este slot, **excluido** |
| `previous_success_at` | | el slot del último run que sí tuvo éxito |

Un run tiene slot cuando lo creó la programación, cuando lo creó un backfill, o cuando
se disparó a mano **para un slot pasado**: el campo `slot` del disparo
(`2026-10-27T04:00:00Z`) ejecuta ese slot de nuevo, con los params de ese slot. Un slot
que el cron nunca dispara se rechaza, indicando el más cercano anterior.

**Todas las marcas de tiempo aquí son UTC**, escritas como RFC 3339 con una `Z`, y la pantalla
del run también las muestra en UTC, de modo que la página y `$BREVIS_AUTO_ADJUSTED_AT` nunca
discrepan. `date` es el **día UTC**, que para un workflow cuyo slot cruza
la medianoche UTC no es el día local: un `0 22 * * *` en `America/Sao_Paulo` tiene un
`date` del día siguiente. El `ds` de Airflow se comporta igual, por la misma
razón: el slot es un instante, y un instante solo tiene un día una vez que se elige una
zona horaria.

`interval_start` e `interval_end` vienen del **cron**, no del
historial: un backfill de un slot de marzo produce la ventana que tuvo marzo, no la
ventana que los runs de este workflow describen hoy. Un pipeline que pide
`[start, end)` nunca se solapa y nunca deja huecos, por tarde que se ejecute y por muchas veces
que reintente.

Son un **snapshot**, calculado cuando el run arranca y almacenado en él.
`previous_error` es un hecho sobre el instante en que empezó este run; recalcularlo
mañana —después de que el run anterior se reintentara y pasara— respondería una
pregunta distinta con el mismo nombre.

### Leerlos

En un step de shell, una variable por valor, de modo que no haya que parsear nada:

```yaml
steps:
  - id: extract
    run: |
      curl "https://api.example.com/events?since=$BREVIS_AUTO_INTERVAL_START&until=$BREVIS_AUTO_INTERVAL_END" \
        > "/data/$BREVIS_AUTO_DATE.json"
```

El conjunto completo también está en `$BREVIS_AUTO_PARAMS` como un único objeto JSON, para un step
que prefiera parsear una sola vez.

En Go, el [SDK](/docs/sdk/) los lee por ti:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    start, end, ok := p.Run.Auto.Window()
    if !ok { // no schedule: fall back to a fixed window
        start, end = p.Run.Auto.Now().Add(-24*time.Hour), p.Run.Auto.Now()
    }
    p.Source.URL += "&from=" + start.Format(time.RFC3339) +
        "&to=" + end.Format(time.RFC3339)
    return nil
},
```

`Auto.Now()` es el reloj que se lee en lugar de `time.Now()`. Fuera del engine
—un fetcher que alguien ejecuta a mano— *es* el reloj de pared, así que no hay que
tratar nada como caso especial en el desarrollo local.

En Python, `pip install brevis` lee los mismos valores:

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

`window()` devuelve `None` cuando el workflow no tiene programación, en lugar de un par
de tiempos cero: una consulta desde la época selecciona todo, y ese fallo
no debe ser silencioso. El `or` de arriba es todo el fallback.

### En dlt, no hay nada que leer

[dlt](https://dlthub.com) mantiene su propio cursor, en un estado que escribe en el
destino. Ese estado solo avanza **hacia adelante**, lo cual está bien hasta que se vuelve a ejecutar
un slot antiguo: el cursor sigue diciendo hoy, así que el backfill lee la ventana de hoy,
y la respuesta de dlt para eso es descartar el estado.

La ventana que entrega Brevis viene del cron, así que el run de marzo produce
la ventana de marzo por muchas veces que se reintente. dlt la acepta: los nombres son
suyos, los lee antes de ponerse a buscar Airflow, y el engine los define en
cada run programado:

```python
@dlt.resource(primary_key="id")
def events(
    updated_at=dlt.sources.incremental(
        "updated_at",
        initial_value=datetime(1970, 1, 1, tzinfo=timezone.utc),
        allow_external_schedulers=True,   # this line is the whole integration
    )
):
    yield from api.events(since=updated_at.start_value, until=updated_at.end_value)
```

`allow_external_schedulers=True` es toda la integración. `$DLT_INTERVAL_START` y
`$DLT_INTERVAL_END` se convierten en el `initial_value` y el `end_value` de dlt, semiabiertos
en ambos lados: la fila que está exactamente en el inicio entra, la que está exactamente
en el final no, que es lo que hace que slots consecutivos no dejen huecos ni se
solapen.

Un incremental con `end_value` **no toca el estado persistido**. Esa
es la parte que importa: volver a ejecutar un slot es idempotente, y un backfill de
marzo no mueve el cursor del que depende el run nocturno.

Dos cosas que saber antes de que funcione:

- **El cursor debe ser un datetime, no un string.** dlt no convierte un cursor
  `text` para compararlo y se niega a unirse al scheduler, con un
  `JoinSchedulerError` que no lo dice de forma evidente. Hay que convertirlo en el
  recurso (`add_map`) o tipar el `initial_value` como arriba.
- **Un workflow sin programación no entrega nada**, y dlt entonces lanza
  `ExternalSchedulerNotAvailable` en lugar de volver en silencio a su
  cursor. Si un pipeline debe funcionar de ambas formas, se deja
  `allow_external_schedulers` desactivado y se pasa la ventana manualmente desde
  `run.window()`.

Declarar `tools: [dlt]` en el step es aparte, y solo dibuja el chip en el
grafo: la ventana se entrega de todos modos.

## El snapshot

Los parámetros se almacenan **en el run**, no se leen del workflow en el momento de la
ejecución. El run lleva la entrada con la que se disparó, y el log de una
ejecución de enero sigue mostrando los valores de enero incluso después de que cambie el valor por defecto.

## Siguientes pasos

- [Configuración](/docs/configuration/) — variables de entorno del proceso
- [SDK](/docs/sdk/) — cómo un fetcher lee el contexto del run
- [Contexto entre steps](/docs/context/) — lo que un step publica para el siguiente
