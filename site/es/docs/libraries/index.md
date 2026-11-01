# Bibliotecas cliente

> Cómo un step en cualquier lenguaje lee el contexto y el reloj del run, y qué lenguajes ya tienen una biblioteca.

*https://brevis.sh/es/docs/libraries/ · brevis.sh docs (es)*

---

Un step necesita dos cosas del orquestador: **lo que publicó el step
anterior** y **qué reloj debe leer este run**. Las bibliotecas cliente entregan
esas dos, y nada más.

## El contrato viene antes que las bibliotecas

Lo que el engine define son **dos variables de entorno y un archivo**:

| | |
|---|---|
| `BREVIS_INPUT` | `{"<step id>": {…}}` — lo que publicaron las dependencias de este step |
| `BREVIS_OUTPUT` | una ruta donde escribir **un** objeto JSON |

Más las variables del run y los parámetros automáticos, todos de solo lectura: `BREVIS_RUN_*`
y `BREVIS_AUTO_*`. En Kubernetes, `BREVIS_OUTPUT` es
`/dev/termination-log`, que el engine ya lee para obtener el código de salida.

Así que **no hay llamada a una API, ni token, ni puerto**. Un step en cualquier lenguaje participa
con un parseo de JSON y una escritura de archivo:

```bash
window=$(jq -r '.extract.watermark' <<< "$BREVIS_INPUT")
./publish --until "$window"
jq -n --arg n "$(date -Is)" '{published_at: $n}' > "$BREVIS_OUTPUT"
```

Una biblioteca aquí no abre ninguna capacidad nueva: ahorra la validación, el
manejo de valores ausentes y los mensajes de error que conviene no reescribir.

## Cuáles existen

| lenguaje | | |
|---|---|---|
| **Python** | `pip install brevis` | [disponible](/es/docs/python/index.md) — `0.2.1` en PyPI |
| **Go** | `brevis/sdk/context` | disponible, dentro del módulo del SDK |
| **Node.js** | — | planificada |
| **Rust** | — | planificada |
| cualquier otro | las variables de arriba | hoy |

:::note Go no vive en `lib/`
El soporte de contexto de Go vive en el módulo `sdk/` que el proyecto ya publica,
como un subpaquete que se poda a nada para quien no lo importa. Un
`lib/go-context` implicaría un segundo módulo de Go para un paquete que ya tiene uno.

La regla es **un directorio por lenguaje que aún no tiene un artefacto
publicado**.
:::

Cada biblioteca es un artefacto separado con su propia versión y tag: una corrección en el
cliente de Python no obliga a una release del engine.

## Qué son, y qué no son

Son **clientes delgados sobre el contrato**, no ports del SDK de Go.

Un port de la maquinaria ETL del SDK —drivers, paginación, checkpoints, ids de
ingesta— no pertenece aquí y probablemente a ningún lado: todos los lenguajes a los que apuntaría
ya tienen mejores herramientas para eso, y el trabajo de Brevis es ejecutarlas. Por
eso la biblioteca de Python existe: para que un equipo pueda usar pandas, Polars o dbt
y aun así obtener lo único para lo que sirve un orquestador.

## Métricas

La biblioteca **emite** métricas, y el engine las expone:

```python
from brevis import metrics

metrics.set("rows_loaded", 48213)      # a gauge: the last value wins
metrics.inc("vendor_rejected_total")   # a counter: it adds up
```

Salen del `/metrics` **del engine**, en el puerto del scheduler, etiquetadas
con el workflow y el step — ver [Observabilidad](/es/docs/observability/index.md).

**Esto no abre un puerto, y no podría.** Un step corre en su propio pod durante
cuarenta segundos y termina; un puerto que abriera nunca sería consultado, o lo sería una vez
por suerte. Así que la línea va a stdout —el mismo canal que el engine ya lee para
dibujar las fases de un pipeline— y el engine la registra. Nada que instalar,
nada que operar, y las etiquetas son las del engine porque un step no puede conocer
el slug de su propio workflow.

Los detalles de la API están en [Python](/es/docs/python/#metrics-only-your-pipeline-knows).

## Escribir un cliente para otro lenguaje

Si llegas a Node.js o Rust antes que nosotros, esto es lo que un cliente tiene que hacer
bien, y lo que la implementación de Python ya aprendió por las malas:

1. **Claves calificadas por step.** `get("bucket")` sin step debe rechazarse. Dos
   steps pueden publicar `bucket`, y elegir uno por búsqueda es precedencia por
   accidente.
2. **Combinar las escrituras.** Dos llamadas con claves distintas conservan ambas; la misma clave
   dos veces conserva la última.
3. **Volcar solo al salir el proceso.** Un step que muere de golpe no publica nada,
   lo cual es correcto, porque la salida de un step que falló describía trabajo que no
   terminó.
4. **Rechazar por encima de 4096 bytes.** La plataforma **trunca** en lugar de rechazar,
   y un objeto cortado por la mitad llega como JSON inválido al siguiente step.
5. **Funcionar fuera de Brevis.** Sin `BREVIS_INPUT`, `get()` devuelve el valor por defecto
   y `set()` acepta y descarta, sin lanzar excepción: un script que no puede
   ejecutarse a mano no puede desarrollarse.

## Siguientes pasos

- [Python](/es/docs/python/index.md) — la API completa
- [Contexto entre steps](/es/docs/context/index.md) — el concepto y el techo de 4096 bytes
- [Parámetros](/es/docs/parameters/index.md) — los declarados y los automáticos
