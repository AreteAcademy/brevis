---
title: Python
description: pip install brevis — contexto entre steps, el reloj del run y las métricas propias de tu pipeline, sin ninguna dependencia.
group: SDK y bibliotecas
order: 14
slug: python
---

```bash
pip install brevis
```

```python
from brevis import context, metrics, run

bucket = context.get("extract.bucket")
since, until = run.window()          # the window this run covers

context.set(rows=48213, watermark="2026-09-07T03:00:00Z")
metrics.set("rows_loaded", 48213)    # reaches the engine's /metrics
```

**Sin dependencias, nunca.** Lee una variable de entorno, escribe un archivo
e imprime una línea en stdout. Requiere Python 3.9+.

No es un port del SDK de Go: sin drivers, sin paginación, sin ids de ingesta. Existe
para que un equipo pueda usar pandas, Polars o dbt y aun así obtener aquello para lo que sirve un
orquestador.

## Leer el contexto

Una clave siempre está calificada por el step que la publicó:

```python
context.get("extract.bucket")            # the value
context.get("extract.nope", default=0)   # absent is not an error
context.of("extract")                    # everything that step published
context.published()                      # what THIS step has published so far
```

`context.get("bucket")` sin step se **rechaza**, nombrando los steps que
la publicaron. Los steps están aislados —`extract` y `transform` pueden publicar ambos
`bucket` sin que ninguno pierda el suyo— y una clave sin calificar lo tira por la borda en cuanto
dos de ellos lo hacen. Buscar y elegir uno es precedencia por accidente: funciona
hasta que deja de funcionar en silencio.

Leer un step del que este no depende es un error, y el mensaje señala
la solución.

## Escribir

```python
context.set(name="Daniel")
context.set(label="Name")
# both survive: {"name": "Daniel", "label": "Name"}
```

Las llamadas **se combinan**. La misma clave dos veces conserva la última escritura; una clave distinta deja
la primera intacta. **No hay argumento de step**: el único espacio de nombres que un proceso
puede escribir es el suyo, lo que hace que el aislamiento sea estructural y no una
regla.

Nada llega al disco hasta que el proceso termina, así que un step que muere de golpe
no publica nada. Eso es correcto: la salida de un step que falló describía trabajo que
no terminó.

## El reloj que este run debe leer

Cada run también lleva **parámetros automáticos**: valores que el engine calcula por su
cuenta. Nadie los declara, todo run los tiene, y responden la pregunta que un
pipeline respondería de otro modo con `datetime.now()`.

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

:::warning Por qué no `datetime.now()`
`run.now()` es el **slot** en un run programado. Un fetcher que lee
`datetime.now()` y resta dos horas acierta en un run que arranca a tiempo
y se equivoca por cuarenta minutos en uno que la cola retrasó, y esos cuarenta minutos
no pertenecen a **ningún run**, porque el siguiente slot también lee su propio `now()`.
Nada falla, y el hueco se descubre semanas después.
:::

| | |
|---|---|
| `run.now()` | el reloj: el slot, o cuándo empezó el run |
| `run.window()` | `(start, end)`, con end excluido; o `None` |
| `run.auto()` | los parámetros automáticos, abajo |
| `run.context()` | id, intento, disparador, params y auto |
| `run.params()` · `run.param(name)` | los parámetros declarados en el workflow |
| `run.map_index()` · `run.map_value()` | la instancia, bajo `for_each:` |

`window()` viene del **cron**, no del historial: un backfill de un slot de
marzo produce la ventana que tuvo marzo. Pedir `[start, end)` nunca se solapa
y nunca deja huecos, por tarde que llegue el run y por muchas veces que reintente.

Devuelve `None` en lugar de un par de tiempos cero cuando el workflow no tiene
programación: una consulta desde la época selecciona todo, y ese fallo no
debe ser silencioso. El `or` de arriba es todo el fallback.

### Los parámetros automáticos

| campo | |
|---|---|
| `date` | `"2026-09-08"` — la partición, la carpeta, el `WHERE` |
| `adjusted_at` | el reloj que se lee en lugar de `now()` |
| `scheduled_at` | el slot que pidió el cron; `None` en un run manual |
| `started_at` | cuándo empezó **este intento**; un reintento lo mueve |
| `delay_seconds` | cuánto se retrasó este intento; nunca negativo |
| `interval_start` · `interval_end` | la ventana, con end excluido |
| `previous_error` | el run anterior a este no tuvo éxito |
| `previous_success_at` | el slot del último que **sí** lo tuvo, lo que hace que `previous_error` sea accionable |

Si se ejecuta el script a mano, `run.now()` **es** el reloj de pared y `window()` es
`None`, así que no hay ninguna rama que escribir para el desarrollo local.

## Una instancia de un step mapeado

Bajo [`for_each:`](/docs/workflows/), el engine ejecuta un proceso por elemento
y le dice a cada uno cuál le tocó:

```python
partition = run.map_value()
if partition is None:
    raise SystemExit("this step is meant to run under `for_each:`")

load(f"/data/{partition}.csv")
```

Un string JSON llega **sin sus comillas**: `for_each` sobre `["2026-01"]`
entrega al step `2026-01`, no `"2026-01"`. Cualquier otra cosa —un objeto, un número,
una lista— llega como su JSON, así que `json.loads(run.map_value())`.

Ambos son `None` en un step que no está mapeado. `None`, y no `-1` ni `""`: un
centinela es un número con el que alguien acaba haciendo aritmética, y un string vacío
no se distingue de un elemento que *es* el string vacío.

Un step mapeado **no publica contexto** aguas abajo: cuatro instancias no pueden compartir
una clave. Un step que necesita pasar algo escribe un archivo, o un step posterior
cuenta lo que aterrizó.

## Métricas que solo tu pipeline conoce

El engine ya mide lo que puede ver: duración, intentos, profundidad de la cola.
Lo que no tiene forma de saber es cuántas filas aterrizaron, cuántas rechazó
el proveedor o cuántos bytes se descartaron. Eso lo sabe el step, y la biblioteca
lo transporta.

```python
from brevis import metrics

metrics.set("rows_loaded", 48213)          # a gauge: the last value wins
metrics.inc("vendor_rejected_total")       # a counter: it adds up
metrics.inc("bytes_discarded_total", 4096)
```

Salen del `/metrics` **del engine**, en el puerto del scheduler, etiquetadas
con el workflow y el step:

```
brevis_step_rows_loaded{workflow="daily_sales",step="load"} 48213
brevis_step_vendor_rejected_total{workflow="daily_sales",step="load"} 5
```

| | |
|---|---|
| `metrics.set(name, value)` | un gauge. Tras terminar el step, el valor permanece hasta que el siguiente run escriba otro |
| `metrics.inc(name, by=1)` | un counter. Se acumula entre steps y entre runs |

### Por qué esto no abre un puerto

Porque **un step no se puede scrapear**. `:9090` pertenece al scheduler y a la
API: procesos de larga vida con una dirección estable, consultados cada quince a sesenta
segundos. Un step arranca en su propio pod, corre cuarenta segundos y termina: un puerto
que abriera nunca sería consultado, o lo sería una vez por suerte.

Las tres respuestas conocidas a eso son un Pushgateway (un componente que operar, y
series que permanecen hasta que alguien las borra), un colector OTLP (un componente
**y** una dependencia en esta biblioteca, que no tiene ninguna y no tendrá), o
dejar que las transporte el **orquestador**. Brevis ya lee el stdout de cada step
buscando líneas `@brevis:` —así es como las fases del SDK llegan al
grafo— y ya tiene un meter y un endpoint de Prometheus.

Así que la biblioteca escribe **una línea en stdout** y el engine hace el resto. La
métrica llega etiquetada con el workflow y el step gratis, porque el
engine sabe qué estaba ejecutando; mediante un Pushgateway esta biblioteca tendría
que etiquetarse a sí misma, y se equivocaría.

La escritura es `print(flush=True)`, porque el engine lee ese canal **en vivo**. Una
línea en buffer llega cuando el proceso termina, lo que para un step que corre una
hora es una hora tarde, y para un step que se mata, nunca.

### Lo que una métrica rechaza

**Un nombre inválido se rechaza, con estruendo.** Prometheus acepta letras, dígitos y
guion bajo, y el primer carácter no puede ser un dígito. Un nombre que rechaza no
produce una métrica rota: produce una exposición que **se niega a parsear**,
y se pierde el **scrape completo**, con todas las demás métricas. Así que `rows-loaded`
lanza `MetricError` aquí, donde se cometió el error, en lugar de llegar
bajo un nombre que nadie escribió.

| rechazado | porque |
|---|---|
| `rows-loaded`, `rows loaded`, `2rows` | Prometheus no lo parseará, y se pierde todo el scrape |
| `metrics.set("ok", True)` | `bool` es un `int` en Python, y reportar `1` es un número cuyo significado depende de saberlo. Hay que reportar el conteo o la duración, no el estado |
| `metrics.inc("x", -1)` | un counter solo sube. Todo `rate()` sobre él lo da por supuesto, y la rotura aparece en un gráfico semanas después |

Si se ejecuta el script a mano, la línea igualmente va a stdout, donde se lee como lo
que es. Nada la recolecta y nada falla, igual que `context.set()` en un portátil.

## Lo que rechaza

| | porque |
|---|---|
| más de 4096 bytes | la plataforma **trunca** en lugar de rechazar, así que un objeto demasiado grande llega cortado por la mitad y aguas abajo se lee como «no publicó nada» |
| un valor que no es JSON | un `datetime` se convierte en `"..."` o desaparece según quién serialice. El error nombra la clave y sugiere `.isoformat()` |
| una clave que no es string | `{1: "x"}` vuelve como `{"1": "x"}` y la búsqueda falla |

El techo es el número de la plataforma, no el nuestro. Toda funcionalidad de «contexto entre tareas»
falla de la misma manera: alguien pone **datos** donde solo cabe
**contexto**. Cuatro kilobytes caben una marca de agua, un conteo o una lista de particiones. Hay que publicar
una referencia y dejar las filas donde están.

:::danger Esto no es un almacén de secretos
Todo lo publicado es visible para cualquiera que pueda ver el run: está en el
estado del pod y en el historial del run. Hay que publicar una ruta, no una URL firmada.
:::

## Ejecutarlo a mano

Fuera de Brevis no hay entrada y nada lee la salida. `get()` devuelve
el valor por defecto, `set()` acepta y descarta, y la biblioteca lo dice una vez en INFO.
**No lanza excepción**: un script que no puede ejecutarse a mano no puede desarrollarse.
La validación sigue ejecutándose, así que un valor que funciona en tu portátil funciona en
producción.

## Probar tu step

Todo el contrato son dos variables de entorno, así que no hay nada que simular:

```python
def test_my_step(monkeypatch, tmp_path):
    monkeypatch.setenv("BREVIS_INPUT", '{"extract": {"rows": 42}}')
    monkeypatch.setenv("BREVIS_OUTPUT", str(tmp_path / "out"))
    assert context.get("extract.rows") == 42
```

## Versiones

La biblioteca se versiona por su cuenta, separada del engine y del SDK de Go.

:::warning No usar `0.2.0`
Se publicó en PyPI solo como wheel. `0.2.1` es el mismo código, completo.
:::

`brevis.run` necesita un engine en **`v0.9.0` o superior** para tener algo que leer;
en uno más antiguo todos los campos están vacíos y `run.now()` es el reloj de pared, lo
mismo que hace fuera del engine.

## Siguientes pasos

- [Bibliotecas cliente](/docs/libraries/) — el contrato y los otros lenguajes
- [Contexto entre steps](/docs/context/) — el concepto
- [Parámetros](/docs/parameters/) — los declarados
