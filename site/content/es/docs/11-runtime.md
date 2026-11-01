---
title: Runtime del step
description: Lo que un step declara sobre el lenguaje y las herramientas que maneja, y por qué rara vez hace falta.
group: Referencia
order: 11
slug: runtime
---

El grafo dibuja un chip en cada step indicando su lenguaje y las herramientas que maneja:

```
┌──────────────────────────────────────┐
│ ● transform            SDK v0.57.0   │
│   dbt build --select gold            │
│   ⬤ Python  ▢ dbt                    │
│   2m14s · attempt 1                  │
└──────────────────────────────────────┘
```

**La mayor parte del tiempo no se escribe nada.** El engine lee `run:` e `image:` y
lo deduce.

## Cuándo declararlo

Dos campos, ambos opcionales, ambos por step:

```yaml
steps:
  - id: fetch
    run: /opt/brevis/bin/fetch-weather
    runtime: go            # a compiled binary; the path says nothing

  - id: transform
    run: dbt build --select gold+
    tools: [dbt]           # what it drives, not what it is
```

| campo | |
|---|---|
| `runtime` | el lenguaje del step: `go`, `python`, `node`, `shell`… |
| `tools` | las herramientas que maneja el comando: `dbt`, `dlt`, `spark`… |

La regla práctica: **declararlo cuando la inferencia se equivoca.** Un `run:` que
apunta a un binario compilado no tiene forma de decir que es Go; un wrapper que llama a
`dbt` internamente no tiene forma de decir que maneja dbt.

## Por qué un vocabulario cerrado

Un id fuera del vocabulario se **rechaza en el publish**, nombrando los válidos.
La alternativa es un chip que se muestra en blanco en una pantalla tres días después, sin
nada que permita rastrearlo.

```
workflow "daily": step "transform": `runtime: pyton` is not valid
  (valid: go, python, node, shell, …)
```

## Para qué sirve el chip

No es decoración. Cuando un run falla a las 3 a. m., la primera pregunta es «¿qué
ejecuta este step?», y la respuesta suele vivir en otro repositorio.
El chip lo pone en el propio grafo, junto a la versión del SDK que usó el step,
que es lo que distingue «se rompió hoy» de «se rompe desde el cambio del martes».

## Siguientes pasos

- [Workflows](/docs/workflows/) — todos los campos de un step
- [Pod por step](/docs/pod-per-step/) — dónde se ejecuta realmente un step
- [Observabilidad](/docs/observability/) — las métricas que lo acompañan
