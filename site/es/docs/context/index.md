# Contexto entre steps

> Lo que un step le dice al siguiente: dos variables de entorno y un archivo.

*https://brevis.sh/es/docs/context/ · brevis.sh docs (es)*

---

Un step publica valores; los steps que dependen de él los leen. Eso es todo lo que
hace la funcionalidad.

```yaml
steps:
  - id: extract
    run: python fetch.py

  - id: transform
    run: python transform.py
    depends_on: [extract]
```

```python
# extract
from brevis import context
context.set(bucket="s3://landing/2026-09-07", rows=48213)
```

```python
# transform
from brevis import context
bucket = context.get("extract.bucket")
```

La clave siempre es `<step id>.<name>`. Un step lee lo que publicaron **sus propias
dependencias**, y nada más.

:::note Lo que esto no es
No es una cola de mensajes, ni estado entre runs, ni un lugar donde poner datos. Es
la nota que un step deja para el siguiente: una ruta, una marca de agua, un conteo.
:::

## Cualquier lenguaje, sin biblioteca obligatoria

El contrato son **dos variables de entorno y un archivo**, así que un step sin ningún SDK
puede participar:

```bash
window=$(jq -r '.extract.watermark' <<< "$BREVIS_INPUT")
./publish --until "$window"
jq -n --arg n "$(date -Is)" '{published_at: $n}' > "$BREVIS_OUTPUT"
```

| | |
|---|---|
| `BREVIS_INPUT` | `{"<step id>": {…}}` — lo que publicaron las dependencias de este step |
| `BREVIS_OUTPUT` | una ruta donde escribir **un** objeto JSON |

En Kubernetes esa ruta es `/dev/termination-log`, que el engine ya
lee para obtener el código de salida. Así que **no hay llamada a una API, ni token, ni puerto**,
y una biblioteca para esto es un parseo de JSON y una escritura de archivo.

| lenguaje | |
|---|---|
| Python | [`pip install brevis`](/es/docs/python/index.md) — también incluye `brevis.run`, con los [parámetros automáticos](/es/docs/parameters/index.md) |
| Go | `sdk/context`, parte del módulo del SDK, no depende de nada |
| cualquier otro | las dos variables de arriba |

## Aislamiento

Ningún step puede sobrescribir lo que otro publicó. Las claves llevan como prefijo el id de
quien las escribió, y el engine —no el step— decide ese prefijo. Dos
steps que publican `bucket` producen `extract.bucket` y `load.bucket`, y
ninguno desaparece.

## El techo de 4096 bytes

Lo que un step publica cabe en **4096 bytes**. El límite no es arbitrario: en
Kubernetes el transporte es `/dev/termination-log`, y eso es lo que el kubelet
acepta.

Excederlo es un **error del step**, con un mensaje que indica cuántos bytes se
escribieron. La alternativa sería truncar, y un JSON truncado es un JSON
inválido, que descubre el siguiente step, sin forma de saber que la falla no es
suya.

Si un valor no cabe, no es contexto: se escribe en un bucket y se publica la
ruta.

## Reintentos y runs reanudados

En un nuevo intento el step vuelve a publicar y el valor anterior se reemplaza. Un
step que ya tuvo éxito ni se reejecuta ni republica: lo que dijo sigue
valiendo para quienes dependen de él.

## Ejecutar un step a mano

Las dos variables son todo el contrato, así que reproducir un run es exportarlas:

```bash
export BREVIS_INPUT='{"extract":{"bucket":"s3://landing/2026-09-07"}}'
export BREVIS_OUTPUT=/tmp/out.json
python transform.py
cat /tmp/out.json
```

## Siguientes pasos

- [Parámetros](/es/docs/parameters/index.md) — lo que cambia entre runs, y los automáticos
- [Runtime del step](/es/docs/runtime/index.md) — lo que un step declara sobre dónde se ejecuta
- [Python](/es/docs/python/index.md) — la biblioteca cliente, y los otros lenguajes
