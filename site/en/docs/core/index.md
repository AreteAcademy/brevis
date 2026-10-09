# Core

> The orchestrator: schedules runs, keeps the queue in Postgres and starts one pod per step — and never touches the data.

*https://brevis.sh/en/docs/core/ · brevis.sh docs (en)*

---

The whole workflow is one versioned file: reviewing a pipeline becomes reviewing a diff. The Core schedules, queues and starts each step with its own image — and never touches the data.

![The Core flow: a scheduler and a queue, kept in Postgres, start one pod per step of the workflow — prepare, extract, validate and publish. Only control lines leave the Core. The data goes from an HTTP source into the extract pod, which runs the SDK, and from there to BigQuery; no data passes through the Core.](/assets/flow-core.svg)

## What it does

- **Schedules.** The scheduler reads each workflow's schedule and creates the runs. It and the queue are two independent loops: one jammed does not stop the other — [Scheduler and queue](/en/docs/scheduler-and-queue/index.md).
- **Queues.** The queue lives in Postgres: whoever executes claims a pending run, walks the graph and records each step's state.
- **Starts one pod per step**, each with its own image — [Pod per step](/en/docs/pod-per-step/index.md).
- **Never touches the data.** Only control lines leave the Core: what reads and writes data is the step, with the [SDK](/en/docs/sdk/index.md) or whatever code is yours.

## One workflow, end to end

The workflow is one YAML file. Two steps that depend on the same one run in parallel:

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

To run it locally, with no queue and no cluster:

```bash
docker run --rm -v ./hello.yaml:/w/hello.yaml -w /w \
  areteacademy/brevis:0.17.1-worker run hello.yaml
```

The output:

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

The real output, from the published image. brevis run executes locally, with no queue; pods are started by the scheduler.

## Where to go next

| if you want | go to |
|---|---|
| the whole YAML format | [Workflows](/en/docs/workflows/index.md) |
| who creates runs and who executes them | [Scheduler and queue](/en/docs/scheduler-and-queue/index.md) |
| why one pod per step | [Pod per step](/en/docs/pod-per-step/index.md) |
| what a step passes to the next | [Context between steps](/en/docs/context/index.md) |
| bring it all up and see the graph in the interface | [Quickstart](/en/docs/quickstart/index.md) |
