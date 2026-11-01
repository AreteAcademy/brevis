---
title: Un pod par step
description: Pourquoi chaque step s'exécute dans son propre pod, et ce que cela change en coût et en isolation.
group: Concepts
order: 6
slug: pod-per-step
---

Sur Kubernetes, **chaque step de workflow devient un pod** avec l'image déclarée
dans ce step. Il n'y a pas de worker générique qui attend du travail : le travail
apporte son propre runtime.

![Schéma : le Core démarre un pod par step — extract en Go, prepare en Python, load en Go et transform avec dbt. Les steps se transmettent du contexte, et le SDK est ce qui le lit et l'écrit.](/assets/execution.svg)

```
scheduler                          cluster
─────────                          ───────
walks the graph
  for each ready step:
    builds the Pod  ───────────►  Pod: the step's image, the step's command
    follows status      ◄───────  Pending → Running → Succeeded/Failed
    follows the log     ◄───────  container stdout
    reads the exit code ◄───────  containerStatuses[0].terminated
    deletes the pod ───────────►  (gone)
```

## Ce que cela change

Un worker monolithique force l'image à contenir tout ce dont n'importe quel step
pourrait avoir besoin. En pratique :

| | image unique | un pod par step |
|---|---|---|
| step dbt | 1,9 Go · 1Gi RAM | 1,9 Go · 1Gi RAM |
| fetcher Go à côté | **1,9 Go · 1Gi** | **12 Mo · 32Mi** |
| changer la version de dbt | tout reconstruire | une ligne dans le YAML de ce workflow |
| un step qui fuit de la mémoire | emporte le worker et ses voisins | meurt seul |

La troisième ligne est la moins évidente et la plus coûteuse au quotidien : avec
une image unique, passer dbt de 1.10 à 1.11 dans un pipeline impose le passage
dans tous.

## Images par rôle

Les images sont par **rôle**, pas par projet. Tout ce qui reste en trop dans une
image est tiré par chaque nœud qui exécute ce step :

| rôle | taille | démarrage à froid |
|---|---|---|
| **Go** | **5,8 Mo** | 0,18 s |
| **Python** | **118 Mo** | 0,54 s |
| **dbt** | **620 Mo** | 3,3 s |

Avant : une seule image de 1,87 Go pour tout.

Pour dbt, `dbt parse` s'exécute au moment du build et
`target/partial_parse.msgpack` voyage dans l'image — **2,68 s de moins par pod**,
à chaque invocation.

## Où chaque step s'exécute

La décision est prise une seule fois, au démarrage, et dépend de `BREVIS_PODS` et
de la présence ou non d'un cluster :

| `BREVIS_PODS` | avec cluster | sans cluster |
|---|---|---|
| `auto` (défaut) | un step avec `image:` devient un pod | tout en processus locaux, avec un avertissement dans le log |
| `on` | un step avec `image:` devient un pod | **erreur au démarrage** |
| `off` | tout en processus locaux | tout en processus locaux |

**Un step avec `host:` ne passe par rien de tout cela.** Il est examiné avant le
pod et avant le processus, et va vers une machine déjà en marche —
`BREVIS_PODS` ne s'applique pas à lui, et il ne retombe jamais sur un processus
local. C'est voulu : `image:` se dégrade en exécution locale avec un
avertissement parce qu'un conteneur et un processus sont la même commande sous
deux enveloppes, et qu'un hôte n'est pas cela. C'est là que vit la licence, ou le
GPU, ou les données.

`auto` est la valeur par défaut parce que le même binaire s'exécute aux deux
endroits : sur un portable, aucun compte de service n'est monté et il retombe sur
des processus locaux ; dans un cluster, il y en a un, et il se met à créer des
pods.

`on` existe pour le déploiement qui **ne doit pas** devenir silencieusement une
exécution locale. Là, l'absence de cluster doit être une erreur au démarrage.

:::warning `brevis run` ne crée aucun pod
La commande `run` ne construit que l'exécuteur de processus. Un step avec
`image:` s'exécute sur l'instance elle-même — et le dit. Se taire donnerait
l'impression que le step s'est exécuté sur l'image déclarée, le genre d'erreur
qui n'apparaît que lorsque le résultat est déjà faux.
:::

## Identité et identifiants

L'identité avec laquelle les pods démarrent est une décision d'**installation**,
pas de workflow — un pipeline ne doit pas pouvoir choisir le compte de service
sous lequel il s'exécute. Elle vient donc de l'environnement :

```bash
BREVIS_POD_NAMESPACE=data
BREVIS_POD_SERVICE_ACCOUNT=brevis-runner
BREVIS_POD_PULL_SECRETS=registry-cred
BREVIS_POD_ENV_FROM_SECRETS=bigquery-cred,api-tokens
```

En mode pod, l'environnement de la tâche vient des Secrets du cluster — pas de
`BREVIS_TASK_ENV`.

## Node selector et tolerations

```bash
BREVIS_POD_NODE_SELECTOR='kubernetes.io/arch=arm64'
BREVIS_POD_TOLERATIONS='dedicated=data:NoSchedule'
```

Seul l'opérateur `Equal` est accepté dans les tolerations : `Exists` tolérerait
**n'importe quel** taint portant cette clé, trop large pour une décision venant
d'une variable d'environnement.

Une toleration mal formée est **ignorée** plutôt que transformée en erreur au
démarrage — elle laisse le pod en `Pending`, ce qui est visible, alors que
refuser de démarrer arrêterait aussi les workflows qui n'ont pas besoin de ce
pool.

## Déboguer un échec

```bash
BREVIS_POD_KEEP_ON_FAILURE=true
```

Conserve le pod en échec pour `kubectl logs` et `kubectl describe`. Laissez-le
désactivé en production : les pods arrêtés consomment du quota du cluster.

## Étapes suivantes

- [Kubernetes](/docs/kubernetes/) — le déploiement complet
- [Configuration](/docs/configuration/) — toutes les variables `BREVIS_POD_*`
- [Observabilité](/docs/observability/) — les métriques des deux processus
