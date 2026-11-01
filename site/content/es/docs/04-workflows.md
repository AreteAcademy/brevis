---
title: Workflows
description: El formato YAML completo: steps, dependencias, imágenes, recursos y programación.
group: Conceptos
order: 4
slug: workflows
---

Un workflow es un archivo YAML. Declara **qué ejecutar**, **en qué orden** y
**con qué runtime**, y nada más.

## Estructura mínima

```yaml
name: hello
steps:
  - id: only
    run: echo done
```

`name` identifica el workflow en la base de datos y en la interfaz. `steps` es
la lista de steps, cada uno con un `id` único y un `run`.

### `description` — para qué sirve

Opcional, de formato libre, y no lo lee nada del engine:

```yaml
name: daily-orders
description: >
  Pulls yesterday's orders from the vendor and lands them in bronze.
  Runs at 04:00 because the vendor closes its books at 03:30.
steps:
  - id: extract
    run: python fetch.py
```

Aparece en la parte superior de la pantalla del workflow, encima de los conteos y de la
programación. El grafo ya dice lo que un flujo **hace**; este es el único lugar
que dice por qué existe, que es lo que realmente necesita quien lo abre por primera vez
a las tres de la madrugada.

Un workflow sin descripción no muestra nada ahí; no un bloque vacío, que se lee como
algo que no terminó de cargar.

## Orden: chain o dag

```yaml
type: chain   # order is the file's order
type: dag     # order comes from depends_on
```

`chain` es azúcar sintáctico: el parser convierte la secuencia en aristas, y **el engine
solo conoce DAGs**. Se usa `dag` cuando el orden no es lineal:

```yaml
type: dag

steps:
  - id: prepare
    run: ./prepare.sh

  # Siblings: they depend on the same step, so they run in parallel.
  - id: extract
    run: ./extract.sh
    depends_on: [prepare]

  - id: validate
    run: ./validate.sh
    depends_on: [prepare]

  - id: publish
    run: ./publish.sh
    depends_on: [extract, validate]
```

El runner recorre el grafo **por niveles**: todo lo que está dentro de un nivel corre en
paralelo, y el siguiente nivel empieza solo cuando el anterior termina por completo.

:::warning Un fallo detiene todo el nivel
El runner se detiene en el **primer** fallo de un nivel, sin iniciar el siguiente.
Continuar tras un error produciría un resultado parcial que parece
completo.
:::

## Programación

```yaml
schedule: "0 5 * * *"   # five-field cron
concurrency: 1
```

Sin `schedule`, el workflow es manual: solo se ejecuta por disparo desde la
interfaz, con `brevis run` o con `backfill`.

`concurrency: 1` limita los runs simultáneos del mismo workflow: es lo que
evita que un `*/15` se solape consigo mismo cuando un run pasa de quince minutos.

Dos workflows **distintos** que escriben las mismas tablas necesitan un **grupo**. Uno
diario y uno `*/15` que construyen los mismos modelos de dbt declaran cada uno:

```yaml
concurrency:
  group: jaffle_models   # the shared resource, named the same in both files
  limit: 1               # optional; a group without one means 1
```

Los runs de todos los workflows del grupo cuentan contra un único límite, sin importar quién los
inició: la programación, un landing, un backfill o un clic. Un run que encuentra el
grupo lleno **espera en la cola**; no se omite ni falla. Un tercer
workflow que escribe las mismas tablas se une nombrando el grupo. Mientras
espera, la página del run lo indica: qué grupo, su límite y qué runs ocupan los
slots.

- Todos los miembros declaran el **mismo límite**. `validate` y `publish` rechazan un
  grupo declarado con 1 en un archivo y 3 en otro, incluso frente a miembros
  que ya están publicados.
- Un grupo serializa **runs de Brevis**. Un `dbt run` que alguien inicia en su propia
  máquina no está en esta cola, y falla de la misma manera.

## Imagen y recursos

```yaml
image: us-central1-docker.pkg.dev/example/apps/dbt:1.10.3
resources:
  cpu: 200m
  memory: 1Gi
  limits: {memory: 2Gi}
```

Declarados en el nivel superior, son el **valor por defecto de todos los steps**. Cada step puede
sobrescribir el suyo:

```yaml
steps:
  - id: bronze
    run: dbt build --select bronze+          # inherits the top-level image

  - id: notify
    image: ghcr.io/example/notify:0.3        # another runtime
    shell: false                             # distroless has no shell
    run: /notify --channel data
    resources: {cpu: 25m, memory: 32Mi, limits: {memory: 64Mi}}
    depends_on: [bronze]
```

Esto es lo que hace que un fetcher en Go cueste 12 MB y 32Mi junto a un
`dbt build` de 1,9 GB, en lugar de que ambos paguen el tamaño del mayor. Ver
[Pod por step](/docs/pod-per-step/).

## Campos de un step

| campo | | |
|---|---|---|
| `id` | **obligatorio** | único en el workflow; el nombre que se muestra en el grafo y en los logs |
| `run` | | el comando |
| `image` | | la imagen del step; sin ella, hereda la del nivel superior |
| `host` | | envía el comando a una máquina que el engine **no** gestiona; una alternativa a `image`, nunca un complemento. Ver [`host:` en la guía de Kubernetes](https://github.com/AreteAcademy/brevis/blob/master/docs/KUBERNETES.md#a-third-place-a-step-can-run-host) |
| `shell` | `true` | `false` ejecuta sin shell; necesario en distroless |
| `depends_on` | | lista de `id`s que deben terminar antes; una entrada puede ser `{step, label}` |
| `marker` | `false` | un step sin comando, para un `start` o un `end` |
| `resources` | | `cpu`, `memory` y `limits` para ese step |
| `runtime`, `tools` | | el lenguaje y las herramientas del step; ver [Runtime del step](/docs/runtime/) |
| `when` | `all_success` | bajo qué estado de sus dependencias se ejecuta este step; ver más abajo |
| `unless_empty` | | una clave de contexto que decide si hay algo que hacer; ver más abajo |
| `for_each` | | una clave de contexto con una lista; el step se ejecuta una vez por elemento; ver más abajo |
| `group` | | dibuja este step dentro de una caja con nombre y plegable; ver más abajo |
| `uses` | | otro workflow cuyos steps ocupan el lugar de este; ver más abajo |
| `on_error` | | anuncia los fallos de este step; ver más abajo |

## Ejecutar un step solo cuando hay algo que hacer

```python
# in extract
context.set(has_rows=len(rows) > 0)
```

```yaml
  - id: transform
    run: ./transform.sh
    depends_on: [extract]
    unless_empty: extract.has_rows
```

`unless_empty` nombra una **clave**, no una expresión. El step decide y
publica la respuesta; el engine lee una clave y pregunta si está vacía.

| cuenta como vacío | cuenta como presente |
|---|---|
| `false`, `0`, `""`, `null`, `[]`, `{}` | todo lo demás |

`"false"` como **string** está presente. Un step que publicó esos cinco
caracteres publicó algo, y adivinar que quería decir un booleano es como una
regla empieza a tener opiniones que su autor no puede ver. Hay que publicar un booleano real.

### Por qué no una expresión

El diseño tentador es `when: "{{ context.extract.rows > 0 }}"`. Un mini
lenguaje de expresiones es un compromiso enorme: un parser, un sistema de tipos que diga qué significa
`>` sobre un `any` de JSON, un problema de seguridad porque la expresión sale
de un YAML que escribió otra persona, y mensajes de error que apuntan dentro de un string.
Todo orquestador que tiene uno tiene un bug tracker lleno de eso.

Aquí la decisión vive en el lenguaje que su autor ya escribe, donde su
propio framework de pruebas puede alcanzarla.

### Una clave ausente es un fallo, no un valor vacío

Si la clave no está, el step **falla** y el mensaje la nombra junto a
lo que el publicador realmente escribió:

```
step "transform" reads `extract.has_row`, and "extract" published "has_rows" but not "has_row"
```

La alternativa, leer una clave ausente como «vacío, así que omitir», convierte un error tipográfico en
un step que deja de ejecutarse en silencio y para siempre, sin que nada indique
por qué. Ese es el peor resultado que puede tener esta funcionalidad.

La clave siempre se califica con el step que la publica, y ese step tiene que
ser uno del que este depende. Ambas condiciones se rechazan en el **publish**: un step condicionado por
una clave que nunca puede ver se omitiría para siempre, y descubrirlo cuando
un proceso nocturno deja de ejecutarse es demasiado tarde.

## Ejecutar un step una vez por elemento

```python
# in extract
context.set(partitions=["2026-01", "2026-02", "2026-03"])
```

```yaml
  - id: load
    run: ./load.sh "$BREVIS_MAP_VALUE"
    depends_on: [extract]
    for_each: extract.partitions
```

El step se ejecuta una vez por elemento, cada uno con su propia fila, reintento y código de salida.
El grafo muestra **un nodo con `[3]`**, no tres nodos.

| variable | |
|---|---|
| `BREVIS_MAP_INDEX` | `0`, `1`, `2` … |
| `BREVIS_MAP_VALUE` | el elemento |

Un string JSON llega **sin sus comillas**, así que `for_each` sobre `["2026-01"]`
entrega a una shell `2026-01` y no `"2026-01"`. Cualquier otra cosa (un número, un
objeto, una lista) llega como su JSON.

Ninguna de las dos variables existe en un step sin mapear. Una que siempre está y siempre
vacía enseña a quien lee el entorno a ignorarla.

### La forma del DAG no cambia

Un step mapeado sigue siendo un nodo con un único conjunto de aristas. Solo varía la cantidad de filas
debajo de él, así que el layout es el de siempre y el `[3]` se cuenta
a partir de esas filas en el momento de la lectura; no se almacena nada, así que nada tiene que mantenerse
sincronizado y un conteo erróneo no puede sobrevivir a una consulta corregida.

Mientras hay instancias en curso, la tarjeta muestra `[2/3]`.

### Una instancia fallida hace fallar el step

Tres particiones cargando y una que no es un step que no cumplió su trabajo, y
los steps de abajo lo ven. Un nodo que se pone verde porque la mayor parte funcionó es
una insignia que miente.

**Un reintento rehace solo las instancias que fallaron.** Dieciocho particiones que
funcionaron no se recargan porque dos fallaron.

### Una lista vacía es `skipped`, no éxito

Un step que no hizo nada porque no había nada que hacer no tuvo éxito en
hacerlo. Un nodo verde sobre cero instancias es exactamente el tipo de cosa sobre la que alguien
construye un dashboard.

Un valor que **no es una lista** falla, y también una clave que no existe: la
misma política que tiene `unless_empty:`, y por la misma razón: un error tipográfico que silenciosamente
produjera cero instancias deshabilitaría el step para siempre.

### El fan-out está acotado, y el límite es heredado

La lista viaja en el contexto, y el contexto tiene un techo de 4096 bytes que
viene del kubelet. Un workflow no puede pedir diez mil pods sin
antes encontrar la forma de decirlo en cuatro kilobytes. Es un límite que vale la pena conservar
en lugar de sortear.

### Qué publica un step mapeado

Su salida se registra en la fila de cada instancia y **no** es visible para los
steps de abajo. Cuatro instancias publicando bajo el nombre de un step son cuatro valores para
una clave, y no hay respuesta a `context.String("load.bucket")` que no sea
una suposición. El step lo dice en su log en lugar de descartarlo en silencio.

## Agrupar steps en el grafo

```yaml
steps:
  - id: extract_orders
    group: sales_data_reporting
    run: ./extract.sh
  - id: load_orders
    group: sales_data_reporting
    depends_on: [extract_orders]
    run: ./load.sh
```

Los steps se dibujan dentro de una caja con nombre que se pliega: el TaskGroup de Airflow,
para el caso en que un DAG crece lo suficiente como para dejar de ser legible.

**Solo visual.** Los TaskGroups de Airflow también *prefijan* los ids que contienen, de modo que
`extract` se convierte en `sales.extract`. Esto no: prefijar cambiaría todos los
`depends_on`, todas las claves de contexto y todas las filas registradas de un workflow
existente, para una funcionalidad cuyo único valor es que un grafo grande se lee mejor. Los
espacios de nombres pueden añadirse después; es una adición estricta a esto.

Un grupo es una **etiqueta**, no un contenedor. Los steps conservan sus ids globales, un grupo
puede abarcar niveles, y nada de la ejecución cambia.

Al hacer clic en el nombre del grupo se pliega: sus steps desaparecen y las flechas que
cruzaban el límite apuntan a la caja.

## Reutilizar otro workflow

```yaml
# nightly.yaml
steps:
  - id: prepare
    run: ./prepare.sh

  - id: mlops
    uses: ml_training       # another workflow in the same publish
    depends_on: [prepare]

  - id: report
    run: ./report.sh
    depends_on: [mlops]
```

En el **publish**, los steps de `ml_training` ocupan el lugar de ese step, con el prefijo de su
id, y el nodo `uses` desaparece:

```
prepare → mlops.train → mlops.evaluate → report
```

Llegan como un único grupo plegable, de modo que el grafo muestra lo que decía el archivo.

**Se expande en el publish, no se ejecuta en tiempo de ejecución.** Un step que disparara un
*run* hijo y esperara es el diseño que causó deadlocks en Airflow, y este engine tiene el
mismo ingrediente: el techo de pods es un semáforo por proceso, así que un padre que retiene
un slot mientras espera a un hijo que necesita slots del mismo pool se cuelga, y
solo bajo carga, es decir, en producción. Un run, un grafo, un pool.

A cambio se renuncia a un run hijo con su propio id y su propio historial.

### Las reglas

| | |
|---|---|
| el hijo debe estar en el **mismo publish** | no basta con que ya esté publicado |
| flechas | las que entran al step pasan a entrar en cada una de las **raíces** del hijo; las que salen de él, salen de cada una de sus **hojas** |
| el `image`, `env`, `secrets`, `resources` del hijo | se materializan en cada step, de modo que corre con lo que decía su propio archivo |
| las claves `unless_empty` / `for_each` del hijo | se mueven con el prefijo |
| anidamiento | aplanado, primero en profundidad |
| un ciclo | se rechaza en el publish, nombrando la cadena |

**Mismo publish, y no «ya publicado», es lo importante.** Una expansión
que leyera la base de datos haría que `brevis validate` —que no toca ninguna base de datos,
a propósito— respondiera una pregunta distinta de la de `brevis publish`, y el archivo
que pasó el CI sería el que falla el despliegue.

El hijo sigue siendo publicable por sí solo: la expansión **copia**, no
consume. Un step no puede a la vez `uses:` y declarar `run:`, `action:` o `marker:`;
eso es un archivo que dice dos cosas.

## Decir qué significa una flecha

Una dependencia puede llevar una etiqueta, que se muestra en la flecha del grafo.

```yaml
steps:
  - id: determine_load_type
    run: ./decide.sh

  - id: load_full
    run: ./full.sh
    depends_on:
      - {step: determine_load_type, label: additional data}

  - id: load_delta
    run: ./delta.sh
    depends_on:
      - step: determine_load_type
        label: changed existing data
```

La forma simple, `depends_on: [extract]`, sigue funcionando y sigue siendo la
normal. La mayoría de las dependencias no tienen nada que decir, y una etiqueta en cada flecha es ruido.

Las etiquetas se ganan su lugar en una **bifurcación**: dos flechas que salen del mismo step
sin nada escrito es un diagrama que obliga a abrir el código fuente para
leerlo, que es justo lo que un grafo existe para evitar.

## Un step que no hace nada

```yaml
steps:
  - id: start
    marker: true

  - id: end
    marker: true
    depends_on: [load_full, load_delta, report]
```

`marker: true` es un step sin comando. No ejecuta nada, termina con éxito al instante
y aparece en el grafo.

No es decoración. Un `end` que depende de cada rama convierte *¿terminó todo?* en un solo nodo en lugar de seis flechas que seguir; y se comporta
como cualquier otro step, así que un `end` bajo una rama fallida se **omite** (`skipped`), no
se pone en verde.

**Un step sin `run:` y sin `marker: true` se sigue rechazando.** Los dos casos
no deben fundirse en uno: un comando vacío casi siempre es un error, y
`marker: true` es la forma de decir que fue intencional. Un marker que además declara
`run:` también se rechaza; eso es un archivo que dice dos cosas.

## Ejecutar un step solo cuando algo falló

Por defecto un step se ejecuta cuando todo lo anterior funcionó. `when:` cambia eso.

```yaml
steps:
  - id: extract
    run: python fetch.py

  - id: notify_failure
    run: ./notify.sh
    depends_on: [extract]
    when: any_failed          # runs precisely when extract did not make it

  - id: cleanup
    run: ./cleanup.sh
    depends_on: [extract, transform]
    when: all_done            # runs either way
```

| regla | |
|---|---|
| `all_success` | el valor por defecto. Todo aquello de lo que depende este step tuvo éxito: sus **propias** dependencias, como en Airflow |
| `any_failed` | al menos un step del que este depende falló |
| `all_done` | todo aquello de lo que depende este step terminó, sin importar cómo |

Una regla desconocida se rechaza en el publish, nombrando las válidas. Tiene que ser así: un
`when: on_failure` leído como «el valor por defecto» se ejecutaría en caso de **éxito**, lo contrario
de lo que dice, descubierto la noche en que importaba.

### Un step que no se ejecuta queda `skipped`

Es un estado propio, dibujado en su propio color, y lleva el motivo:
*`extract` fue failed*. Antes de esto, un step bajo un fallo no tenía ningún registro
y la pantalla lo mostraba pendiente para siempre.

`skipped` no es un fallo ni un éxito. Un step cuyo upstream se rompió no
falló: nunca tuvo la oportunidad, y llamarlo éxito es una mentira que
llega al propio estado del run.

Una regla de disparo decide qué steps **se ejecutan**. No decide el resultado del run:
un `notify_failure` que entregó su mensaje no significa que el pipeline
funcionó, y el run sigue fallido.

### `all_success` es local, como en Airflow

La regla pregunta por las dependencias **propias** de un step y por nada más. Un fallo en
una rama no relacionada no detiene a esta:

```
extract_orders ──✕                 (failed)
extract_users  ──✓── transform_users ──✓     keeps going
```

Una rama **debajo** del fallo sigue deteniéndose: que `all_success` sea local no
significa que esté ausente.

Este engine solía abortar todo el grafo en el primer fallo. Eso tenía una
razón: continuar tras un error producía un resultado parcial que parecía
completo, y un pipeline corrió 28 días con retraso sin que nadie lo notara.

**Lo que reemplaza esa protección**, y no es poco:

- el run sigue **fallando**, con el mismo error;
- el grafo muestra el step fallido en rojo y cada uno de los omitidos en su propio
  color, cada uno indicando qué step lo detuvo;
- la alerta sigue saliendo cuando el run se rinde.

Un resultado parcial ya no parece completo, porque el run dice que no lo es.

**A lo que se renuncia**, dicho con claridad: una rama no relacionada ahora escribe sus datos en
un run que falló en otra parte.

### Un reintento reejecuta solo lo que falló

El reintento de un run no rehace todo el grafo. Un step que ya tuvo éxito en un
intento anterior del **mismo run** conserva su resultado y no se vuelve a ejecutar,
como se comporta un run de DAG limpiado en Airflow.

```
attempt 1   extract ✓    load ✕    report (skipped)
attempt 2   extract –    load ✓    report ✓            extract is not re-run
```

Conserva su fila original: su duración, su log y lo que publicó son
los del primer intento, porque fue entonces cuando ocurrió el trabajo. **El step de abajo
sigue leyendo lo que publicó**: el contexto del run se almacena, no se reconstruye.

La distinción que importa: esto trata de los intentos anteriores de **este run**.
Un step que tuvo éxito en el run de *ayer* se ejecuta con normalidad hoy. (Esa otra
pregunta también existe, y es la que le dice al SDK si es la primera vez de un step;
ver `BREVIS_RUN_FIRST`.)

Un step que fue **omitido** (`skipped`) no está resuelto: nunca se ejecutó, y el reintento decide
sobre él de nuevo con los resultados del nuevo intento.

## Anunciar los fallos de un step

Todo fallo ya se anuncia: con un webhook configurado, un run que se rinde
envía un mensaje, sin repetir ningún bloque en ningún archivo. `on_error` es para el
step que necesita el suyo.

```yaml
steps:
  - id: fetch_observations
    run: python fetch.py
    on_error:
      type: SLACK
```

| campo | | |
|---|---|---|
| `type` | **obligatorio** | `SLACK`. Un valor desconocido se rechaza en el publish, nombrando los válidos |
| `when` | `give_up` | `attempt` anuncia cada intento fallido, no solo el último |

Llegan dos mensajes en lugar de uno, y ese es el punto: la alerta a nivel de run
dice *`id_verification` falló*, que es lo que necesita quien es dueño del pipeline;
la del step dice *`fetch_observations` falló*, que es lo que necesita quien
es dueño de esa integración.

**No hay un campo `webhook` ni `url`, y no lo habrá.** El destino
es una credencial —quien la tenga publica en el canal como si fuera la
plataforma— y un archivo de workflow lo escribe alguien que no necesariamente
tiene permiso para elegir adónde van las alertas de la empresa. El archivo dice **si** y
**cómo**; la instalación dice **dónde**, mediante `BREVIS_SLACK_WEBHOOK`.

**`when: give_up` es el valor por defecto por la noche de quién se trata.** Un step que
falla dos veces y pasa al tercer intento enviaría dos mensajes con el otro
valor, y el segundo llegaría cuando el problema ya no existe.

**Un step que no falló nunca se anuncia.** En un workflow donde una rama
se rompe, no se despierta al dueño de la otra rama.

La entrega es trabajo de `brevis alert`: la alerta se registra en la misma transacción
que el fallo, así que una caída de Slack la retrasa en lugar de perderla.

## Tags

```yaml
tags: [analytics, dbt, daily]
```

Sirven para filtrar en la interfaz. No afectan la ejecución.

## Validación

La validación no necesita base de datos, así que corre en CI junto a las pruebas:

```bash
brevis validate workflows/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencias  cron 0 2 * * *
```

Acepta un archivo o un directorio. En un directorio busca `*.y*ml` y ordena;
dos ejecuciones producen el mismo log, y la diferencia entre dos despliegues no
se vuelve ruido.

## Publicación

```bash
brevis publish workflows/
brevis publish workflows/ --project acme --prune
```

`--prune` elimina del proyecto los workflows ausentes de la lista publicada,
conservando su historial. **No** es el valor por defecto, por una razón práctica:
`publish one-file.yaml` no debe borrar los otros cuarenta y ocho solo porque
no se nombraron en la línea de comandos.

## Siguientes pasos

- [Parámetros](/docs/parameters/) — lo que cambia entre dos runs
- [Scheduler y cola](/docs/scheduler-and-queue/) — cómo un workflow se convierte en ejecución
- [Contexto entre steps](/docs/context/) — lo que un step le dice al siguiente
