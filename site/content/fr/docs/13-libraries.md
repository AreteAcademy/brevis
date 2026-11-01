---
title: Bibliothèques clientes
description: Comment un step, dans n'importe quel langage, lit le contexte et l'horloge du run — et quels langages ont déjà une bibliothèque.
group: SDK et bibliothèques
order: 13
slug: libraries
---

Un step a besoin de deux choses de l'orchestrateur : **ce que le step précédent a
publié** et **quelle horloge ce run doit lire**. Les bibliothèques clientes
fournissent ces deux-là, et rien d'autre.

## Le contrat précède les bibliothèques

Ce que le moteur définit, ce sont **deux variables d'environnement et un
fichier** :

| | |
|---|---|
| `BREVIS_INPUT` | `{"<step id>": {…}}` — ce que les dépendances de ce step ont publié |
| `BREVIS_OUTPUT` | un chemin où écrire **un** objet JSON |

Plus les variables du run et les paramètres automatiques, tous en lecture seule :
`BREVIS_RUN_*` et `BREVIS_AUTO_*`. Sous Kubernetes, `BREVIS_OUTPUT` est
`/dev/termination-log`, que le moteur lit déjà pour obtenir le code de sortie.

Il n'y a donc **aucun appel d'API, aucun jeton et aucun port**. Un step dans
n'importe quel langage y participe avec un parse JSON et une écriture de fichier :

```bash
window=$(jq -r '.extract.watermark' <<< "$BREVIS_INPUT")
./publish --until "$window"
jq -n --arg n "$(date -Is)" '{published_at: $n}' > "$BREVIS_OUTPUT"
```

Une bibliothèque ici n'ouvre aucune nouvelle capacité — elle vous épargne la
validation, la gestion des valeurs absentes et les messages d'erreur qu'il vaut
mieux ne pas réécrire.

## Celles qui existent

| langage | | |
|---|---|---|
| **Python** | `pip install brevis` | [disponible](/docs/python/) — `0.2.1` sur PyPI |
| **Go** | `brevis/sdk/context` | disponible, dans le module du SDK |
| **Node.js** | — | prévu |
| **Rust** | — | prévu |
| tout autre | les variables ci-dessus | aujourd'hui |

:::note Go ne vit pas dans `lib/`
Le support du contexte en Go vit dans le module `sdk/` que le projet publie déjà,
sous forme de sous-package qui s'élague à rien pour quiconque ne l'importe pas. Un
`lib/go-context` signifierait un second module Go pour un package qui en a déjà
un.

La règle est **un répertoire par langage qui n'a pas déjà un artefact publié**.
:::

Chaque bibliothèque est un artefact séparé avec sa propre version et son propre
tag : un correctif dans le client Python n'impose pas de release du moteur.

## Ce qu'elles sont, et ce qu'elles ne sont pas

Ce sont des **clients légers au-dessus du contrat**, pas des portages du SDK Go.

Un portage de la mécanique ETL du SDK — drivers, pagination, checkpoints, ids
d'ingestion — n'a pas sa place ici et probablement nulle part : chaque langage
visé a déjà de meilleurs outils pour cela, et le travail de Brevis est de les
exécuter. C'est pourquoi la bibliothèque Python existe : pour qu'une équipe puisse
utiliser pandas, Polars ou dbt tout en obtenant la seule chose pour laquelle un
orchestrateur existe.

## Métriques

La bibliothèque **émet** des métriques, et c'est le moteur qui les expose :

```python
from brevis import metrics

metrics.set("rows_loaded", 48213)      # a gauge: the last value wins
metrics.inc("vendor_rejected_total")   # a counter: it adds up
```

Elles ressortent du `/metrics` **du moteur**, sur le port du scheduler, étiquetées
avec le workflow et le step — voir [Observabilité](/docs/observability/).

**Cela n'ouvre pas de port, et ne le pourrait pas.** Un step s'exécute dans son
propre pod pendant quarante secondes et s'arrête ; un port qu'il ouvrirait ne
serait jamais scruté, ou une fois par chance. La ligne va donc sur stdout — le
même canal que le moteur lit déjà pour dessiner les phases d'un pipeline — et le
moteur l'enregistre. Rien à installer, rien à opérer, et les étiquettes sont celles
du moteur parce qu'un step ne peut pas connaître le slug de son propre workflow.

Les détails de l'API sont dans [Python](/docs/python/#metrics-only-your-pipeline-knows).

## Écrire un client pour un autre langage

Si vous arrivez à Node.js ou Rust avant nous, voici ce qu'un client doit réussir —
et ce que l'implémentation Python a déjà appris à ses dépens :

1. **Des clés qualifiées par le step.** `get("bucket")` sans step doit être
   refusé. Deux steps peuvent publier `bucket`, et en choisir un par recherche,
   c'est une priorité par accident.
2. **Fusionner les écritures.** Deux appels avec des clés différentes gardent les
   deux ; la même clé deux fois garde la dernière.
3. **N'écrire qu'à la sortie du processus.** Un step qui meurt brutalement ne
   publie rien — ce qui est correct, car la sortie d'un step planté décrivait un
   travail qui ne s'est pas terminé.
4. **Refuser au-delà de 4096 octets.** La plateforme **tronque** au lieu de
   refuser, et un objet coupé en deux arrive comme un JSON invalide pour le step
   suivant.
5. **Fonctionner hors de Brevis.** Sans `BREVIS_INPUT`, `get()` renvoie la valeur
   par défaut et `set()` accepte et jette, sans lever d'erreur : un script qui ne
   peut pas être exécuté à la main ne peut pas être développé.

## Étapes suivantes

- [Python](/docs/python/) — l'API complète
- [Contexte entre steps](/docs/context/) — le concept et le plafond de 4096 octets
- [Paramètres](/docs/parameters/) — les déclarés et les automatiques
