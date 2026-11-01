# Ingesta

> Un endpoint HTTP que aterriza datos: validar, identificar, agrupar en lotes, entregar. Imagen propia, versión propia.

*https://brevis.sh/es/docs/ingestion/ · brevis.sh docs (es)*

---

El gateway de ingesta es un **endpoint HTTP que aterriza datos**. Un cliente hace `POST`,
recibe un `202`, y lo que llegó se convierte en un registro en un topic, en una tabla o en un
bucket.

![El flujo del Gateway: un POST a /v1/clicks se decodifica, recibe su ingestion_id, se responde con 202 de inmediato y se añade a un lote; un worker entrega el lote al sink, aquí files. El Gateway no tiene base de datos.](/assets/flow-gateway.svg)

Es **otro binario, otro módulo de Go, otra versión y otra
imagen**. El engine orquesta y nunca toca datos de clientes; el
gateway no hace otra cosa. La numeración es independiente a propósito: el engine
está en 0.17 y el gateway en 0.27, que es lo verdadero.

Con el `gateway.yaml` de la sección siguiente, y la clave que pide en `keys_from`:

```bash
docker run -p 8080:8080 -e BREVIS_INGEST_KEYS=dev-key \
  -v ./gateway.yaml:/etc/brevis/gateway.yaml:ro \
  areteacademy/brevis-gateway:0.32.0
```

```text
2026/10/09 01:03:01 drain budget 30s; set terminationGracePeriodSeconds >= 40
2026/10/09 01:03:01 connections idle out after 1m30s and are recycled after 1m0s
2026/10/09 01:03:01 listening on :8080
2026/10/09 01:03:01 metrics on :9090/metrics
```

Un `POST` vuelve `202` de inmediato:

```bash
curl -X POST localhost:8080/v1/clicks -H 'Authorization: Bearer dev-key' \
  -d '{"event_id":"e-1","occurred_at":"2026-10-08T12:00:00Z","page":"/pricing"}'
```

```text
{"accepted":1,"rejected":null}
```

Sin credenciales de Google Cloud, el lote se reintenta y va al dead
letter; ver [Cuando el sink rechaza](#when-the-sink-refuses).

La imagen `-slim` lleva solo los sinks `auto_table`, `files` y `postgres`;
el `pubsub` de este ejemplo y los demás están en la imagen completa. Una imagen slim con un
sink que no lleva se rechaza al arrancar, con el motivo.

## El archivo es el contrato

No hay código que escribir salvo los hooks. Streams, rutas, formatos, identidad,
reintentos, destino y dead letter: todo en el YAML, y cada campo se rechaza
cuando no se puede cumplir.

```yaml
name: events_gateway

listen:
  addr: :8080
  max_body: 1MiB
  idle_timeout: 90s                  # the default; it cannot be turned off
  max_conn_age: 60s                  # off unless set — see below
  auth:
    type: bearer
    keys_from: BREVIS_INGEST_KEYS   # the NAME of the variable, never the keys

metrics:
  addr: :9090                        # its own port, never the ingest one

streams:
  - name: clicks
    path: /v1/clicks
    format: json                     # json | array | ndjson — declared, never sniffed

    identity:
      provider: web
      entity: click
      source_key: event_id
      record_ts: occurred_at

    buffer:
      flush: {every: 1s, records: 500}

    sink:
      type: pubsub
      project: acme-prod
      topic: clicks

    dead_letter:
      type: files
      path: /var/dead-letter/
```

`format` se declara y nunca se adivina: adivinar es como un lote de mil
se convierte en una fila que contiene un arreglo.

## El `ingestion_id`, que es lo importante

Cada registro sale con un `ingestion_id`: un **UUID v5 congelado** sobre
`provider|entity|source_key|record_ts`.

Es el **mismo id** que calcula un fetcher batch para el mismo registro. Una fila que
aterriza el gateway y una fila que aterriza un pipeline del [SDK](/es/docs/sdk/index.md) son *una sola fila*,
sin reconciliación entre ellas.

Dos consecuencias prácticas:

- **Un `POST` reintentado es el mismo registro.** Un cliente puede reenviar sin temor;
  un destino con `merge` absorbe el reintento en lugar de escribir una segunda fila.
- **La fórmula está congelada.** Los cuatro campos son obligatorios, y omitir uno
  produce un id *distinto* en lugar de uno más débil. El archivo rechaza la
  configuración incompleta en lugar de aceptarla.

Las herramientas de ingesta mueven bytes muy bien y no tienen opinión sobre lo que un registro
*es*. Esa opinión es lo que añade el gateway.

## La petición nunca espera al sink

Decodifica, ejecuta el hook, calcula la identidad, añade a un buffer y
devuelve. **Eso es todo.** Un lote lleno va a un pool de workers, y la publicación,
el `COPY`, los reintentos y el dead letter ocurren allí.

Esto no es cosmético. Antes de que existiera, la petición que llenaba por casualidad
un lote ejecutaba la entrega en línea, así que un llamador de cada `flush.records` pagaba el
viaje de ida y vuelta completo, y cuando el sink estaba caído, toda la ventana de reintentos. Un p99
determinado por qué llamador tuvo mala suerte no es un p99 sobre el que alguien pueda actuar.

```yaml
buffer:
  flush:
    every: 1s         # age
    records: 500      # count
    size: 8MiB        # bytes — whichever is crossed FIRST sends the batch
    max_age: 30s      # and this one sends it REGARDLESS
  workers: 4          # batches delivered at once
  queue: 64           # full batches that may wait for a worker
  max_records: 10000  # events held in memory before the gateway says no
  max_bytes: 256MiB   # the same ceiling in bytes
```

### El reloj de flush pertenece al despliegue

`buffer.flush.every` es un `time.AfterFunc` en **cada réplica**. Así que los load
jobs que recibe una tabla son función del número de réplicas, no del tráfico:

```
jobs / day / table  =  replicas × 86400 / every
```

En BigQuery, frente a 1.500 por tabla por día, **dos réplicas a 60 s ya son el
192% de una cuota que no se puede aumentar**, antes de que un HPA haga nada. Y la
dirección es contraria a la intuición: el gateway escala horizontalmente por concurrencia HTTP
mientras que el sink quiere concentración.

```yaml
buffer:
  flush:
    every: 60s
    max_age: 300s
    claim: true
```

Con `claim`, una réplica hace un set-if-absent para el stream y la ventana
antes de volcar por el timer, y el timer despierta en el **límite de la ventana**,
de modo que todas las réplicas piden la misma clave. Un flush por ventana, sea cual sea el
número de réplicas. Y se autoajusta: una réplica solo espera cuando otra
está realmente compitiendo, así que al reducir réplicas se recupera la menor latencia sin que nadie
edite una configuración.

Medido con dos réplicas y un Redis, ventanas de 5 s: **8 load jobs pasaron a 6,
uno por ventana, alternando 3/3**, sin ninguna ventana con dos.

**Necesita un backend compartido.** Con `memory`, cada réplica gana su propio claim
y nada coordina. Y **requiere `max_age`**, sin el cual la configuración
se rechaza: el claim convierte `every` en un objetivo, y un objetivo sin
techo es latencia sin nada que la acote.

**Un metastore inalcanzable vuelca de todos modos.** Perder la coordinación cuesta load
jobs; negarse a volcar retiene datos. Verificado matando Redis a mitad de un run: 40 de 40
eventos aterrizaron, nada retenido.

**`outcome="unreachable"` es el que merece una alerta.** No se pudo consultar
al backend, así que el claim falló abierto: la coordinación está apagada y los load jobs
se multiplican de nuevo por el número de réplicas, en silencio, porque cada flush sigue
pareciendo un flush.

Y el claim da **exclusividad, no equidad**: gana quien pide primero. Un
run real de dos réplicas midió 4/1, no un reparto parejo. `max_age` es lo que impide
que la desafortunada pase hambre.

Con `claim: true` y `metastore: memory` el gateway avisa **una vez al arrancar**:
cada réplica gana su propio claim, así que todo parece correcto y nada
coordina.

### `every` es un objetivo; `max_age` es una promesa

Los tres disparadores de arriba deciden cuándo un lote **intenta** salir. `max_age`
decide cuándo sale **pase lo que pase**.

Hoy la diferencia está inerte, porque nada aplaza el objetivo. Empieza a
importar una vez que el reloj de flush deja de ser por proceso: `buffer.flush.every` es un
`time.AfterFunc` en cada réplica, así que el número de load jobs que recibe una tabla es
función del **número de réplicas**, no del tráfico: `replicas × 86400 / every`. En
BigQuery, frente a 1.500 por tabla por día, **dos réplicas a 60 s ya son el 192%
de la cuota**.

Una vez que una réplica puede ceder una ventana a otra, sigue llenándose, y `max_age` es
lo que impide que alguien retenga datos indefinidamente esperando su turno.

También fija el drenaje: lo que un buffer puede estar **reteniendo** al recibir `SIGTERM` queda
acotado por él, y el `terminationGracePeriodSeconds` que el gateway imprime al
arrancar se deriva de él.

Un techo **por debajo** del objetivo se rechaza al cargar: significaría que el objetivo
nunca se aplica.

### Por qué hay un disparador por tamaño

Los otros dos son **conteos**, y un conteo no distingue 500 eventos de 2 KB de
500 de 2 MB: 1 MB frente a 1 GB, el mismo número en el archivo. `max_records:
10000` tiene el mismo punto ciego: 20 MB o 20 GB, y nada en el YAML podría
decir cuál.

Un productor que empieza a enviar el documento completo en lugar de su id tumba el pod,
y hasta que esto existió **ningún campo podía expresar el límite**.

El tamaño se mide sobre lo que **llegó**, no sobre lo que pesa el evento en memoria (un
`map[string]any` es varias veces su JSON) ni después de un hook que lo infle.
Una aproximación, y la correcta: es gratis al decodificar, se mueve con el
payload, y aquello contra lo que protege es un registro que creció diez veces.

**En BigQuery `size` es un techo, no un objetivo.** Ese destino permite 1.500
load jobs por tabla por día, razón por la cual `every` tiene un piso de 60 segundos. Pero
el piso rige el **timer**: un tamaño que se dispara cada pocos segundos lo atraviesa
de largo y gasta la misma cuota. Nada rechaza eso al cargar, porque la tasa de
llegada no se puede conocer allí. Lo que existe en cambio es visibilidad:

```
brevis_gateway_flushes_total{stream,trigger}   trigger = time | records | size
```

`trigger="size"` subiendo en un stream de BigQuery te dice que la ventana no es
lo que está agrupando en lotes.

Ese contador es por stream, y la cuota es por **tabla**: con `auto_table`
una ruta se convierte en muchas tablas, y 94 de ellas informando un número no dice nada
sobre ninguna. Detrás de `BREVIS_INGESTION_METRICS` hay uno que la nombra:

```
brevis_gateway_table_flushes_total{stream,table,trigger}
```

Un flush de una tabla es un load job para ella, así que esta es la serie sobre la que alertar
al llegar a 1.200 de los 1.500 que permite BigQuery. La mitad incondicional es una línea al
arrancar, que dice cuánto cuesta `flush.every` por tabla por día antes de que llegue
un evento.

**Está acotado, no es fire-and-forget.** Una goroutine por lote convertiría una caída del sink
en memoria ilimitada. Cuando la cola y el buffer están ambos llenos, la
respuesta es **`503` con `Retry-After`** en lugar de un `202` para un evento sin
adónde ir. Un evento aceptado que nunca se entrega es el único resultado que este
servicio existe para evitar.

Y ese `503` es **seguro de reintentar**, de un modo que casi ningún servicio puede afirmar: el
`ingestion_id` es una función congelada del propio evento, así que el mismo cuerpo enviado
de nuevo es el *mismo registro*.

## Una conexión decide qué réplica recibe la carga

Detrás de un Service `ClusterIP` de Kubernetes, kube-proxy elige un backend **una vez
por conexión TCP**, no por petición. La mayoría de los clientes mantienen las conexiones vivas
y las reutilizan, así que un productor con un puñado de ellas lo envía todo
a una o dos réplicas sin importar cuántas estén ejecutándose, y una réplica que el HPA
añade no recibe nada, porque nadie la contacta.

```yaml
listen:
  idle_timeout: 90s     # closes a connection nobody came back to
  max_conn_age: 60s     # after this, the next response asks the client to redial
```

**`max_conn_age` está desactivado por defecto**, y es deliberado: cuánto tiempo puede
un cliente mantener una conexión es política, e intercambia un handshake dentro del clúster por
conexión por edad a cambio de una carga que se reparte. La petición misma se
atiende siempre: el header va en la respuesta, nada se reintenta y ningún
lote se pierde.

**`idle_timeout` tiene un valor por defecto y no se puede desactivar.** Servir sin ninguno
es un descriptor de archivo por cliente abandonado hasta que el proceso se reinicia. Hay que fijar uno
largo si eso es lo que se quiere; entonces es un número que alguien eligió.

Los dos son independientes: una conexión se topa con el límite que encuentre primero,
y una conexión solo alcanza la edad si se usa, que es exactamente el
productor de larga vida que la edad existe para reequilibrar.

## Cuando el sink rechaza

El lote se reintenta —cuatro intentos en unos siete segundos por defecto,
duplicando con jitter— y luego va al **dead letter**, con el motivo
en cada registro:

```json
{
  "event_id": "dl-9",
  "ingestion_id": "84aaee4b-66af-5cc0-b97b-6856dec95b25",
  "_dead_letter_reason": "rpc error: code = NotFound desc = Topic not found",
  "_dead_letter_sink": "pubsub:acme-prod/clicks",
  "_dead_letter_at": "2026-09-25T02:02:32Z"
}
```

El motivo viaja **en el registro** y no solo en un log, porque quien
encuentre ese archivo más tarde tiene los eventos y no el log, y *por qué está esto aquí* es
su primera pregunta.

**Un stream sin `dead_letter` se rechaza al cargar.** Dejarlo en silencio por defecto
pondría la decisión donde nadie la toma, y un lote rechazado que es solo
una línea de log es perder datos en silencio.

## El hook es Go, compilado dentro

```go
hooks := gateway.NewHooks()
hooks.MustRegister("enrich_clicks", enrichClicks)
gateway.Main(hooks, gateway.WithSinks(sinks))

func enrichClicks(e map[string]any) (map[string]any, error) {
    host, _ := e["host"].(string)
    e["tenant"] = strings.Split(host, ".")[0]
    return e, nil     // returning nil DROPS the event, on purpose
}
```

El YAML nombra un hook; no lleva uno. Medido antes de elegir: una función de Go
cuesta **93 ns/op** frente a los 959 de Starlark y los 1.281 de yaegi, sin
añadir nada al binario, y `plugin.Open` no es una opción en absoluto, porque
bajo `CGO_ENABLED=0`, que es como se construye cada artefacto aquí, devuelve
`plugin: not implemented`.

Lo que cuesta, dicho donde alguien lo leerá: **añadir un hook es una recompilación
y un despliegue, no un cambio de configuración.** Es el intercambio correcto mientras los hooks
los escriben quienes publican el binario, y el equivocado el día en que un cliente
tiene que cambiar uno sin una release.

Un error en un hook envía *ese* evento por el camino del dead letter y nunca hace fallar
el lote que va a su lado. Un panic también: se recupera por evento, porque un
registro malformado no debe tumbar un proceso que sirve a todos los demás streams.

## Cuando un evento es demasiado grande

```yaml
oversize:
  larger_than: 256KiB
  archive: {type: files, path: gs://acme-oversize/clicks/}
  hook: strip_heavy_clicks
```

El evento se escribe en `archive` **completo**, y una versión reducida continúa por
el stream llevando un puntero de vuelta:

```json
{
  "event_id": "big-1",
  "ingestion_id": "bd8ac083-9796-50f5-bab6-77a4e2e57ad0",
  "_oversize": true,
  "_oversize_archive": "files:gs://acme-oversize/clicks/",
  "_oversize_bytes": 2149
}
```

Este es el patrón **Claim Check**, y es mejor que un `413` plano porque un `413`
pierde el evento, y un payload sobredimensionado suele ser el más interesante que
alguien tiene: es la petición con el documento completo adjunto, el
caso que vale la pena depurar.

`larger_than` mide un **evento**, después del hook. `listen.max_body` es
otra cosa: limita una **petición** completa y rechaza con `413` antes de parsear un
byte.

## Qué cuenta

Once series de Prometheus, en **su propio puerto**, nunca el de ingesta, y esa
regla importa más aquí que en el engine: el puerto de ingesta es público por
construcción, es donde los clientes hacen `POST`, así que un `/metrics` en él publicaría
cada nombre de stream, ruta y destino a quien encontrara la ruta. Nombrar
la misma dirección para ambos se rechaza al cargar.

```
brevis_gateway_events_received_total{stream,format}     counter
brevis_gateway_events_rejected_total{stream,reason}     counter  per event, after decode
brevis_gateway_requests_refused_total{stream,reason}    counter  per request: unauthorized|body_too_large|malformed|empty|saturated
brevis_gateway_batches_total{stream,sink,outcome}       counter  delivered|retried|buried
brevis_gateway_reconciled_total{stream,sink}            counter  landed after the send window; also delivered
brevis_gateway_flushes_total{stream,trigger}            counter  time|records|size
brevis_gateway_flush_windows_total{stream,outcome}      counter  won|yielded|ceiling|unreachable
brevis_gateway_ingested_bytes_total{stream,table}        counter  opt-in
brevis_gateway_ingested_events_total{stream,table}      counter  opt-in
brevis_gateway_table_flushes_total{stream,table,trigger} counter  opt-in
process_start_time_seconds                              gauge    unprefixed, on purpose
brevis_gateway_saturated_total{stream}                  counter  the 503s
brevis_gateway_delivery_seconds{stream,sink}            histogram
brevis_gateway_buffer_records{stream}                   gauge
brevis_gateway_queue_batches{stream}                    gauge
```

`process_start_time_seconds` **no** lleva el prefijo `brevis_`, y es
deliberado: los colectores buscan exactamente ese nombre. Dice cuándo arrancó el proceso,
y sin él un colector que hace ajuste por tiempo de inicio —el receptor
Prometheus de OpenTelemetry, sobre el que está construido Google Managed Prometheus—
ancla cada serie acumulativa en su **primer scrape** y gasta el valor de ese
scrape como base. El resultado es que cada contador se lee bajo, con
los deltas correctos y los totales equivocados.

Las últimas tres son las que nadie pide hasta después del primer incidente.
`saturated_total` es el único número que indica que a un cliente se le dijo que se retirara;
`buffer_records` frente a `buffer.max_records` es el único que lo **predice**.

`reason` es un conjunto cerrado y nunca el texto del error: un string de error lleva un
nombre de tabla, una columna, a veces una fila, y un cliente malformado crearía una serie
nueva por petición. El texto permanece en el dead letter, en el registro.

## `202`, no `200`

El gateway ha **aceptado** los eventos. Con `durability: memory` —el único
nivel implementado— eso es todo lo que puede afirmar honestamente. El nivel
que se gana un `200` es el que ya los ha escrito, y ese no
existe.

`disk` se rechaza **por su nombre**, no se acepta y se trata como memory: quien
escribe `disk` cree que sus eventos sobreviven a un crash, y estar de acuerdo sin
cumplir es exactamente el fallo que ese campo existe para evitar.

Lo que hay en un buffer al apagarse se entrega, no se descarta, por el mismo
camino que sigue un lote lleno, con reintentos y dead letter.

## Lo que no hace

- **No aparece en la console.** No comparte base de datos con la interfaz del engine,
  así que no hay página `/ingestion`, y la evolución de esquema que
  realiza `auto_table` tampoco aparece allí. Ese es el siguiente paso y
  no se ha escrito.
- **Un stream con `table` no evoluciona el esquema.** Cuando se nombra la tabla,
  el contrato es el de la tabla: un campo que no tiene se rechaza con el
  mensaje que lo soluciona, en lugar de alterar una tabla que alguien revisó. Hacer crecer
  una columna por cuenta propia es lo que hace `auto_table`, y es la diferencia
  entre ambos.
- **Un stream con `table` no crea ninguna tabla ni índice.** Un servicio que
  crea tablas convierte un error tipográfico en una segunda tabla que nadie lee, razón
  por la cual la creación ocurre solo bajo `auto_table`, donde `naming` acota qué
  nombres pueden existir y `listen.auth` es obligatorio.
- **La imagen publicada no tiene hooks**, que es el artefacto honesto para un diseño
  de hooks compilados: sirve streams que no declaran `hook:`.

## Una ruta, N tablas

`auto_table` enruta cada evento a la tabla que nombra el **envelope**, crea esa
tabla si no existe, y agrega una columna cuando aparece un campo nuevo:

```json
{"table_name": "app_orders", "operation": "INSERT", "unique_key": "id",
 "data": {"id": "A-1", "total": 150, "customer": {"id": 7, "uf": "SP"}}}
```

Campos de control arriba, el registro dentro de `data`, y siete columnas de seguimiento
`brevis_*` en cada tabla. Una ruta, N tablas, nada declarado.

Está en [Sinks de ingesta](/es/docs/ingestion-sinks/#one-route-n-tables-nothing-declared).

## Adónde ir después

| si quieres | ve a |
|---|---|
| los destinos y cómo escribe cada uno | [Sinks de ingesta](/es/docs/ingestion-sinks/index.md) |
| el `ingestion_id` desde el otro lado | [SDK de Go](/es/docs/sdk/index.md) |
| dashboards y alertas | [Observabilidad](/es/docs/observability/index.md) |
