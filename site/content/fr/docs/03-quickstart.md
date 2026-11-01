---
title: Démarrage rapide
description: De rien à un DAG vivant dans l'interface, en quatre étapes.
group: Prise en main
order: 3
slug: quickstart
---

Ce guide démarre brevis.sh, publie un workflow et l'exécute. À la fin, vous
voyez le graphe avec l'état en direct de chaque step.

Prérequis : un PostgreSQL accessible et la variable `BREVIS_DATABASE_URL`.

## 1. Appliquer le schéma

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
brevis migrate up
```

## 2. Écrire un workflow

Créez `hello.yaml`. Cet exemple n'utilise que le shell, il s'exécute donc partout :

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

Vérifiez-le avant de publier — cela ne touche pas à la base de données :

```bash
brevis validate hello.yaml
```

```text
  ok    hello                        dag  4 steps, 4 dependencies  (manual)
```

## 3. L'exécuter maintenant, sans la file

Le chemin le plus court pour voir le graphe fonctionner :

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

`extract` et `validate` démarrent ensemble parce qu'ils dépendent du même step.
Le runner parcourt le graphe **par niveaux** : tout ce qui se trouve dans un
niveau s'exécute en parallèle, et le niveau suivant ne démarre que lorsque le
précédent est entièrement terminé.

:::note `run` est local par nature
`brevis run` ne fonctionne qu'avec `BREVIS_ENV=local` (une valeur vide compte
comme local) et exécute les steps comme des processus sur l'instance elle-même —
même ceux qui déclarent `image:`. Les pods sont créés par le `scheduler`.
:::

## 4. Publier et opérer

Pour que le workflow existe dans la base, apparaisse dans l'interface et suive sa
planification :

```bash
brevis publish hello.yaml
```

```text
  published  hello                      (manual)
```

Démarrez l'interface et le scheduler dans deux terminaux :

```bash
brevis serve
```

```bash
brevis scheduler --interval 5s --concurrency 4
```

Ouvrez `http://localhost:8080`, trouvez `hello` dans la liste des workflows et
cliquez sur ▶. Le graphe montre chaque step changer d'état en direct.

## Ce qui s'est passé

| | |
|---|---|
| `migrate up` | a créé les tables |
| `validate` | a vérifié le YAML sans toucher à la base de données |
| `run` | s'est exécuté sur l'instance elle-même, sans file |
| `publish` | a stocké le workflow et sa planification |
| `serve` | a démarré l'API et l'interface |
| `scheduler` | a matérialisé la planification et vidé la file |

## Étapes suivantes

- [Workflows](/docs/workflows/) — le format YAML complet
- [Paramètres](/docs/parameters/) — ce qui change d'un run à l'autre
- [Un pod par step](/docs/pod-per-step/) — comment cela devient du Kubernetes
