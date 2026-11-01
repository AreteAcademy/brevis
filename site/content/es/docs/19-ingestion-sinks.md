---
title: Sinks de ingesta
description: Los seis destinos, cómo escribe un registro cada base de datos y por qué la imagen lleva solo lo que se usa.
group: Ingesta
order: 19
slug: ingestion-sinks
---

Seis destinos, y la lista es explícita a propósito: un conector que está
*planificado* y uno que *funciona* son cosas distintas para quien está eligiendo
esto.

| sink | estado | qué es una escritura | probado contra |
|---|---|---|---|
| `pubsub` | **funciona** | una publicación por lote, atributos y clave de orden a partir de los campos del propio evento | el emulador |
| `postgres` | **funciona** | `COPY FROM STDIN`, o un `INSERT` por etapas en una transacción | un Postgres real |
| `mysql` | **funciona** | `INSERT` de varias filas en una transacción, o `INSERT IGNORE` | un MySQL real |
| `files` | **funciona** | objetos NDJSON en un directorio, `gs://` o `s3://` | el sistema de archivos, y S3 |
| `bigquery` | **funciona** | filas cargadas, o preparadas y combinadas con `MERGE` por `ingestion_id` | **solo configuración: no existe emulador** |
| `redshift` | **funciona** | el lote a S3, luego `COPY`, luego `MERGE` | **solo configuración: no existe emulador** |

**Dos de los seis no están probados de punta a punta**, y la tabla lo dice en lugar de
dejar que la palabra «funciona» respalde una afirmación que nadie comprobó. BigQuery y Redshift no
tienen emulador local, así que lo que se prueba es la configuración, los rechazos y el driver
subyacente, que la propia suite del SDK ya cubre. El primer uso en producción de
cualquiera de los dos es la prueba real, y debería ser un stream de bajo riesgo.

Un `type:` desconocido se **rechaza al arrancar**, nombrando lo que lleva *este binario*.
Un gateway que arranca con un sink que no tiene es uno que descarta eventos por
una razón que nadie puede ver.

`gs://` y `s3://` no son conectores aparte: `files` lee las tres formas de
ruta, así que el dead letter es una carpeta en un portátil y un bucket en producción
sin ningún cambio aquí.

## Cómo escribe cada uno

`write` es **obligatorio y nunca tiene valor por defecto** en los cuatro destinos que escriben
en una tabla, porque los dos modos son genuinamente distintos y solo el dueño de la tabla
sabe cuál es:

| | `append` | `merge` |
|---|---|---|
| **postgres** | `COPY FROM STDIN` | `INSERT … ON CONFLICT (ingestion_id) DO NOTHING` |
| **mysql** | `INSERT` de varias filas por bloque | `INSERT IGNORE` |
| **bigquery** | filas cargadas (a través de GCS por encima del límite inline) | `MERGE … WHEN NOT MATCHED THEN INSERT` |
| **redshift** | `COPY` desde S3 | `COPY` a una tabla de staging, luego `MERGE` |

`merge` significa **lo mismo en los cuatro**: el insert idempotente, y **gana la
primera entrega**. Una reentrega se ignora, no se aplica, lo cual es correcto
para un evento, que ocurrió una vez y no cambia, e incorrecto para una fila
que lleva un estado mutable.

Postgres y MySQL **requieren un índice `UNIQUE` sobre `ingestion_id`** para `merge`,
y rechazan sin él en lugar de añadir filas en silencio:

```sql
CREATE UNIQUE INDEX CONCURRENTLY ON landing.orders (ingestion_id);
```

Sin el índice `ON CONFLICT` no tiene con qué coincidir, y cada reentrega
duplicaría en una tabla cuyo dueño pidió lo contrario. El rechazo no es un
lote perdido: se reintenta, y luego se entierra en el dead letter con el motivo y
el `CREATE INDEX` en el registro; nadie pierde eventos mientras alguien crea
el índice.

**`upsert` se rechaza por su nombre.** `ON CONFLICT DO UPDATE`, donde gana la
*última* entrega, es un modo real que **no está implementado**. Aceptar la palabra
mientras se comporta como `merge` es exactamente el fallo que ese campo existe para evitar,
así que la configuración lo dice.

## Cada uno, con lo que es cierto solo de él

```yaml
# Postgres: schema-qualified name, and the table has to exist.
sink: {type: postgres, dsn_from: BREVIS_DSN, table: landing.clicks, write: merge}

# MySQL: no COPY, so throughput is an order below Postgres on the same
# hardware. Flush wider here.
sink: {type: mysql, dsn_from: BREVIS_DSN, table: landing.clicks, write: merge}

# BigQuery: no dsn_from. It authenticates with the pod's own credentials, which
# is what a workload identity is for. The name has no dots — the project and
# the dataset are their own fields.
sink: {type: bigquery, project: acme-prod, dataset: landing, table: clicks, write: merge}

# Redshift: two hops, not one. It is columnar, a row-by-row INSERT pays the cost
# of a block, and the only workable load is COPY from S3. So every batch becomes
# an object in the staging prefix and then a COPY — a stream flushing every
# second writes 86,400 objects a day. Flush much wider here.
sink:
  type: redshift
  dsn_from: BREVIS_RS_DSN
  table: landing.clicks
  staging: s3://acme-staging/gateway/
  iam_role: arn:aws:iam::123456789012:role/redshift-copy
  write: merge
```

`dsn_from` nombra la **variable de entorno** que contiene la cadena de conexión,
nunca la cadena: un DSN lleva una contraseña y ese archivo está en git. Es la
misma división que hace `secrets:` en un workflow.

`iam_role` es un rol y el driver **no aceptará una clave**: una clave en la URL
de un `COPY` acaba en el log de consultas del clúster, que mucha gente lee.

## Sin declaración de columnas

El gateway no envía lista de columnas. Un pipeline declara la suya porque sus filas
tienen una forma que controla; el lote de un gateway es lo que *N* clientes enviaron en
una ventana de flush, y las formas pueden diferir.

El driver resuelve la lista a partir de **la propia tabla**, intersectada con lo que
lleva el lote, **antes** de tocar el servidor. Un campo que la tabla no tiene
se rechaza con el mensaje que lo soluciona, en lugar de fallar a mitad del `COPY`
con `column "x" of relation "y" does not exist`; un campo que un evento omite
se escribe como `NULL`.

## La lista de imports es la selección

Los sinks están **compilados dentro**, como los hooks, y el binario registra lo que
tiene:

```go
func main() {
    sinks := gateway.NewSinks()
    sinks.MustRegister(postgres.Sink, postgres.New)
    sinks.MustRegister(files.Sink, files.New)
    gateway.Main(nil, gateway.WithSinks(sinks))
}
```

El linker de Go poda lo que nada referencia, así que un binario que nunca importa el
driver de BigQuery no lleva BigQuery, ni Arrow, ni la Storage Write API.
Así es como siempre ha funcionado `database/sql`.

Vale lo que suena:

| build | paquetes | binario |
|---|---|---|
| los seis sinks, ambos object stores, ambos metastores | unos 900 | 55,0 MB |
| `postgres` + `files` local | unos 240 | **10,2 MB** |

Los object stores son un **segundo** registro, porque un esquema no es un destino:
`files` es un sink que escribe en un directorio, en `gs://` y en `s3://`, y un
build que solo escribe localmente no debería llevar el SDK de AWS para hacerlo.

### Dos imágenes publicadas

| imagen | lleva | descarga (amd64) |
|---|---|---|
| `areteacademy/brevis-gateway:0.32.0` | seis sinks, S3, GCS, Redis, memcached | 19,1 MB |
| `areteacademy/brevis-gateway:0.32.0-slim` | `postgres`, `files` local, `auto_table` | **5,2 MB** |

Ambas de un mismo build de un mismo árbol, así que los dos tags son siempre el mismo commit.
No hay `latest-slim`: `latest` ya es un tag que nadie debería desplegar.

**¿Se quiere otro par?** `cmd/gateway-slim` son quince líneas y el bloque de imports
es toda su configuración. Se copia, se cambian los imports, se compila.
Quien quiere un hook ya está compilando su propio binario, así que elegir los
sinks no le cuesta nada más.

### El rechazo dice qué build se tiene entre manos

```
sink type "bigquery" is not one this binary carries (it has: files, postgres).
Sinks are compiled in, so this is a build that left it out rather than a
destination that does not exist.
```

Una lista fija enviaría a esa persona a buscar un error de configuración que no
cometió. Lo mismo vale para un object store: un dead letter `s3://` en un binario
sin backend de S3 se rechaza **al arrancar**, nombrando el esquema, en lugar de
en el primer lote que tuviera que enterrar.

## Una ruta, N tablas, nada declarado

```yaml
sink:
  type: auto_table
  shape: columns
  naming: {pattern: '^(app|svc)_[a-z0-9_]{2,40}$', allow: [app_, svc_], max_new_per_hour: 20}
  metastore: {type: redis, addr_from: BREVIS_METASTORE_ADDR, ttl: 60s}
  into: {type: bigquery, project: acme-prod, dataset: landing, write: merge}
```

`auto_table` **enruta y no escribe**: `into` es lo que escribe, y no
lleva `table`: la tabla viene de cada evento. Un `into` con `table`
se rechaza al arrancar, porque una tabla fija bajo un enrutador ganaría en silencio
y todos los eventos aterrizarían en ella.

### El envelope

El cuerpo es un **envelope**: campos de control arriba, el registro dentro de
`data`.

```json
{
  "table_name": "app_orders",
  "description": "App orders",
  "operation": "INSERT",
  "unique_key": "id",
  "data": {
    "id": "A-1",
    "total": 150,
    "customer": {"id": 7, "uf": "SP"},
    "items": [{"sku": "X", "qty": 2}]
  }
}
```

| campo | | qué es |
|---|---|---|
| `table_name` | **obligatorio** | dónde aterriza esto |
| `data` | **obligatorio** | el registro, y solo él |
| `unique_key` | por defecto `id` | qué campo de `data` identifica el registro — **obligatorio bajo `merge`** |
| `operation` | por defecto `INSERT` | `INSERT`, `UPDATE` o `DELETE` |
| `description` | opcional | **aceptado y todavía sin usar**; ver más abajo |

`table_name` al lado de `total` y `customer` era un campo del
**transporte** haciéndose pasar por un campo del **registro**. Separarlos es como
tiene forma todo formato CDC, y es lo que hace posible reservar un prefijo,
porque ahora hay un solo lugar donde escribe únicamente el productor.

### La clave pertenece a `merge`, no al envelope

`unique_key` es opcional, y que sea **obligatoria** depende del modo de
escritura, porque los dos modos quieren cosas distintas:

| | |
|---|---|
| `write: merge` | **obligatoria.** El modo existe para mantener una fila por registro, y `brevis_record_key` es lo que significa «por registro»: el `qualify` que resuelve la versión vigente particiona por ella. Una tabla merge llena de filas que no nombran ningún registro es una tabla que nadie puede resolver. |
| `write: append` | **opcional.** Una tabla append es un log, y una entrada de log no tiene por qué ser *sobre* un registro: una línea de auditoría, un webhook, una muestra de métrica. Exigir un `id` ahí haría que los productores inventaran uno, lo cual es peor que `NULL` porque parece real. |

Bajo `append` la columna se crea nullable y una fila sin clave lleva `NULL`,
no `""`, que es un hecho distinto. Y `brevis_ingestion_id` sigue existiendo:
sin clave, **el contenido es la clave**, así que el mismo documento dos veces es el mismo
id.

**Nombrar un campo que no está en `data` es un error en ambos modos.** «No lo
dijiste» y «dijiste algo que no está» son errores distintos, y solo el
primero pasa:

```json
{"accepted":0,"rejected":["event 0: \"unique_key\" names \"pedido_id\" as this
 record's identity and \"data\".\"pedido_id\" is missing or empty. Drop
 \"unique_key\" to fall back to \"id\", or send the field"]}
```

**`description` se lee y hoy no va a ninguna parte.** Está en el contrato para la
página de ingesta de la console, que no se ha escrito. Se dice aquí porque un
campo aceptado en silencio es un campo que alguien cree que se está almacenando.

### Ocho columnas fijas, más lo que lleve el registro

| columna | | |
|---|---|---|
| `brevis_ingestion_id` | `STRING` | la identidad, y la clave de merge |
| `brevis_record_key` | `STRING` | `data[unique_key]` — sobre qué **registro** trata esto; `NULL` bajo `append` |
| `brevis_operation` | `STRING` | `INSERT`, `UPDATE` o `DELETE` |
| `brevis_received_at` | `TIMESTAMP` | la llegada, según **nuestro** reloj — la columna de partición |
| `brevis_loaded_at` | `TIMESTAMP` | la escritura, sellada por el **destino** |
| `brevis_stream` | `STRING` | qué ruta lo escribió |
| `brevis_gateway` | `STRING` | qué despliegue |
| `brevis_received_bytes` | `INT64` | cuánto pesó el evento al **llegar**, envelope incluido |

Estas ocho son **del SDK**, no de este sink: `sdk.LandingSchema` las declara
y `sdk.LandingID` acuña la identidad, de modo que un pipeline puede aterrizar la misma
tabla con los mismos ids. Una sola definición, porque dos divergirían. Ver
[el layout de aterrizaje del SDK](/docs/sdk/#the-landing-layout).

**`brevis_` está reservado.** Un `data` que lleve cualquier clave con ese prefijo se rechaza
**por evento**; de lo contrario un productor falsifica un campo de control, y un
`brevis_received_at` falsificado es peor que ninguno porque parece real.

**`brevis_loaded_at` es un `DEFAULT` de la base de datos**, no un valor que envíe el gateway.
El gateway conoce la hora de **despacho**, el destino conoce la hora de **escritura**,
y la diferencia entre ambas columnas es la latencia real de extremo a extremo, por
fila, sin instrumentar nada.

**`brevis_received_at` es la columna de partición, nunca el reloj de un cliente**: el de un
productor puede estar mal o ausente, y una columna de partición que controla el cliente es
un cliente que puede escribir en 2035. Una tabla de landing sin particionar es una factura de
consultas que solo crece. El clustering es por `brevis_record_key`, porque leer el historial de
un registro es lo que cualquiera hace con una tabla como esta.

Una declaración sirve a todos los destinos, porque el generador de DDL del SDK
traduce cada tipo: `STRING` se convierte en `TEXT` en Postgres, `LONGTEXT` en MySQL
y `STRING` en BigQuery; `JSON` se convierte en `JSONB`, `JSON` y `JSON`.

### Volumen sin infraestructura para medir volumen

`brevis_received_bytes` es **gratis**: el gateway ya cuenta estos bytes al
decodificar para gobernar `buffer.flush.size`, y hasta ahora el número moría allí.
Medir solo el registro costaría un `json.Marshal` por evento —2,0 µs
frente a los 3,1 µs que ya gasta el parseo, **un 67% más en el camino caliente**— para
refinar un número cuyo trabajo es la tendencia y la atribución.

Responde lo que las métricas **estructuralmente no pueden**. Cada serie `brevis_gateway_*`
es por *stream* y no lleva etiqueta `table`, deliberadamente: con
`auto_table` una ruta se convierte en N tablas y esa etiqueta la elige el productor.
Así que «qué tabla está creciendo, y desde cuándo» no tenía respuesta en ningún lado.

```sql
select date(brevis_received_at) day,
       count(*) events,
       sum(brevis_received_bytes)/1e9 gb
from app_orders
group by 1 order by 1 desc
```

Sin la columna, la misma pregunta significa escanear toda la columna JSON
—`sum(length(to_json_string(data)))`—, aproximadamente **cincuenta veces los bytes escaneados**,
cada vez que alguien lo pregunta. La columna se paga sola en la primera consulta.

**Es ingress, no almacenamiento.** El destino guarda la fila tipada y
comprimida: 400 bytes de JSON pueden ser 80 en disco. Sumar esto da lo que
*llegó*, nunca lo que se factura por conservarlo.

Y **el productor no puede escribirla**. El gateway sella el valor después del
hook y sobrescribe lo que llevara el envelope: un cliente que enviara el campo
estaría informando su propio volumen, y sumarlo dejaría de significar
algo.

### Las series de volumen son opt-in

```bash
BREVIS_INGESTION_METRICS=true
```

```
brevis_gateway_ingested_bytes_total{stream,table}
brevis_gateway_ingested_events_total{stream,table}
```

**Desactivadas por defecto**, y el valor por defecto es el punto: `table` es una etiqueta que elige el
*productor*. Todo lo demás de este archivo se etiqueta por lo que un operador
escribió en el YAML, de modo que el número de series se conoce antes de que arranque el proceso.
Nadie debería encontrarse con una factura de métricas por haber actualizado.

Un valor que nadie puede parsear se lee como **desactivado**, nunca como un fallo: esto es
observabilidad, y un error tipográfico aquí no debe ser la razón por la que un endpoint de ingesta
no arranque.

La columna está ahí de todos modos. La métrica compra el *ahora*; la columna compra el *desde
cuándo* y el *por quién*.

### Dos formas, un contrato

`shape` decide qué aporta el **registro** a la fila. El contrato del productor
es el mismo en ambos casos: el envelope nunca cambia. Lo que cambia es
el aspecto de la tabla, que es decisión del operador y no del
productor.

```yaml
shape: document   # the default
shape: columns
```

Con **`document`**, `data` va completo a una columna `JSON`:

```
 brevis_ingestion_id | brevis_record_key | … | data
 d7179bfe-…          | A-1               |   | {"id": "A-1", "total": 150, "customer": {…}}
```

Nunca hay DDL después de la creación. Un campo nuevo es una clave nueva: sin
cuota de cambios de esquema, sin reapertura de write stream, sin carrera entre réplicas, y
se puede consultar el día en que llega. Es donde aterrizó el mercado: Fivetran,
Airbyte y Snowpipe aterrizan todos en una capa raw que no puede fallar y la modelan
aguas abajo.

Con **`columns`**, cada campo del registro obtiene una columna propia:

```
 id   | total | customer              | items
 A-1  | 150   | {"id": 7, "uf": "SP"} | [{"qty": 2, "sku": "X"}]
```

**Los escalares se convierten en `STRING`, los objetos y arreglos en `JSON`, y no hay
inferencia en ninguna parte.** `150` llegó como número y se convirtió en el string `150`.
Es una elección y no una limitación: un campo que llega entero hoy y
fraccionario mañana cambiaría el tipo de una columna sin que nadie escriba nada,
y la fila que ya no encaja va al dead letter. Tipar una es el camino de
**promoción**: se escribe en el YAML y se revisa en un diff.

**Un `null` no se convierte en ninguna columna.** No tiene forma, así que no hay nada que
leer: el campo aparece en la tabla desde el primer evento que lleva un
VALOR, con la forma de ese valor. El evento que envió `null` aterriza `NULL`
igualmente, que es lo que significaba; y donde la columna ya existe, porque
se declaró o la creó otro evento, el `null` se escribe en ella
en lugar de omitirse.

**Y una columna `JSON` recibe un VALOR JSON, nunca un string de uno.** Suena
a detalle y es la diferencia entre una tabla consultable y una
tabla que parece correcta:

```sql
SELECT JSON_TYPE(customer) FROM bronze.orders;     -- object
SELECT JSON_VALUE(customer, '$.uf') ...;           -- SP
```

Si `JSON_TYPE` responde `string` y `JSON_VALUE` responde NULL, el objeto se
codificó dos veces: la columna contiene el TEXTO del objeto y no el
objeto. Las versiones del gateway hasta la **0.15.0** inclusive hacían eso en BigQuery,
en **ambas** formas; ver el [changelog del gateway](https://github.com/AreteAcademy/brevis/blob/master/gateway/CHANGELOG.md)
para la comprobación de una línea y la reparación. Postgres y MySQL nunca se vieron afectados:
su servidor parsea el texto.

Un nombre de campo tiene que cumplir la regla de BigQuery, la más estrecha de las cuatro. Postgres
aceptaría casi cualquier cosa entrecomillada, y esa es la trampa: la tabla se crea allí
y se rompe el día en que alguien apunta un stream a BigQuery.

Ambas formas tienen un equivalente en el SDK, y es el mismo código y no uno
equivalente: `sdk.Landing(table, sdk.LandingKey("id"))` aterriza `document`, y
añadir [`sdk.LandingColumns()`](/docs/sdk/#two-shapes-the-record-whole-or-one-column-per-field)
aterriza `columns`. Mismas columnas, mismos valores, mismo `brevis_ingestion_id`, de modo que un
stream puede pasar entre un gateway y un pipeline sin una migración, y los dos
se pueden leer como una sola tabla.

El pipeline puede hacer crecer la misma columna del mismo modo: definir
[`Evolve: sdk.EvolveAdditiveFromPayload`](/docs/sdk/#when-a-field-the-table-does-not-have-shows-up)
en el destino y un campo que el lote lleve se convierte en columna, con esta
regla, mediante este código.


**El prefijo es configurable, y es una decisión para todo el proceso.**
`BREVIS_LANDING_PREFIX=acme` las convierte en `acme_ingestion_id` y las demás;
todos los streams de este gateway, así que dos sinks no pueden discrepar sobre una tabla en la
que ambos podrían escribir. La regla de reservados de arriba se mueve con él: bajo `acme_`, un
productor que envía `acme_region` se rechaza y `brevis_region` es un campo
ordinario.

**Hay que elegirlo antes de crear la primera tabla.** Cambiarlo después añade ocho
columnas y abandona ocho —nada elimina una columna—, así que una tabla que ya
lleva el layout con un prefijo rechaza una carga que declare otro. Ver
[la página del SDK](/docs/sdk/#the-brevis-prefix-is-yours-to-change) para la
normalización y la salida.

**Los campos de un objeto anidado pueden obtener columnas propias.**
`BREVIS_NORMALIZE_DATA=true` en el proceso del gateway aplana un nivel
—`{"name": {"first": "x"}}` aterriza como `name_first`— y todos los streams lo comparten,
así que dos sinks no pueden discrepar sobre una tabla en la que ambos podrían escribir. Ver
[la página del SDK](/docs/sdk/#one-column-per-nested-field) para saber qué renombra,
que es la mitad que hay que leer antes de activarlo.

**En BigQuery, `nationalID` y `nationalId` son una sola columna.** BigQuery pliega
las mayúsculas de las columnas, así que un flush que lleva una grafía en un `DELETE` y la otra en
un `INSERT` crea **una** columna —la primera en orden alfabético— en lugar de
fallar el `CREATE`. Un único evento que lleve ambas se rechaza, nombrando ambos
campos. Postgres y MySQL las mantienen como dos columnas, lo cual es correcto para
ellos. Ver [la página del SDK](/docs/sdk/) para saber qué grafía gana y qué no se
migra.

### `UPDATE` y `DELETE` se registran, no se aplican

`brevis_operation` es una columna. Una tabla de landing es **historial**: un `UPDATE`
aplicado en el sitio pierde la versión anterior, y el día en que se quiere esa versión
es el día en que algo se rompió. Resolver «la versión vigente de cada registro» es
trabajo del modelo aguas abajo:

```sql
qualify row_number() over (partition by brevis_record_key
                           order by brevis_received_at desc) = 1
   and brevis_operation <> 'DELETE'
```

Que es lo que hacen Debezium, Fivetran y Airbyte, y significa que CDC cuesta
**nada** en el camino de escritura: sin modo upsert, sin lock, sin delete.

### La identidad no lleva reloj

```
brevis_ingestion_id = uuid5(auto_table | table | data[unique_key] | sha256(canonical(data)))
```

No hay `occurred_at` en la fórmula, y es deliberado: la llegada pasó a ser
responsabilidad **nuestra**, y nuestro reloj es `time.Now()`, distinto en cada
entrega. Una identidad con él dentro haría de cada reentrega un evento nuevo,
y `merge` dejaría de absorber nada.

```
same record, same content, twice  →  same id, merge absorbs
same record, content changed      →  a different id, both versions land
```

La salvedad es la documentada: dos eventos genuinamente distintos con
`data` idéntico byte a byte se colapsan en uno. Para CDC eso es **correcto**: dos
actualizaciones idénticas de un registro con los mismos valores son el mismo hecho.

La huella es sobre el documento en forma **canónica**: claves ordenadas en
cada nivel, arreglos en su orden, todo entrecomillado. Go aleatoriza la iteración de mapas,
así que un hash ingenuo diferiría entre dos entregas del mismo
evento, que es justo lo que existe para evitar.

### La tabla agrega una columna por su cuenta

Un `data` que lleva un campo que la tabla no tiene hace aparecer la columna.
**Aditivo, y solo aditivo**: nada se elimina, nada se estrecha.

En BigQuery esto solo fue cierto desde la **0.13.0**. Antes, `auto_table`
declaraba `Evolve: additive` en cada destino y solo los drivers de Postgres y
MySQL leían la declaración, así que allí la forma quedaba congelada en la creación
y nada lo decía.

> **Si se viene de la 0.11.0 o la 0.12.0**, las tablas creadas antes de la `0.11.0` necesitan
> una migración puntual, porque la octava columna fija entró en la declaración
> y no en la tabla:
>
> ```sql
> ALTER TABLE `<dataset>.<table>` ADD COLUMN IF NOT EXISTS brevis_received_bytes INT64
> ```
>
> Postgres y MySQL nunca se vieron afectados.

Un lote que pierde la carrera contra el `ALTER` falla, y el reintento ordinario del pipe
lo resuelve: un lote que espera DDL y un lote que espera un worker son la
misma cosa, así que no hay un segundo buffer. Entre réplicas, el metastore mantiene un
*debounce* de un segundo por tabla y por forma, para que diez réplicas que ven el
mismo campo nuevo no se conviertan en diez `ALTER` contra la cuota de BigQuery de cinco
operaciones de metadatos por tabla cada diez segundos.

**Un debounce y no un lock**: nada se libera y ningún lease se renueva.
A quien pierde se le *reintenta*, no se le bloquea, y una réplica que muere reteniendo
uno cuesta a las demás un segundo. Es lo que hace el mercado: Delta Lake e
Iceberg confirman de forma optimista y reintentan, Kafka Connect obtiene la serialización del
orden de las particiones, Fivetran mantiene un escritor por tabla.

### El nombre es la superficie de ataque

`auto_table` convierte un string de un payload en DDL, así que cada campo bajo
`naming` es un rechazo:

- **`pattern`** tiene por defecto `^[a-z][a-z0-9_]{2,48}$`: la intersección de lo que
  aceptan sin comillas Postgres, MySQL, BigQuery y Redshift. Un nombre que pasa
  no necesita comillas en ninguna parte y no puede llevar una inyección.
- **`allow`** restringe más, por prefijo.
- **`max_new_per_hour`** (por defecto 20) acota las **creaciones** en una hora móvil,
  nunca las escrituras: una tabla que ya existe nunca tiene límite de tasa. Bajo
  `metastore: memory` el presupuesto vive en el proceso y cuatro réplicas admiten
  cuatro veces esto; bajo `redis` o `memcached` comparten un contador.

Un nombre fuera de las reglas se rechaza **por evento**, en la respuesta, y todo
evento bien formado de la misma petición aterriza igualmente:

```json
{"accepted": 0, "rejected": ["event 0: the table name \"orders\" does not match ^(app|svc)_[a-z0-9_]{2,40}$"]}
```

Por evento y no por lote, deliberadamente. Rechazarlo al escribir haría fallar
todo el lote: un evento malformado de un productor enterrando los eventos de
todos los demás productores de la misma ventana de flush, sin avisarle a ninguno.

**`auto_table` se rechaza en un endpoint sin `listen.auth`**, incluso
bajo `BREVIS_ENV=local`. Un productor que puede nombrar una tabla puede crear una, así que
necesita autenticación incluso donde un stream ordinario no la necesitaría. Una NetworkPolicy
no lo cubre: limita quién llega al puerto, no qué nombre de tabla
pide.

### El metastore es una caché

Existe para que una escritura por evento no sea una consulta por evento, y guarda tres
cosas: qué tablas existen, el *debounce* de DDL, y el contador detrás de
`max_new_per_hour`.

Es una caché y **no** una fuente de verdad. El destino decide si una
tabla existe, y que *N* réplicas compitan por crear una es el caso normal:
`AlreadyExists` es éxito, y esto solo reduce la carrera. El TTL es cuánto tiempo
puede estar **equivocada**.

| backend | cuándo |
|---|---|
| `memory` | el valor por defecto, no necesita nada. Con **una** réplica es la respuesta correcta |
| `redis` | `addr_from` obligatorio. Un `SETNX` es el claim, un `INCR` es el contador |
| `memcached` | `addr_from` obligatorio. Un `Add` es el claim, y la expiración tiene granularidad de **segundos** |

**Redis es el más rápido para un stream con clave de enrutamiento**, y la
diferencia merece un número y no un adjetivo. Con el disparador por
tabla, una ventana pide un claim por tabla: Redis responde 94 de ellos en un solo
viaje de ida y vuelta en pipeline, y cualquier otro backend los responde en paralelo,
acotado a 32. Medido contra un backend a 25 ms, 94 claims:

| backend | uno a uno | tal como se distribuye |
|---|---:|---:|
| `redis` | 2,589 s | **29,8 ms** |
| `memcached` | 2,577 s | **89,4 ms** |

Tres oleadas frente a un viaje de ida y vuelta. Ambos están dentro de los 500 ms que se
da a un claim, así que memcached es la opción más lenta y no una rechazada.

Con varias réplicas, `memory` da a cada una la suya: el debounce no debouncea
nada y `max_new_per_hour` acota un proceso. Esa es la única razón por la que existen
los otros dos.

**Un metastore caído no debe arrastrar consigo a una escritura.** Un `Get` que
falla es un miss, un claim que falla se comporta como ganado, un contador que falla
relaja el límite. Una caché que puede detener la ingesta es peor que ninguna caché.

**Y «caído» incluye «lento».** El claim se ejecuta mientras se sostiene el lock del buffer,
así que un backend que tarda dos segundos en responder son dos segundos en que ese
stream no acepta nada. Cada llamada está acotada a **500 ms** y falla abierta pasado
ese tiempo. Redis lo obedece como el contexto del llamador; `gomemcache` no admite ningún contexto,
así que memcached lo obedece como un deadline de socket: el mismo límite alcanzado de
dos maneras, porque a uno de los dos clientes no se le puede decir que se detenga.

**`ttl: 0` significa nunca**, y no decir nada sigue tomando el valor por defecto de un minuto:
tres estados, no dos.

El reloj **no** es lo que se recupera de una entrada errónea. Una escritura que el destino
rechaza llama a `invalidate`, que descarta la entrada de inmediato y deja que el
propio reintento del pipe vuelva contra una caché fría: una tabla eliminada a mano cuesta **un
intento fallido**, no un minuto.

La expiración tiene un precio. `sinkFor` cobra `naming.max_new_per_hour` cuando no
**conoce** una tabla, así que un miss cobra por una tabla que existe desde hace
semanas, y un pod que se reinicia un minuto después del último evento de una tabla no obtiene nada
del backend compartido por el que está pagando. Con tablas de larga vida, `ttl: 0` es
la respuesta.

> En `0.12.0` y `0.13.1` esa grafía **no se parsea**: YAML lee un cero desnudo como un entero, y `0.13.2` es la primera que acepta ambas. En esas versiones,
> escribir `ttl: 0s`.

`addr_from` nombra la **variable de entorno** que contiene la dirección, nunca la
dirección: lleva una contraseña con bastante frecuencia.

### BigQuery tiene un piso de flush

**BigQuery permite 1.500 load jobs por tabla por día.** La ventana por defecto de un segundo
son 86.400: 57 veces la cuota, agotada en unos veinticinco minutos. No es un
problema para Pub/Sub, y el número llegaría aquí *desde* una configuración de Pub/Sub.

Por eso un stream que escribe en BigQuery, directamente o a través de un enrutador, se **rechaza
al cargar** con una ventana menor de 60 s. La respuesta real es la Storage Write API,
que no se ha escrito; hasta entonces, la configuración rechaza una ventana que no puede
cumplir en lugar de dejar que el primer despliegue lo descubra.

`auto_table` no puede enrutar a **Redshift**: ese driver no crea tablas, así que
cada nombre nuevo fallaría en la carga. Se rechaza por su nombre, al arrancar.

## Lo que todavía no existe

`sqlite`, `dynamodb`, `kinesis`, `sqs`, `sns`, `kafka` y `rabbitmq` están
planificados y no se ha escrito nada. (`redis` y `memcached` existen como
**metastore**, que es otra cosa: una caché de lo que se sabe sobre las
tablas, nunca un destino.) La tabla al inicio de esta página indica
el estado de cada uno, y se actualiza cuando el estado cambia, no antes.
