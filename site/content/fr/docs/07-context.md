---
title: Contexte entre steps
description: Ce qu'un step dit au suivant — deux variables d'environnement et un fichier.
group: Concepts
order: 7
slug: context
---

Un step publie des valeurs ; les steps qui en dépendent les lisent. C'est tout ce
que fait cette fonctionnalité.

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

La clé est toujours `<step id>.<name>`. Un step lit ce que **ses propres
dépendances** ont publié, et rien d'autre.

:::note Ce que ce n'est pas
Ce n'est ni une file de messages, ni un état entre runs, ni un endroit où mettre
des données. C'est la note qu'un step laisse au suivant — un chemin, un
watermark, un compteur.
:::

## N'importe quel langage, aucune bibliothèque requise

Le contrat tient en **deux variables d'environnement et un fichier**, de sorte
qu'un step sans aucun SDK peut y participer :

```bash
window=$(jq -r '.extract.watermark' <<< "$BREVIS_INPUT")
./publish --until "$window"
jq -n --arg n "$(date -Is)" '{published_at: $n}' > "$BREVIS_OUTPUT"
```

| | |
|---|---|
| `BREVIS_INPUT` | `{"<step id>": {…}}` — ce que les dépendances de ce step ont publié |
| `BREVIS_OUTPUT` | un chemin où écrire **un** objet JSON |

Sous Kubernetes, ce chemin est `/dev/termination-log`, que le moteur lit déjà
pour obtenir le code de sortie. Il n'y a donc **aucun appel d'API, aucun jeton
et aucun port** — et une bibliothèque pour cela se réduit à un parse JSON et une
écriture de fichier.

| langage | |
|---|---|
| Python | [`pip install brevis`](/docs/python/) — elle apporte aussi `brevis.run`, avec les [paramètres automatiques](/docs/parameters/) |
| Go | `sdk/context`, qui fait partie du module du SDK, ne dépend de rien |
| tout autre | les deux variables ci-dessus |

## Isolation

Aucun step ne peut écraser ce qu'un autre a publié. Les clés sont préfixées par
l'id de celui qui les a écrites, et c'est le moteur — pas le step — qui décide
de ce préfixe. Deux steps qui publient `bucket` produisent `extract.bucket` et
`load.bucket`, et aucun ne disparaît.

## Le plafond de 4096 octets

Ce qu'un step publie tient dans **4096 octets**. La limite n'est pas arbitraire :
sous Kubernetes, le transport est `/dev/termination-log`, et c'est ce que le
kubelet accepte.

Dépasser est une **erreur du step**, avec un message indiquant combien d'octets
ont été écrits. L'alternative serait la troncature — et un JSON tronqué est un
JSON invalide, découvert par le step suivant, qui n'a aucun moyen de savoir que
la faute n'est pas la sienne.

Si une valeur ne tient pas, ce n'est pas du contexte : écrivez-la dans un bucket
et publiez le chemin.

## Nouvelles tentatives et runs repris

À une nouvelle tentative, le step publie de nouveau et la valeur précédente est
remplacée. Un step qui a déjà réussi ne se relance ni ne republie — ce qu'il a
dit reste valable pour ceux qui en dépendent.

## Exécuter un step à la main

Les deux variables constituent tout le contrat, donc reproduire un run revient à
les exporter :

```bash
export BREVIS_INPUT='{"extract":{"bucket":"s3://landing/2026-09-07"}}'
export BREVIS_OUTPUT=/tmp/out.json
python transform.py
cat /tmp/out.json
```

## Étapes suivantes

- [Paramètres](/docs/parameters/) — ce qui change d'un run à l'autre, et les paramètres automatiques
- [Runtime d'un step](/docs/runtime/) — ce qu'un step déclare sur l'endroit où il s'exécute
- [Python](/docs/python/) — la bibliothèque cliente, et les autres langages
