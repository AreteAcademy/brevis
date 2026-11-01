---
title: SDK de Go
description: Escribir un fetcher: extraer de una fuente, transformar y cargar en un destino.
group: SDK y bibliotecas
order: 12
slug: sdk
---

Cuando el trabajo de un step es **obtener datos y cargarlos en algún lugar**, el SDK de Go
entrega el fetcher completo en unas pocas líneas. Es un módulo aparte, versionado
de forma independiente del engine.

```bash
go get github.com/AreteAcademy/brevis/sdk@latest
```

Requiere Go 1.23 o superior: el SDK emite filas como `iter.Seq2`.

:::warning No usar `v0.1.0`
Se publicó con un `go.mod` que fija una revisión que no existe, y el proxy de módulos
de Go es inmutable. Empezar en `v0.1.1`.
:::

## Tres pasos

```go
import (
	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to/bigquery"
)

rows, err := sdk.Extract(ctx, sdk.Source{
	From: from.HTTP{URL: "https://api.example.com/v1/events"},
})

rows = sdk.Transform(rows, sdk.Accept("id", "created_at", "amount"))

res, err := sdk.Load(ctx, rows, sdk.Target{
	To:      bigquery.Table{Dataset: "bronze", Name: "events"},
	Columns: []string{"id", "created_at", "amount"},
})
```

`Extract` lee, `Transform` remodela, `Load` escribe. Cada uno recibe y devuelve una
secuencia: nada se materializa en memoria de una sola vez.

![El flujo del SDK: sdk.Extract lee de una API HTTP con from.HTTP, sdk.Transform conserva los campos aceptados y sdk.Load escribe las filas en una tabla de BigQuery. Las tres llamadas corren dentro de tu propio step.](/assets/flow-sdk.svg)

## El driver es un valor, no un ajuste

`from.HTTP` lleva todo lo que necesita una fuente HTTP: URL, headers, reintentos,
paginación y qué significa una respuesta. `from.Files` lleva una ruta y un formato.
Ninguno tiene que dejar espacio para los campos del otro, así que no hay un struct de fuente
que acumule cuarenta opciones de las que cada driver lee seis.

También decide **qué se compila**. Go poda dependencias por paquete
importado, nunca por campo usado:

| lo que se importa | paquetes | AWS | Google |
|---|---|---|---|
| `sdk` | 190 | no | no |
| `sdk` + `from` | 194 | no | no |
| `sdk` + `from` + `to` (archivos) | 195 | no | no |
| `sdk` + `to/bigquery` | 456 | no | **sí** |
| `sdk` + `from` + `store/s3` | 265 | **sí** | no |

Un pipeline completo de archivos —lectura y escritura— cuesta 195 paquetes y ningún SDK
de nube. **Un driver con un SDK de proveedor detrás vive en su propio paquete**, por lo
que BigQuery es `to/bigquery` y los almacenes de objetos son `store/s3` y
`store/gcs`.

## Fuentes y destinos

| fuente | paquete |
|---|---|
| HTTP | `from.HTTP` |
| archivos (local, S3, GCS) | `from.Files` |
| Postgres | `from/postgres` |
| MySQL | `from/mysql` |
| varias fuentes | `from.Many` |

| destino | paquete |
|---|---|
| archivos | `to.Files` |
| BigQuery | `to/bigquery` |
| Postgres | `to/postgres` |
| MySQL | `to/mysql` |
| Redshift | `to/redshift` |
| Pub/Sub | `to/pubsub` |

El esquema de la ruta decide el backend, y el `Store` se **pasa desde fuera** en lugar de
elegirse dentro del driver:

```go
from.Files{Path: "s3://bucket/day=1/*.ndjson", Store: s3.New(client)}
to.Files{Path: "gs://bucket/landing/", Store: gcs.New(client)}
```

Eso es lo que evita que un programa de archivos locales compile una sola línea de código de AWS o
de Google.

### Publicar en un topic

`to/pubsub` es el único destino que no es una tabla, y se comporta
de forma distinta en un aspecto que conviene conocer antes de usarlo: **no añade nada a lo que
envía.**

```go
Target: sdk.Target{
    To: pubsub.Topic{
        Project: "acme-prod",
        Name:    "orders",

        // Nil is the default, and it means NO attributes at all.
        Attributes: func(e sdk.Envelope) map[string]string {
            row, ok := e.Payload.(map[string]any)
            if !ok {
                return nil
            }
            return map[string]string{"orderId": fmt.Sprint(row["order_id"])}
        },
    },
},
```

Un suscriptor recibe el payload, byte por byte, más exactamente los atributos
que se nombraron. Nada más: ni `ingestion_id`, ni `provider`, ni ningún envelope
de ningún tipo.

Es una regla y no minimalismo. Una tabla la crea el pipeline que
escribe en ella, así que el SDK puede decidir cuáles son sus columnas. **Un topic no**: existe
antes que el pipeline, sus suscriptores se escribieron primero y sus
filtros se escribieron primero. Un atributo añadido por cuenta propia sería Brevis
editando el contrato de otra persona, y el suscriptor se enteraría a las tres de la
madrugada.

De ello se siguen tres cosas:

- **Nunca crea un topic.** Un pipeline que puede crear uno puede crear el
  equivocado, y a diferencia de una tabla con un nombre mal escrito nadie lo nota: los mensajes van
  a otra parte y el suscriptor que debía recibirlos permanece en silencio.
- **`Schema`, `Dedup` y `PartitionBy` se rechazan**, nombrando la opción. Existen
  para tablas: un `Schema` es la forma en que un destino crea una, la deduplicación
  necesita una fila que reemplazar, una partición es una idea de las tablas. Lo más parecido aquí
  es `OrderingKey`, que es otra cosa: agrupa mensajes que deben
  *llegar* en orden.
- **Un fallo es parcial.** Publicar no es transaccional, así que 48.000 mensajes
  que fallan en el 31.000 han *entregado* 31.000. El resultado informa lo que realmente
  salió, y el error dice que volver a ejecutar es volver a entregar: el suscriptor tiene que ser
  idempotente, y Pub/Sub es at-least-once de todos modos.

Ejecutable, contra el emulador:
[`examples/13-pubsub`](https://github.com/AreteAcademy/brevis/tree/master/examples/13-pubsub).

## Un fetcher completo

`sdk.Run` se ocupa de los flags, `-dry-run`, el logging, los reintentos, la paginación,
la procedencia, la creación de tablas y el código de salida. Lo que queda en el archivo es solo
lo específico de esa fuente:

```go
package main

import (
	"time"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
)

func main() {
	sdk.Run(sdk.Pipeline{
		Source: sdk.Source{
			From: from.HTTP{
				URL:     "https://api.example.com/v1/events",
				Timeout: 15 * time.Second,
			},
			Guard:  sdk.RejectIf("error"),
			Expand: sdk.ArrayAt("results"),
		},
		Target: sdk.Target{
			Provider: "example",
			Entity:   "events",
			Key:      sdk.Key("id"),
			When:     sdk.Field("created_at"),
		},
	})
}
```

```bash
go run ./fetcher -dry-run   # extracts, counts rows and errors, writes nothing
go run ./fetcher
```

## El contexto del run

Cuando el fetcher corre **como step de un workflow**, el engine inyecta en el
entorno lo que de otro modo no podría saber: si es la primera ejecución,
con qué parámetros se despachó, qué run es. El SDK lo lee en
`Pipeline.Run`:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	if p.Run.Params["load_full"] == "true" {
		p.Source.From = from.HTTP{URL: base + "?full=1"}
	}
	return nil
},
```

Ejecutado a mano, `Run` vuelve en cero: leerlo es opcional, e ignorarlo
no cuesta nada.

Sin historial, la respuesta a «¿es la primera ejecución?» es siempre **no**:
crear una tabla sin certeza es peor que no crearla.

### El reloj

`p.Run.Auto` es lo que el engine calculó sobre este run: los
[parámetros automáticos](/docs/parameters/#parametros-automaticos). `Auto.Now()` es el reloj que se lee
en lugar de `time.Now()`: en un run programado es el slot, así que no se mueve
cuando el run llega tarde y no se mueve cuando el run se reintenta tres horas
después.

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	start, end, ok := p.Run.Auto.Window()
	if !ok { // no schedule: fall back to a fixed window
		start, end = p.Run.Auto.Now().Add(-24*time.Hour), p.Run.Auto.Now()
	}
	p.Source.From = from.HTTP{URL: base +
		"?from=" + start.Format(time.RFC3339) +
		"&to=" + end.Format(time.RFC3339)}
	return nil
},
```

Ejecutado a mano, `Auto.Now()` *es* el reloj de pared y `Window()` devuelve `false`, de modo que
el desarrollo local no necesita ningún caso especial.

## No confundir con las bibliotecas cliente

Este SDK es maquinaria ETL en Go: drivers, paginación, procedencia, creación
de tablas. Las [bibliotecas cliente](/docs/libraries/) son otra cosa:
clientes delgados que entregan a un step el contexto y el reloj del run, en Python hoy
y en Node.js y Rust más adelante. Un step que usa pandas o dbt quiere
las segundas, no este.

## Parámetros que construyen el pipeline

`p.Run.Params` se puede leer dentro del pipeline, lo cual llega tarde para un valor
que decide qué **es** el pipeline: una fuente por estado, una tabla por
tenant. `Pipeline.Flags` se parsea dentro de `Run`, así que llega tarde por la misma
razón.

```go
func main() {
	sdk.Run(pipeline(sdk.ParamList("ufs")))
}
```

`sdk.Param` y `sdk.ParamList` leen primero el entorno del engine y
`-param name=value` después:

```bash
./fetch-stations -param ufs=SP,RJ
```

El entorno gana: bajo el engine es el valor, y un flag dejado en un manifiesto
no debe anular lo que escribió el operador. Sin engine, el flag es lo que hay, lo cual
es mejor que escribir `BREVIS_RUN_PARAMS` como JSON en cada ejecución.

## El layout de aterrizaje

Un pipeline puede aterrizar la **misma tabla que aterriza un gateway** —las mismas columnas
y el mismo `brevis_ingestion_id`—, de modo que elegir entre ambos es una
decisión de despliegue y no de esquema. Un equipo puede leer ambos juntos, y pasar
de uno a otro sin una migración.

```go
const table = "landing.orders"

sdk.Run(sdk.Pipeline{
    Source:    sdk.Source{From: from.HTTP(/* ... */)},
    Transform: []sdk.Transformer{sdk.Landing(table, sdk.LandingKey("id"))},
    Target: sdk.Target{
        To:     postgres.Table{DSN: dsn, Name: table, CreateTable: true},
        Schema: sdk.LandingSchema(sdk.LandingOptions{}),
    },
})
```

`sdk.LandingSchema` son nueve columnas: las ocho que posee el layout y la que
pone el productor. `sdk.Landing` las rellena y pone el registro, completo, en
`data`. Se pueden añadir columnas propias al esquema si se quiere, o tomar
`sdk.LandingControlColumns` y dar columnas propias a los campos del registro.

### Dos formas: el registro completo, o una columna por campo

Lo de arriba es la forma de **documento**: el registro va a `data` como JSON,
y la tabla es la misma sea lo que sea que contenga el registro. Con
`sdk.LandingColumns()` cada campo del registro obtiene una columna propia:

```go
sdk.Landing(table, sdk.LandingKey("source_key"), sdk.LandingColumns())
```

La tabla es entonces `sdk.LandingControlColumns` más las columnas propias, **no**
`sdk.LandingSchema`, que incluye `data`:

```go
Target{
    To: postgres.Table{DSN: dsn, Name: table, CreateTable: true},
    Schema: append(sdk.LandingControlColumns(sdk.LandingOptions{Keyed: true}),
        sdk.Column{Name: "source_key", Type: sdk.TypeString},
        sdk.Column{Name: "series", Type: sdk.TypeString},
        sdk.Column{Name: "data", Type: sdk.TypeString},
        sdk.Column{Name: "valor", Type: sdk.TypeString},
    ),
}
```

**Esas columnas las declaras tú.** El SDK nunca infiere un esquema, y esta opción
no cambia eso: decide cómo se construye la *fila*, nunca qué es la tabla.
Un gateway infiere porque su productor es un desconocido que envía lo que tenga; aquí
el autor del pipeline conoce la forma y la escribe, en un diff que alguien
revisa.

Es la misma forma que aterriza un gateway como
[`shape: columns`](/docs/ingestion-sinks/#dos-formas-un-contrato), mediante el
mismo código: mismas columnas, mismos valores, mismo `brevis_ingestion_id`.

**Un nombre de campo tiene que poder ser un nombre de columna**: una letra o guion bajo,
y luego letras, dígitos y guiones bajos. Esa es la regla de BigQuery, la más estrecha
de los cuatro destinos, así que un nombre que pasa aquí funciona en todas partes. Un
registro con `my-field` se rechaza por su nombre, antes de escribir nada. La
forma de documento no tiene esa regla: un guion es una clave JSON perfectamente válida.

**Por defecto no aplana.** Un objeto anidado se convierte en UNA columna `JSON` bajo la
clave que lo contenía: `customer` contiene `{"id": 7, "uf": "SP"}`, no `customer_id`
y `customer_uf`. Un arreglo también es `JSON`. Un registro que quiere una fila *por
elemento del arreglo* quiere `sdk.ArrayAt` en el `Expand` de la fuente: se ejecuta antes de
`Landing`, y es otra operación con otro nombre.
[`BREVIS_NORMALIZE_DATA`](#una-columna-por-campo-anidado) cambia la primera mitad
de eso para todo un despliegue; un arreglo sigue siendo `JSON` en ambos casos:

```go
Source: sdk.Source{From: from.HTTP(/* ... */), Expand: sdk.ArrayAt("results")},
```

**`null` sigue siendo `NULL`**, nunca `""`. Un campo enviado como null y un campo enviado
vacío son hechos distintos, y una columna no puede distinguirlos después.

**`data` pasa a ser tuyo.** En esta forma el layout no tiene ninguna columna con ese nombre,
así que un productor con un campo llamado `data` —que es exactamente lo que envía la
serie de Bacen— obtiene una columna propia con su propio valor. Bajo
`document`, el suyo es una clave dentro del JSON del layout.

### El prefijo `brevis_` se puede cambiar

```
BREVIS_LANDING_PREFIX=acme
```

y las ocho columnas del layout pasan a ser `acme_ingestion_id`, `acme_record_key`
y las demás. Sin definirlo, son lo que siempre han sido.

**Hay que elegirlo cuando se crea la tabla, porque cambiarlo después no es un
renombrado.** Nada en este SDK elimina jamás una columna, así que un stream que cambia
de prefijo no renombra ocho: añade ocho y abandona ocho. Cada fila
posterior tiene NULLs en el conjunto antiguo, y

```sql
qualify row_number() over (partition by brevis_record_key
                           order by brevis_received_at desc) = 1
```

particiona por una columna en la que ya no escribe nadie. Llegan filas, el dashboard
se queda plano, y nada registra nada.

Por eso una tabla que ya lleva el layout con un prefijo **rechaza** una carga que
declare otro, antes de la extracción, nombrando ambos. Hay que apuntar a una tabla nueva,
o renombrar primero las ocho columnas a mano.

**Lo que se escriba se normaliza**, de modo que haya una sola ortografía en la que equivocarse
en lugar de cuatro:

| defines | obtienes |
|---|---|
| nada, o en blanco | `brevis_` |
| `_NAME` | `name_` |
| `_NAME_` | `name_` |
| `NAME` | `name_` |

Se recorta `_` de ambos extremos, se pasa a minúsculas y se añade exactamente uno. Dos valores se rechazan
en lugar de adivinarse: solo guiones bajos y nada más, porque dejaría
`ingestion_id` —exactamente la columna que `sdk.IngestionID()` escribe en cada
pipeline ordinario— y cualquier cosa que no pueda iniciar un nombre de columna. **Un proceso
con un valor inutilizable no arranca**, que es el único resultado honesto: la
alternativa son tablas con el nombre del prefijo que se estaba reemplazando.

**Es por PROCESO, y eso es el objetivo y no una limitación.** Todos los
streams de un gateway lo comparten, así que dos sinks no pueden discrepar, ni
tampoco el esquema que declara una tabla y el transformer que la llena.

**La regla de reservados se mueve con él.** Bajo `acme_`, un productor que envía
`acme_region` se rechaza, porque quien puede falsificar una columna de control
falsifica una de aspecto real. Y `brevis_region` pasa a ser un campo ordinario.

`data` nunca lleva el prefijo. Es la columna del productor, no la del
layout.

### Una columna por campo anidado

```
BREVIS_NORMALIZE_DATA=true
```

y los campos de un objeto anidado obtienen columnas propias, como lo hace el
`json_normalize` de pandas, un nivel:

```python
data = [
    {"id": 1, "name": {"first": "Coleen", "last": "Volk"}},
    {"name": {"given": "Mark", "family": "Regner"}},
    {"id": 2, "name": "Faye Raker"},
]
```

```
id  name_first  name_last  name_given  name_family  name
```

**Hay que elegirlo antes de la primera tabla, porque no solo AÑADE columnas.**
Las renombra, y nada en este SDK elimina una jamás: el nombre antiguo se queda
con las filas que ya tiene mientras todo lo posterior aterriza en el nuevo, y
la consulta que alguien escribió contra él sigue ejecutándose y deja de ver filas nuevas.
Ocurren dos renombrados:

- `userName` pasa a ser `username`, haya o no algo anidado
- `name`, que contenía objetos, se reemplaza por `name_first` y `name_last`

Una tabla que ya lleva cualquiera de las dos formas **rechaza** una carga con esto activado, antes de
la extracción, nombrando ambas columnas. Hay que apuntar a una tabla nueva, o renombrar
las columnas a mano primero.

**`name` puede aparecer tanto aplanado como completo**, y el ejemplo de arriba
lo muestra: el tercer registro lleva `name` como string, así que la tabla tiene
`name_first`, `name_last` Y `name`. Un campo que es un objeto en un registro
y un escalar en otro produce ambos.

**Un nivel, y los arreglos son valores.** `{"a": {"b": {"c": 1}}}` es `a_b`
con `{"c": 1}`: una columna `JSON`, no `a_b_c`. `{"a": [1, 2]}` es `a`,
también `JSON`. Un registro que quiere una fila por elemento del arreglo sigue queriendo
`sdk.ArrayAt`.

**Unido y en minúsculas, sin dividir por camelCase.** `userName` pasa a ser
`username` y no `user_name`, porque la otra lectura no tiene una respuesta
acordada para los acrónimos —`HTTPStatus`, `userID`— y cada biblioteca discrepa
sobre ellos.

**Una colisión se rechaza, nunca se resuelve.** `{"name_first": 1, "name":
{"first": 2}}` haría una columna de dos campos, y la iteración de mapas de Go es
aleatoria: dejar ganar a uno daría al mismo lote una tabla distinta en
otra ejecución. El rechazo nombra ambos.

**El prefijo reservado se comprueba en la columna, no en el campo.** Un productor
que envía `{"brevis": {"stream": "x"}}` crearía de otro modo `brevis_stream`
por una puerta que la comprobación del campo no vigila. Se rechaza, nombrando
`brevis.stream` para que pueda encontrar el campo.

**Es por PROCESO**, como el prefijo, y por la misma razón: todos los streams
de un gateway lo comparten, así que dos sinks no pueden discrepar sobre una tabla en la
que ambos podrían escribir. Se aplica solo a la forma `columns`: bajo
`document` el registro va completo a una columna `JSON` y no hay nada
que aplanar.

### Texto que ya es JSON

Una columna declarada como `sdk.TypeJSON` recibe un **valor** JSON: un objeto, un
arreglo, un número. En BigQuery la fila se codifica completa, así que un `string` de Go allí
se convierte en un literal de string JSON y la columna acaba conteniendo el TEXTO del
objeto en lugar del objeto:

```sql
SELECT JSON_TYPE(payload) ...;            -- "string", not "object"
SELECT JSON_VALUE(payload, '$.a') ...;    -- NULL
```

La columna es del tipo correcto y los datos que contiene son inalcanzables, que es lo
peor de los tres resultados porque nada falla. **El destino BigQuery lo
rechaza** desde `v0.74.0`, nombrando la columna.

Hay que pasar el propio objeto cuando se tiene uno. Cuando lo que se tiene ya es
texto JSON —una columna leída de otra base de datos, un cuerpo de respuesta que no
se quiere volver a parsear—, se indica:

```go
row["payload"] = sdk.JSONText(body)
```

El propio `json.RawMessage` de `encoding/json` también funciona, por la misma razón:
ambos escriben el JSON que contienen en lugar de un string entrecomillado de él.

**Postgres y MySQL no se ven afectados y no lo rechazan.** Su servidor
parsea un string en una columna JSON, y siempre lo ha hecho.

### Cuando aparece un campo que la tabla no tiene

Un productor añade un campo y la carga se detiene, nombrándolo: la fila lleva una
columna que nadie declaró, y escribirla en un destino que nunca la mencionó
la descartaría en silencio. Ese rechazo es correcto por defecto, pero en una tabla de landing es el único cambio
que no puede romper a un lector, y el destino puede hacerlo:

```go
postgres.Table{DSN: dsn, Name: table, Evolve: sdk.EvolveAdditiveFromPayload}
```

El lote entonces **completa** la declaración: un campo que lleva y que la
declaración no tiene se convierte en columna, `STRING` a menos que el campo sea un objeto
o un arreglo, y entonces `JSON`. Es la misma regla y el mismo código con el que un gateway
aterriza `shape: columns`, así que ambos producen la misma tabla.

Otros dos valores, y la diferencia entre ellos es de dónde viene la columna:

| `Evolve` | añade |
|---|---|
| `sdk.EvolveNone` (por defecto) | nada. Cualquier diferencia se rechaza, nombrando ambos lados |
| `sdk.EvolveAdditive` | una columna que tu **declaración** tiene y la tabla no |
| `sdk.EvolveAdditiveFromPayload` | eso, más una columna que el **lote** lleva |

**Solo `ADD COLUMN`, siempre.** Una columna que desaparece de la fuente deja de
escribirse y permanece en la tabla; nada se renombra, nada se elimina y ningún
tipo se estrecha jamás. Eso no es una carencia: una tabla de landing es historial, y la
única alteración que no puede romper a quien la lee es una columna nueva que admite nulos.

**Completa una declaración; no la reemplaza.** Sin nada declarado
se rechaza antes de la extracción, nombrando la tabla: crear una tabla completa
a partir de un payload es otra decisión, y el SDK no la toma. En el layout de landing el
daño sería concreto: `brevis_received_at` saldría
como texto en lugar de timestamp, y `brevis_loaded_at` nunca viaja en
la fila, así que la medición de latencia de extremo a extremo simplemente faltaría.

**El SDK sigue sin inferir.** El tipo viene de la FORMA del campo, nunca
de su valor: `21129` y `8.89` declaran ambos `STRING`, de modo que el día en que la
serie publique un número entero no cambia nada. Un tipo leído del primer
lote es el fallo que esta regla existe para evitar.

**Y un `null` no decide nada, pero permanece en la fila.** Son dos preguntas
distintas, y responderlas con una sola respuesta costó la v0.77.0.

Un null no tiene forma, así que el **esquema** no tiene nada que leer: un campo que
llega `null` no declara ninguna columna, y la columna aparece desde el primer registro
que lleva un VALOR, con la forma que tenga ese valor. Al revés, un
null se convertiría en `STRING` por falta de respuesta, y el primer arreglo que llegara después
haría que el destino rechazara el lote completo, sin una migración de `STRING` a `JSON`
que lo deshaga.

La **fila** lleva lo que envió el productor, null incluido. Tiene que hacerlo: una columna
que DECLARASTE en `Target.Schema` y que llegó vacía debe estar en la fila, o la
comprobación que compara la declaración con ella lee una cadena que dejó
de producirla, y rechaza la carga. Eso fue lo que hizo la v0.77.0, y lo que la 0.78.0
deshizo.

El destino resuelve el resto: un campo null en TODOS los registros del lote,
sin columna en la tabla, no se escribe en ninguna parte y no rechaza nada. Un null y
un campo ausente aterrizan el mismo `NULL`, así que no se pierde nada.

**Un campo que cambia de forma se rechaza, no se absorbe.** Si `valor` llegó como
escalar y creó una columna de texto, un objeto que llegue después es un cambio de tipo
y el lote se detiene, con el propio comentario de la columna en el mensaje, para que puedas
ver que nadie la declaró. Hay que renombrar la columna, o migrarla a mano.

**En BigQuery, dos grafías de un nombre son una sola columna.** BigQuery pliega
las mayúsculas de las columnas: un lote que lleva `nationalID` en un registro y `nationalId` en
otro declara **una** columna, no dos, y la grafía que gana es la
primera en orden alfabético: `nationalID`, porque `D` ordena antes que `d`. No la
primera en llegar: el mismo lote repetido no debe crear una columna con grafía distinta. Una
declaración que dice `createdat` frente a una tabla cuya columna es
`createdAt` es también la misma columna, así que no se añade nada y no se rechaza nada.

**Un registro que lleva ambas grafías se rechaza, nombrando ambas.** El
destino tiene una columna y uno de los dos valores ganaría en silencio, así que
la carga se detiene: hay que renombrar uno en `Transform`, o mapear ambos a un único
campo. Dos *registros* que llevan cada uno una grafía están bien: es tráfico
CDC ordinario, y es lo que esta regla existe para aterrizar.

**No se migra nada.** Una tabla que ya tiene dos columnas que difieren solo
en mayúsculas conserva ambas; ninguna columna se renombra y ninguna se elimina.

**Postgres y MySQL no pliegan.** Un identificador entrecomillado es distinto allí,
así que dos grafías son dos columnas: a propósito, y el mismo fetcher apuntado a
BigQuery las aterrizará en una.

**Cada columna que creó un lote lo dice, en la tabla.** Un comentario en Postgres
y MySQL, una descripción de campo en BigQuery, con la fecha:

```
brevis: added from a batch on 2026-09-29; the type is the landing rule
(scalar text, object and array json), not a decision
```

Seis meses después eso es lo único que queda que responde «¿cuándo apareció esta
columna, y quién lo decidió?». Un registro de log ya habrá rotado para entonces.

**No es un modo.** `Target.Schema` sigue siendo tú declarando qué es la tabla:
esto te entrega un layout que ya existe en lugar de obligarte a escribirlo. Nada en el SDK se comporta de forma distinta porque lo usaste, y un
pipeline que declara su propio esquema no se ve afectado.

**El nombre de la tabla se escribe una sola vez.** Va a `Landing`, porque es el segundo
espacio del id, y al destino, porque ahí es donde van las filas.
**Nada comprueba que ambos coincidan**: el `Writer` conoce su propia tabla pero
solo la expone mediante `Describe`, que es el nombre para logs y errores,
y un id construido sobre eso ataría cada id ya escrito a un string de log.
Si se desincronizan, las filas aterrizan correctamente con ids acuñados para una tabla
en la que nadie escribió. Se verán bien. No coincidirán con las del gateway.

**Tres columnas son `NULL`, y esa es la respuesta honesta.** `brevis_stream`
y `brevis_gateway` nombran cosas que un pipeline no tiene:
`brevis_gateway IS NULL` es la forma de distinguir la fila de un pipeline de la de un gateway.
`brevis_received_bytes` también es `NULL`: el del gateway cuenta lo que llegó por
el cable, envelope incluido, y el JSON propio del registro es alrededor de un 30% menor.
Una columna con dos significados haría que una suma entre filas de ambos caminos
fuera errónea según la proporción que vino de cada uno. `length(data)` responde
exactamente la versión del pipeline.

**Combinar requiere `DedupKey`, y sin ella no hay combinación alguna.** Todo
driver combina por `ingestion_id` salvo que se indique otra cosa, y la columna de identidad de este layout
es `brevis_ingestion_id`:

```go
Target{
    To:       postgres.Table{DSN: dsn, Name: table, CreateTable: true},
    Schema:   sdk.LandingSchema(sdk.LandingOptions{UniqueID: true, Keyed: true}),
    Dedup:    sdk.DedupMerge,
    DedupKey: sdk.LandingColumnID,
}
```

`UniqueID` es lo que pone la restricción UNIQUE al id, y tanto Postgres como
MySQL se niegan a combinar sin una: `ON CONFLICT` no tiene nada con qué coincidir.
Un `DedupKey` que tu `Schema` no declara se rechaza antes de ejecutar nada,
porque una combinación sobre una columna que la tabla no tiene no coincide con nada, y una combinación
que no coincide con nada se ve exactamente igual que una que coincide con todo lo que debe.

**Según el destino, y hoy MySQL no puede.** BigQuery no tiene restricciones
únicas y rechaza la declaración de plano. El `string` de MySQL es
`LONGTEXT`, y MySQL no indexa un `LONGTEXT` sin longitud, así que una
tabla de landing con combinación no se puede *crear* allí en absoluto:

```
Error 1170 (42000): BLOB/TEXT column 'brevis_ingestion_id' used in key
specification without a key length
```

Postgres funciona, porque su `string` es `TEXT`. En MySQL, aterrizar con
`DedupNone` y resolver la versión vigente aguas abajo, o crear la tabla
manualmente con `CreateSQL` y una columna de id con tamaño.

**El id es direccionable por contenido.** El mismo registro produce el mismo id por
cualquiera de los dos caminos, y un registro modificado produce uno nuevo, que es lo que permite
que una reejecución no haga nada en lugar de duplicar.

## Referencia

- [pkg.go.dev](https://pkg.go.dev/github.com/AreteAcademy/brevis/sdk) — la API completa
- [`examples/`](https://github.com/AreteAcademy/brevis/tree/master/examples) — doce ejemplos ejecutables
- [`CHANGELOG.md`](https://github.com/AreteAcademy/brevis/blob/master/CHANGELOG.md) — historial versión por versión
