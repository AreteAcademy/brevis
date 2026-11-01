---
title: SQL
description: Archivos .sql simples se convierten en tablas y vistas, en orden de dependencia, como step de un workflow o por sí solos.
group: Modelado en SQL
order: 21
slug: sql
---

Sin plantillas: un modelo es un archivo que sigue siendo SQL válido, y el orden sale de leer el propio SQL. Postgres y BigQuery, bajo la misma suite de conformidad.

![El flujo de SQL: brevis-sql lee raw.orders, una tabla que aterrizó el SDK o el Gateway. El modelo staging.stg_orders se convierte en una vista sobre ella, y marts.orders en una tabla sobre esa vista; el orden sale de leer el SQL. Los mismos modelos se construyen en Postgres o en BigQuery.](/assets/flow-sql.svg)

## Seis comandos

| comando | qué hace |
|---|---|
| `brevis-sql compile` | parsea cada modelo y resuelve cada arista, **sin conectarse a nada** |
| `brevis-sql graph` | imprime las aristas inferidas, para que una equivocada se vea y no se descubra |
| `brevis-sql build` | crea o reemplaza cada modelo, en orden de dependencia |
| `brevis-sql test` | ejecuta las pruebas de cada modelo; cada una es un SELECT que no debe encontrar nada |
| `brevis-sql serve` | responde vistas previas y consultas de solo lectura por HTTP, de modo que una console no necesita credencial de warehouse |
| `brevis-sql generation bump` | registra que una tabla fue reconstruida, para que las respuestas en caché de `brevis-sql-api` sobre ella queden obsoletas; `build --generations-from` lo hace por cada modelo que construye |

`--select orders+` acota cualquiera de ellos a un modelo y todo lo que está aguas abajo de él.


## `brevis-sql serve` — leer un warehouse sin credencial

Las pantallas Datos y SQL de la console muestran lo que aterrizó y lo que contiene. Ninguna llega a un warehouse: le preguntan a este servicio, que corre a su lado y guarda la credencial que el engine deliberadamente no tiene.

```bash
brevis-sql serve --connections brevis.yaml --addr 127.0.0.1:8088
```

Un techo no es un presupuesto: `--max-bytes` acota una consulta, y `--budget` acota la suma por conexión por hora, comprobada contra la cotización, de modo que el rechazo llega antes de gastar el dinero. Solo acota warehouses que informan bytes, que son BigQuery y no Postgres; el banner de arranque indica qué postura está en vigor, incluida la de no tener ninguna.

**Solo es alcanzable lo que declara el archivo**, y por defecto nada de lo que hace es una escritura: cada sentencia se clasifica antes de ejecutarse, una lectura que escanearía más de `--max-bytes` se cotiza y se rechaza antes de que cueste nada, y al propio warehouse se le pide que rechace las escrituras. La línea de auditoría lleva un hash de la sentencia y nunca la sentencia.

**Ya no necesita una service account propia.** `brevis-sql build` crea tablas, así que su rol puede escribir, y un servicio que rechazara cualquier rol así significaba una segunda cuenta que crear, conservar y rotar. El rol ya no es lo único que sostiene la línea: Postgres ejecuta cada sentencia dentro de una transacción abierta `READ ONLY` —donde `SELECT nextval('s')`, una escritura que ningún clasificador puede ver por dentro, vuelve rechazada por el propio Postgres— y BigQuery informa de qué tipo es la sentencia en el dry run que la cotiza, de modo que un `CREATE TABLE` o un `EXPORT DATA` se rechaza por el veredicto del propio warehouse. Un warehouse que no puede ofrecer ninguna de las dos cosas igualmente rechaza una credencial que pueda escribir.

Fuera de `BREVIS_ENV=local` se requiere un token y el servicio no arrancará sin uno.

### Un warehouse de desarrollo que alguien puede modificar

```bash
BREVIS_SQL_SERVE_WRITES=warehouse_dev
```

**Nombra conexiones.** Un servicio alcanza varios warehouses, y desbloquear aquel contra el que se desarrolla no debe desbloquear el que está al lado. En una conexión nombrada todas las capas se retiran y este servicio es exactamente tan poderoso como su credencial: el techo de filas acota una pantalla y el techo de bytes acota el dinero, y ninguno acota lo que una sentencia modifica.

Se requiere un token dondequiera que se defina, y `BREVIS_ENV=local` no es una excepción. El banner de arranque nombra cada conexión desbloqueada, la console dibuja un banner sobre el editor, y `brevis_sql_writable_connection` aparece en las métricas. **Déjese sin definir en producción**; ningún manifiesto de este repositorio lo define.

Los tokens pueden tener **nombre**: `BREVIS_SQL_SERVE_TOKEN_CONSOLE=…` declara un llamador llamado `console`, y cada línea de auditoría que produce ese bearer lleva `"caller":"console"`. La identidad es la credencial y no un header que rellena el llamador, así que el log no puede decir nada que quien posee ese token no poseyera. Un token rechazado también deja una línea, y nunca registra lo que se presentó.

## Un modelo es un archivo que sigue siendo SQL válido

La configuración es un comentario de bloque al inicio, de modo que el archivo se abre en un editor
con resaltado, se ejecuta a mano en una console, y `psql -f` lo ejecuta.

```sql
/* brevis
materialized: table
tests:
  - not_null: [order_id]
  - unique: [order_id]
  - relationships: {column: customer_id, to: staging.stg_customers, field: customer_id}
*/
select * from staging.stg_orders
```

**Sin plantillas.** `models/<schema>/<name>.sql` es `<schema>.<name>`, y
las dependencias salen de leer el SQL y no de una llamada a función que
haya que escribir. `brevis-sql graph` imprime lo que se infirió; `depends_on:` en
la cabecera es la sobrescritura explícita para el raro fallo.

## De compile a test

Dos modelos: una vista que limpia `raw.orders`, y una tabla que depende de ella.

`models/staging/stg_orders.sql`:

```sql
/* brevis
materialized: view
*/
select order_id, customer_id, amount, ordered_at
from raw.orders
where amount is not null
```

`models/marts/orders.sql`:

```sql
/* brevis
materialized: table
tests:
  - not_null: [customer_id]
  - unique: [customer_id]
*/
select customer_id, count(*) as orders, sum(amount) as revenue
from staging.stg_orders
group by customer_id
```

`compile` y `graph` no se conectan a nada:

```bash
docker run --rm -v ./project:/project:ro areteacademy/brevis-sql:0.6.0 \
  compile --project /project
```

```text
2 models, 2 to build, 0 function(s)
  staging.stg_orders                 view  0 test(s)
  marts.orders                       table 2 test(s)
```

```bash
docker run --rm -v ./project:/project:ro areteacademy/brevis-sql:0.6.0 \
  graph --project /project
```

```text
staging.stg_orders
  └─ raw.orders (source)
marts.orders
  ├─ staging.stg_orders
```

`build` y `test`, contra un Postgres:

```bash
docker run --rm -v ./project:/project:ro \
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db areteacademy/brevis-sql:0.6.0 \
  build --project /project --dsn-from BREVIS_SQL_DSN
```

```text
  staging.stg_orders                 view  new   4ms
@brevis:{"type":"landed","target":"postgres://postgres/staging/stg_orders"}
  marts.orders                       table new   4ms
@brevis:{"type":"landed","target":"postgres://postgres/marts/orders","rows":2}
2 models built on postgres
```

```bash
docker run --rm -v ./project:/project:ro \
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db areteacademy/brevis-sql:0.6.0 \
  test --project /project --dsn-from BREVIS_SQL_DSN
```

```text
2 tests passed on postgres
```

La salida real, de la imagen publicada: compile y graph no se conectan a nada. Contra un Postgres, build y test terminan con «2 models built» y «2 tests passed».

### Cuando una prueba falla

Con un pedido que no tiene cliente en `raw.orders`, `build` pasa y la prueba `not_null` no. El fallo nombra el modelo, la columna, cuántas filas y **la consulta**, que es lo que se pega en una console y se va acotando:

```text
FAIL  marts.orders                   not_null         customer_id          1 row(s)
      SELECT COUNT(*) FROM (SELECT customer_id FROM marts.orders WHERE customer_id IS NULL) AS v
brevis-sql: 1 of 2 tests failed
```

El proceso sale con código 1, y el step del workflow falla con él.

## Las pruebas son SELECTs que devuelven filas que violan la regla

`not_null`, `unique`, `accepted_values`, `relationships`. Cero filas es un éxito, y no hay nada más que interpretar.

## Dice lo que escribió

Cada modelo materializado imprime la línea `@brevis:` que el engine de Brevis ya
lee, así que aparece en `/data` junto a todo lo demás (ver la salida de `build`
arriba). Una vista no informa conteo de filas en lugar de cero: no contiene ninguna, y
un cero ahí no se distingue de una tabla que realmente está vacía.

## Dos warehouses, una suite

PostgreSQL y BigQuery (`--dialect bigquery`), y ambos ejecutan la misma suite de
pruebas, escrita cuando había una única implementación, para que la segunda no
pudiera divergir en silencio.

BigQuery se autentica con Application Default Credentials: en una workload
identity, esa es la identidad del propio pod, y `--dsn-from` nombra la variable
que contiene el id del proyecto.

## 13 MB

Distroless, nonroot, sin shell. El cliente oficial de BigQuery son 31 MB y 526
paquetes en un binario vacío, así que esto habla con BigQuery por su endpoint REST
en su lugar: unas 200 líneas, con una prueba por cada forma en que podría estar en
silencio equivocado. Una compuerta en el repositorio rechaza ese cliente por su nombre, de modo que la decisión
sobreviva a una actualización rutinaria de dependencias.

## Verificado contra dbt

jaffle-shop, construido por esta imagen sobre los mismos datos semilla que usó dbt:

|  | resultado |
|---|---|
| filas y md5 de cada fila, por modelo | **idénticos, 13 de 13** |
| columnas y tipos, por modelo | **idénticos, 13 de 13** |
| 13 modelos, primer build | 1,56 s |

## Lo que no hace

**Sin compatibilidad con dbt**, y es una decisión y no una carencia. Alrededor de un
tercio de los modelos de un proyecto dbt real no usa nada más allá de `ref`, `source` y
`config`; el resto usa macros, y los paquetes son plantillas por diseño. Media
compatibilidad que nadie puede predecir es peor que ninguna.

## Adónde ir después

| si quieres | ve a |
|---|---|
| ver dónde encaja SQL entre las cuatro piezas | [Ecosistema](/docs/ecosystem/) |
| ejecutar brevis-sql como un step programado | [Core](/docs/core/) |
| aterrizar los datos que modelará | [SDK de Go](/docs/sdk/) o [Ingesta](/docs/ingestion/) |
