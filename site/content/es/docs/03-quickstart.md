---
title: Inicio rápido
description: De cero a un DAG en vivo en la interfaz, en cuatro pasos.
group: Primeros pasos
order: 3
slug: quickstart
---

Esta guía levanta brevis.sh, publica un workflow y lo ejecuta. Al final se
ve el grafo con el estado en vivo de cada step.

Requisito: un PostgreSQL accesible y la variable `BREVIS_DATABASE_URL`.

## 1. Aplicar el esquema

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
brevis migrate up
```

## 2. Escribir un workflow

Crear `hello.yaml`. Este ejemplo usa solo shell, así que corre en cualquier parte:

```yaml
name: hello
type: dag
tags: [example]

steps:
  - id: prepare
    run: sh -c 'echo preparing; sleep 1'

  # The next two depend on the same step, so they run IN PARALLEL.
  - id: extract
    run: sh -c 'sleep 2; echo 42 extracted'
    depends_on: [prepare]

  - id: validate
    run: sh -c 'sleep 1; echo validated'
    depends_on: [prepare]

  - id: publish
    run: echo published
    depends_on: [extract, validate]
```

Verificarlo antes de publicar; esto no toca la base de datos:

```bash
brevis validate hello.yaml
```

```text
  ok    hello                        dag  4 steps, 4 dependencies  (manual)
```

## 3. Ejecutarlo ahora, sin la cola

El camino más corto para ver el grafo funcionando:

```bash
brevis run hello.yaml
```

```text
workflow hello (dag, 4 steps) in .
  ▶ prepare
    prepare | preparing
  ✓ prepare
  ▶ extract
  ▶ validate
    validate | validated
  ✓ validate
    extract | 42 extracted
  ✓ extract
  ▶ publish
    publish | published
  ✓ publish

workflow hello finished
```

`extract` y `validate` arrancan juntos porque dependen del mismo step.
El runner recorre el grafo **por niveles**: todo lo que está dentro de un nivel corre en
paralelo, y el siguiente nivel empieza solo cuando el anterior termina por completo.

:::note `run` es local por naturaleza
`brevis run` solo opera con `BREVIS_ENV=local` (vacío cuenta como local) y
ejecuta los steps como procesos en la propia instancia, incluso aquellos que declaran
`image:`. Los pods los crea el `scheduler`.
:::

## 4. Publicar y operar

Para que el workflow exista en la base de datos, aparezca en la interfaz y siga su
programación:

```bash
brevis publish hello.yaml
```

```text
  published  hello                      (manual)
```

Levantar la interfaz y el scheduler en dos terminales:

```bash
brevis serve
```

```bash
brevis scheduler --interval 5s --concurrency 4
```

Abrir `http://localhost:8080`, buscar `hello` en la lista de workflows y hacer clic en ▶. El
grafo muestra cada step cambiando de estado en vivo.

## Qué pasó

| | |
|---|---|
| `migrate up` | creó las tablas |
| `validate` | verificó el YAML sin tocar la base de datos |
| `run` | se ejecutó en la propia instancia, sin cola |
| `publish` | almacenó el workflow y su programación |
| `serve` | levantó la API y la interfaz |
| `scheduler` | materializó la programación y vació la cola |

## Siguientes pasos

- [Workflows](/docs/workflows/) — el formato YAML completo
- [Parámetros](/docs/parameters/) — lo que cambia entre dos runs
- [Pod por step](/docs/pod-per-step/) — cómo esto se convierte en Kubernetes
