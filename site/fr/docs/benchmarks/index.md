# Benchmarks

> Comment nous mesurons le gateway, ce que disent les chiffres, et les trois témoins qui doivent concorder.

*https://brevis.sh/fr/docs/benchmarks/ · brevis.sh docs (fr)*

---

Une seule commande, et elle mesure tout le gateway :

```bash
BREVIS_BENCH_IMAGE=brevissh/brevis-gateway:0.11.0 ./bench/run.sh
```

Elle démarre un Postgres, lance le gateway, le charge avec [k6], **attend que le
buffer se vide**, relève le `/metrics` du gateway lui-même et écrit
`bench/results/REPORT.md`.

## Trois témoins, et le troisième est celui qui compte

k6 sait ce qu'un producteur a vu. Le gateway sait ce qu'il a fait. Un test de
charge qui ne porte que le premier ne peut pas distinguer un gateway rapide d'un
gateway qui répond `202` et abandonne ce qu'il a accepté.

Le rapport met donc les deux côte à côte — et compte les lignes dans Postgres :

| témoin | événements |
|---|---:|
| k6 a reçu `accepted` | 5 431 000 |
| le gateway a compté reçus | 5 431 000 |
| lignes dans Postgres | 5 431 000 |
| enregistrements dans la dead letter | 0 |

Les deux premiers sont l'arithmétique du gateway lui-même — l'un à partir de la
réponse qu'il a écrite, l'autre à partir du compteur qu'il a incrémenté — et un
bug qui perdrait des événements pourrait les garder cohérents **entre eux**. La
base de données est la seule qui n'ait pas pris notre parole pour argent comptant.

**Un run où les trois divergent a échoué**, quelle que soit la qualité des
percentiles.

## Le run ci-dessus

Le run du 2026-09-26 dans `bench/results/REPORT.md` :
`brevissh/brevis-gateway:0.11.0`, Darwin arm64, 11 CPU, Postgres 17 dans un
conteneur sur le même noyau, `auto_table[columns]` avec `write: append` :

| | |
|---|---:|
| événements acceptés | **5 431 000** (135 768/s) |
| corps envoyé | 1 156,7 MiB en 40s |
| `503` (backpressure) | 0 |
| p50 / p95 / p99 | 5,9 ms / 16,0 ms / 22,7 ms |
| lots livrés | 1 087, aucun enterré |

## Les seuils sont le point central

k6 quitte avec un code non nul quand l'un d'eux échoue : c'est donc une **barrière**
et non un graphique :

| seuil | pourquoi |
|---|---|
| `p95 < 500ms` | le chemin d'acceptation doit rester indépendant de la latence du sink. Une requête qui attend un `COPY` est le défaut contre lequel le tuyau asynchrone existe. |
| `http_req_failed < 1%` | un `503` passe ; un timeout ou un 5xx, non. |
| `events accepted > 1000` | une suite de seuils passe contre un gateway qui n'a rien répondu. |

Ils sont volontairement larges : cela s'exécute sur la machine que la CI a fournie.
Ils détectent **un ordre de grandeur**, ce à quoi ressemble une régression, pas un
pourcent.

**Un `503` n'est pas un échec.** C'est le gateway qui refuse un événement qu'il ne
sait où mettre, et il est sans risque de réessayer — l'`ingestion_id` est une
fonction figée de l'événement, donc le même corps renvoyé est le même
enregistrement. Le compter comme une erreur ferait passer la backpressure pour un
bug.

## Ce qu'il ne mesure pas

- **Pas BigQuery.** La destination est un Postgres dans un conteneur. Le quota de
  load jobs d'un entrepôt, la latence de commit et la concurrence des DDL ne sont
  pas dans ces chiffres et ne peuvent pas y être.
- **Pas la durabilité.** `durability: memory` est le seul niveau, donc un `202`
  signifie *accepté en RAM*. Cela mesure la vitesse à laquelle cela se produit,
  jamais ce qui survit à un `SIGKILL`.
- **Pas votre matériel.** Lisez-le comme une forme et un ratio.

## Le cloud local, par profil

```bash
docker compose -f docker-compose.drivers.yml --profile gcp up -d
docker compose -f docker-compose.drivers.yml --profile all up -d
```

Un seul fichier avec des profils, pas un par cloud : deux fichiers, ce sont deux
noms de projet, et `postgres` dans les deux a déjà une fois remplacé la base de
développement du moteur.

### floci couvre moins de BigQuery qu'il n'y paraît

Mesuré, pas supposé :

| | |
|---|---|
| créer un dataset et une table | ✅ |
| `timePartitioning` et `clustering` préservés | ✅ — la forme du bug de la `0.9.1` |
| `PATCH` d'une colonne | ✅ — l'évolution d'`auto_table` |
| `insertAll` | ✅ |
| requêtes | ✅, uniquement avec `/var/run/docker.sock` monté |
| **load jobs** | ❌ `Only QUERY jobs are supported by the floci BigQuery emulator` |

Le SDK n'écrit dans BigQuery **que** par des load jobs. Donc floci exerce chaque
ligne d'`auto_table` qui transforme un payload en DDL — et **pas une seule écriture
de Brevis**. C'est quand même la moitié qui n'a aujourd'hui aucun test local, et
elle mérite d'être couverte. Mais une suite verte contre floci ne signifie pas que
BigQuery fonctionne.

[k6]: https://k6.io
