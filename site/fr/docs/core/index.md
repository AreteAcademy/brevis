# Core

> L'orchestrateur : planifie les runs, garde la file dans Postgres et démarre un pod par step — sans jamais toucher aux données.

*https://brevis.sh/fr/docs/core/ · brevis.sh docs (fr)*

---

Tout le workflow tient dans un fichier versionné : relire un pipeline revient à relire un diff. Le Core planifie, met en file et démarre chaque step avec sa propre image — sans jamais toucher aux données.

![Le flux du Core : un scheduler et une file, conservés dans Postgres, démarrent un pod par step du workflow — prepare, extract, validate et publish. Seules des lignes de contrôle sortent du Core. Les données vont d'une source HTTP vers le pod extract, qui exécute le SDK, puis de là vers BigQuery ; aucune donnée ne passe par le Core.](/assets/flow-core.svg)

## Ce qu'il fait

- **Planifie.** Le scheduler lit la planification de chaque workflow et crée les runs. Lui et la file sont deux boucles indépendantes : l'une bloquée n'arrête pas l'autre — [Scheduler et file](/fr/docs/scheduler-and-queue/index.md).
- **Met en file.** La file vit dans Postgres : celui qui exécute réclame un run en attente, parcourt le graphe et enregistre l'état de chaque step.
- **Démarre un pod par step**, chacun avec sa propre image — [Un pod par step](/fr/docs/pod-per-step/index.md).
- **Ne touche jamais aux données.** Seules des lignes de contrôle sortent du Core : ce qui lit et écrit les données, c'est le step, avec le [SDK](/fr/docs/sdk/index.md) ou n'importe quel code qui est le vôtre.

## Un workflow, de bout en bout

Le workflow est un fichier YAML. Deux steps qui dépendent du même step s'exécutent en parallèle :

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

Pour l'exécuter en local, sans file ni cluster :

```bash
docker run --rm -v ./hello.yaml:/w/hello.yaml -w /w \
  areteacademy/brevis:0.40.0-worker run hello.yaml
```

La sortie :

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

La sortie réelle, issue de l'image publiée. brevis run s'exécute en local, sans file ; les pods sont démarrés par le scheduler.

## Où aller ensuite

| si vous voulez | allez à |
|---|---|
| tout le format YAML | [Workflows](/fr/docs/workflows/index.md) |
| qui crée les runs et qui les exécute | [Scheduler et file](/fr/docs/scheduler-and-queue/index.md) |
| pourquoi un pod par step | [Un pod par step](/fr/docs/pod-per-step/index.md) |
| ce qu'un step transmet au suivant | [Contexte entre steps](/fr/docs/context/index.md) |
| tout démarrer et voir le graphe dans l'interface | [Démarrage rapide](/fr/docs/quickstart/index.md) |
