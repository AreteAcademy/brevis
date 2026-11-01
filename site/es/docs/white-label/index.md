# White label

> Cambiar el título, el lema, el logo y la paleta de la interfaz mediante un único archivo YAML.

*https://brevis.sh/es/docs/white-label/ · brevis.sh docs (es)*

---

La interfaz se puede personalizar mediante un archivo. Los colores sobrescriben las variables CSS
**en tiempo de ejecución**, así que cambiar el tema de un cliente no recompila nada.

## El archivo

Copiar `brand.example.yaml` a `brand.yaml` en la raíz, o apuntar
`BREVIS_BRAND_FILE` hacia él:

```yaml
titulo: Acme Data
subtitulo: Data platform

frase: |
  Measure before deciding.
  Decide before scaling.

cores:
  fundo:    "#f4efe4"
  tinta:    "#21180f"
  destaque: "#aa8450"
```

**Todos los campos son opcionales.** Lo que se omite conserva el valor por defecto, así que un archivo
con solo `titulo:` ya es válido.

El salto de línea de `frase` se conserva: marca el ritmo de la frase en la
barra lateral.

## Logo

```yaml
logo: https://example.com/mark.svg
```

Acepta una URL (`https://` o `http://`) o una ruta interna que empiece con `/`.
Si falta, usa el símbolo incorporado, que sigue el color del tema.

:::warning Un logo alojado en otro lugar depende de ese host
Si se cae, o si el clúster no tiene salida a internet, la imagen no
carga. El resto de la interfaz sigue funcionando, pero conviene sopesarlo al elegir:
el resto de la UI, fuentes y bundles incluidos, se sirve desde el propio binario
justamente para no depender de la red.
:::

## Validar

Un valor hexadecimal erróneo solo afloraría cuando el contenedor arrancara, y el mensaje
llegaría por el log del pod, lejos de quien editó el archivo. De ahí un
subcomando:

```bash
brevis marca brand.yaml
```

```
  ok    Acme Data · Data platform
        logo      /assets/logo.svg  (built-in symbol)
        accent    (the product's own)
        Powered by Brevis
```

Se puede invocar en el CI de la instalación y el error vuelve en el pull request.

:::note La ausencia significa cosas distintas
En `serve`, un archivo ausente significa «usar la identidad por defecto», y la API
arranca. En `marca`, un archivo ausente es un **error**: quien pidió validar una
ruta espera que le digan que no existe.
:::

## Comportamiento al arrancar

Un error de contenido —un color inválido, YAML roto— **no impide que la interfaz
arranque**. El proceso registra una advertencia y usa el tema por defecto. Derribar
la API por un color sería peor que servirla con el tema por defecto.

```
level=WARN msg="visual identity ignored" file=brand.yaml error="brand.yaml: colour accent: \"#gggggg\" is not a hex value (#rgb, #rrggbb or #rrggbbaa)"
```

## Lo que no se puede personalizar

La línea **«Powered by Brevis»** del pie de página no viene de la configuración:
viene del código.

## En Kubernetes

Montar el archivo desde un ConfigMap:

```yaml
volumes:
  - name: brand
    configMap: {name: brevis-brand}
containers:
  - name: api
    volumeMounts:
      - {name: brand, mountPath: /etc/brevis}
    env:
      - name: BREVIS_BRAND_FILE
        value: /etc/brevis/brand.yaml
```
