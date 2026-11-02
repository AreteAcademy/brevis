# Benchmarks

> Cómo medimos el gateway, qué dicen los números y los tres testigos que tienen que coincidir.

*https://brevis.sh/es/docs/benchmarks/ · brevis.sh docs (es)*

---

Un comando, y mide todo el gateway:

```bash
BREVIS_BENCH_IMAGE=brevissh/brevis-gateway:0.11.0 ./bench/run.sh
```

Levanta un Postgres, inicia el gateway, lo carga con [k6], **espera a que
el buffer se vacíe**, scrapea el propio `/metrics` del gateway y escribe
`bench/results/REPORT.md`.

## Tres testigos, y el tercero es el que importa

k6 sabe lo que vio un productor. El gateway sabe lo que hizo. Una prueba de carga
que lleva solo el primero no puede distinguir un gateway rápido de uno que responde `202`
y descarta lo que aceptó.

Por eso el reporte pone ambos lado a lado, y cuenta las filas en Postgres:

| testigo | eventos |
|---|---:|
| a k6 se le dijo `accepted` | 5.431.000 |
| el gateway contó recibidos | 5.431.000 |
| filas en Postgres | 5.431.000 |
| registros en el dead letter | 0 |

Los dos primeros son aritmética del propio gateway —uno de la respuesta que
escribió, otro del contador que incrementó—, y un bug que perdiera eventos podría
mantenerlos coherentes **entre sí**. La base de datos es la única que no
nos tomó la palabra.

**Un run en el que los tres discrepan falló**, por buenos que sean los percentiles.

## El run de arriba

El run del 2026-09-26 en `bench/results/REPORT.md`:
`brevissh/brevis-gateway:0.11.0`, Darwin arm64, 11 CPUs, Postgres 17 en un
contenedor sobre el mismo kernel, `auto_table[columns]` con `write: append`:

| | |
|---|---:|
| eventos aceptados | **5.431.000** (135.768/s) |
| cuerpo enviado | 1.156,7 MiB en 40 s |
| `503` (backpressure) | 0 |
| p50 / p95 / p99 | 5,9 ms / 16,0 ms / 22,7 ms |
| lotes entregados | 1.087, ninguno enterrado |

## Los umbrales son lo importante

k6 sale con código distinto de cero cuando uno falla, así que esto es una **compuerta** y no un gráfico:

| umbral | por qué |
|---|---|
| `p95 < 500ms` | el camino de aceptación debe mantenerse al margen de la latencia del sink. Una petición que espera un `COPY` es el defecto contra el que existe el pipe asíncrono. |
| `http_req_failed < 1%` | un `503` está bien; un timeout o un 5xx no. |
| `events accepted > 1000` | una suite de umbrales pasa contra un gateway que no respondió nada. |

Son deliberadamente laxos: esto corre en la máquina que le tocó al CI. Detectan
**un orden de magnitud**, que es el aspecto de una regresión, no un
porcentaje.

**Un `503` no es un fallo.** Es el gateway rechazando un evento para el que no tiene
dónde ponerlo, y es seguro reintentar: el `ingestion_id` es una función
congelada del evento, así que el mismo cuerpo enviado de nuevo es el mismo registro. Contarlo
como error haría que el backpressure pareciera un bug.

## Lo que no mide

- **No es BigQuery.** El destino es un Postgres en un contenedor. La cuota de
  load jobs de un warehouse, la latencia de commit y la concurrencia de DDL no están en estos números
  y no pueden estarlo.
- **No es durabilidad.** `durability: memory` es el único nivel, así que un `202` significa
  *aceptado en RAM*. Esto mide qué tan rápido ocurre eso, nunca qué sobrevive
  a un `SIGKILL`.
- **No es tu hardware.** Léase como una forma y una proporción.

## La nube local, por perfil

```bash
docker compose -f docker-compose.drivers.yml --profile gcp up -d
docker compose -f docker-compose.drivers.yml --profile all up -d
```

Un archivo con perfiles, no uno por nube: dos archivos son dos nombres de proyecto, y
`postgres` en ambos ya reemplazó una vez la base de datos de desarrollo del engine.

### floci cubre menos de BigQuery de lo que parece

Medido, no supuesto:

| | |
|---|---|
| crear un dataset y una tabla | ✅ |
| `timePartitioning` y `clustering` preservados | ✅ — la forma del bug de `0.9.1` |
| `PATCH` de una columna | ✅ — la evolución de `auto_table` |
| `insertAll` | ✅ |
| consultas | ✅, solo con `/var/run/docker.sock` montado |
| **load jobs** | ❌ `Only QUERY jobs are supported by the floci BigQuery emulator` |

El SDK escribe en BigQuery **únicamente** mediante load jobs. Así que floci ejercita cada
línea de `auto_table` que convierte un payload en DDL, y **ni una escritura de Brevis**.
Esa sigue siendo la mitad sin prueba local hoy, y vale la pena
tenerla. Pero una suite en verde contra floci no significa que BigQuery funcione.

[k6]: https://k6.io
