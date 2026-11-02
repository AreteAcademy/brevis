# Core

> El orquestador: programa runs, mantiene la cola en Postgres e inicia un pod por step, y nunca toca los datos.

*https://brevis.sh/es/docs/core/ · brevis.sh docs (es)*

---

Todo el workflow es un archivo versionado: revisar un pipeline pasa a ser revisar un diff. El Core programa, encola e inicia cada step con su propia imagen, y nunca toca los datos.

![El flujo del Core: un scheduler y una cola, mantenidos en Postgres, inician un pod por step del workflow — prepare, extract, validate y publish. Del Core solo salen líneas de control. Los datos van de una fuente HTTP al pod extract, que ejecuta el SDK, y de allí a BigQuery; ningún dato pasa por el Core.](/assets/flow-core.svg)

## Qué hace

- **Programa.** El scheduler lee la programación de cada workflow y crea los runs. Él y la cola son dos bucles independientes: uno atascado no detiene al otro — [Scheduler y cola](/es/docs/scheduler-and-queue/index.md).
- **Encola.** La cola vive en Postgres: quien ejecuta reclama un run pendiente, recorre el grafo y registra el estado de cada step.
- **Inicia un pod por step**, cada uno con su propia imagen — [Pod por step](/es/docs/pod-per-step/index.md).
- **Nunca toca los datos.** Del Core solo salen líneas de control: quien lee y escribe datos es el step, con el [SDK](/es/docs/sdk/index.md) o con el código que sea propio.

## Un workflow, de punta a punta

El workflow es un archivo YAML. Dos steps que dependen del mismo corren en paralelo:

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

Para ejecutarlo localmente, sin cola y sin clúster:

```bash
docker run --rm -v ./hello.yaml:/w/hello.yaml -w /w \
  brevissh/brevis:0.40.0-worker run hello.yaml
```

La salida:

```text
workflow hello (dag, 4 steps) in .
  ▶ prepare
    prepare | preparing
  ✓ prepare
  ▶ validate
  ▶ extract
    validate | validated
  ✓ validate
    extract | 42 extracted
  ✓ extract
  ▶ publish
    publish | published
  ✓ publish

workflow hello finished
```

La salida real, de la imagen publicada. brevis run se ejecuta localmente, sin cola; los pods los inicia el scheduler.

## Adónde ir después

| si quieres | ve a |
|---|---|
| el formato YAML completo | [Workflows](/es/docs/workflows/index.md) |
| quién crea los runs y quién los ejecuta | [Scheduler y cola](/es/docs/scheduler-and-queue/index.md) |
| por qué un pod por step | [Pod por step](/es/docs/pod-per-step/index.md) |
| lo que un step pasa al siguiente | [Contexto entre steps](/es/docs/context/index.md) |
| levantarlo todo y ver el grafo en la interfaz | [Inicio rápido](/es/docs/quickstart/index.md) |
