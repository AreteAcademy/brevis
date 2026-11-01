---
title: SQL
description: De simples fichiers .sql deviennent des tables et des vues, dans l'ordre des dépendances — comme step de workflow ou de façon autonome.
group: Modélisation en SQL
order: 21
slug: sql
---

Pas de templating : un modèle est un fichier qui reste du SQL valide, et l'ordre vient de la lecture du SQL lui-même. Postgres et BigQuery, sous la même suite de conformité.

![Le flux SQL : brevis-sql lit raw.orders, une table que le SDK ou le Gateway a fait atterrir. Le modèle staging.stg_orders devient une vue par-dessus, et marts.orders une table par-dessus cette vue ; l'ordre vient de la lecture du SQL. Les mêmes modèles se construisent sur Postgres ou BigQuery.](/assets/flow-sql.svg)

## Six commandes

| commande | ce qu'elle fait |
|---|---|
| `brevis-sql compile` | analyse chaque modèle et résout chaque arête, **sans se connecter à rien** |
| `brevis-sql graph` | affiche les arêtes inférées, pour qu'une arête erronée soit vue et non découverte |
| `brevis-sql build` | crée ou remplace chaque modèle, dans l'ordre des dépendances |
| `brevis-sql test` | exécute les tests de chaque modèle ; chacun est un SELECT qui ne doit rien trouver |
| `brevis-sql serve` | répond à des aperçus et requêtes en lecture seule via HTTP, pour qu'une console n'ait besoin d'aucun identifiant d'entrepôt |
| `brevis-sql generation bump` | enregistre qu'une table a été reconstruite, pour que les réponses en cache de `brevis-sql-api` sur elle deviennent périmées — `build --generations-from` le fait pour chaque modèle qu'il construit |

`--select orders+` restreint n'importe laquelle d'entre elles à un modèle et à tout ce qui se trouve en aval.


## `brevis-sql serve` — lire un entrepôt sans identifiant

Les écrans Data et SQL de la console montrent ce qui a atterri et ce qu'il contient. Ni l'un ni l'autre n'atteint un entrepôt : ils interrogent ce service, qui s'exécute à côté et détient l'identifiant que le moteur ne détient délibérément pas.

```bash
brevis-sql serve --connections brevis.yaml --addr 127.0.0.1:8088
```

Un plafond n'est pas un budget : `--max-bytes` borne une requête, et `--budget` borne la somme par connexion et par heure — vérifiée contre le devis, de sorte qu'un refus vient avant que l'argent soit dépensé. Il ne borne que les entrepôts qui rapportent des octets, c'est-à-dire BigQuery et non Postgres ; la bannière de démarrage dit quelle posture est en vigueur, y compris l'absence de toute posture.

**Seul ce que le fichier déclare est joignable**, et par défaut rien de ce qu'il fait n'est une écriture : chaque instruction est classée avant d'être exécutée, une lecture qui scannerait plus que `--max-bytes` est chiffrée et refusée avant de coûter quoi que ce soit, et l'entrepôt lui-même est prié de refuser les écritures. La ligne d'audit porte un hash de l'instruction et jamais l'instruction.

**Il n'a plus besoin d'un compte de service à lui.** `brevis-sql build` crée des tables, donc son rôle peut écrire, et un service qui refusait tout rôle de ce genre signifiait un second compte à créer, conserver et faire tourner. Le rôle n'est plus la seule chose qui tienne la ligne : Postgres exécute chaque instruction dans une transaction ouverte en `READ ONLY` — où `SELECT nextval('s')`, une écriture qu'aucun classificateur ne voit de l'intérieur, est refusé par Postgres lui-même — et BigQuery indique de quel genre d'instruction il s'agit lors du dry run qui la chiffre, de sorte qu'un `CREATE TABLE` ou un `EXPORT DATA` est refusé sur le verdict propre de l'entrepôt. Un entrepôt qui ne peut offrir ni l'un ni l'autre refuse quand même un identifiant capable d'écrire.

Hors de `BREVIS_ENV=local`, un jeton est requis et le service ne démarre pas sans.

### Un entrepôt de développement que quelqu'un peut modifier

```bash
BREVIS_SQL_SERVE_WRITES=warehouse_dev
```

Elle **nomme des connexions**. Un service atteint plusieurs entrepôts, et déverrouiller celui sur lequel vous développez ne doit pas déverrouiller celui d'à côté. Sur une connexion nommée, chaque couche se retire et ce service est exactement aussi puissant que son identifiant — le plafond de lignes borne un écran et le plafond d'octets borne l'argent, et aucun ne borne ce qu'une instruction modifie.

Un jeton est requis partout où elle est définie, et `BREVIS_ENV=local` n'est pas une exception. La bannière de démarrage nomme chaque connexion déverrouillée, la console dessine un bandeau au-dessus de l'éditeur, et `brevis_sql_writable_connection` apparaît dans les métriques. **Laissez-la non définie en production** ; aucun manifeste de ce dépôt ne la définit.

Les jetons peuvent être **nommés** : `BREVIS_SQL_SERVE_TOKEN_CONSOLE=…` déclare un appelant nommé `console`, et chaque ligne d'audit produite par ce porteur contient `"caller":"console"`. L'identité est l'identifiant plutôt qu'un en-tête qu'un appelant renseigne, de sorte que le log ne peut rien dire que le détenteur de ce jeton n'ait pas détenu. Un jeton refusé laisse aussi une ligne, et n'enregistre jamais ce qui a été présenté.

## Un modèle est un fichier qui reste du SQL valide

La configuration est un commentaire de bloc en tête, de sorte que le fichier
s'ouvre dans un éditeur avec coloration syntaxique, s'exécute à la main dans une
console, et que `psql -f` l'exécute.

```sql
/* brevis
materialized: table
tests:
  - not_null: [order_id]
  - unique: [order_id]
  - relationships: {column: customer_id, to: staging.stg_customers, field: customer_id}
*/
select * from staging.stg_orders
```

**Pas de templating.** `models/<schema>/<name>.sql` est `<schema>.<name>`, et les
dépendances viennent de la lecture du SQL plutôt que d'un appel de fonction que
vous devriez écrire. `brevis-sql graph` affiche ce qui a été inféré ; `depends_on:`
dans l'en-tête est le remplacement explicite pour la rare erreur.

## De compile à test

Deux modèles : une vue qui nettoie `raw.orders`, et une table qui en dépend.

`models/staging/stg_orders.sql` :

```sql
/* brevis
materialized: view
*/
select order_id, customer_id, amount, ordered_at
from raw.orders
where amount is not null
```

`models/marts/orders.sql` :

```sql
/* brevis
materialized: table
tests:
  - not_null: [customer_id]
  - unique: [customer_id]
*/
select customer_id, count(*) as orders, sum(amount) as revenue
from staging.stg_orders
group by customer_id
```

`compile` et `graph` ne se connectent à rien :

```bash
docker run --rm -v ./project:/project:ro areteacademy/brevis-sql:0.6.0 \
  compile --project /project
```

```text
2 models, 2 to build, 0 function(s)
  staging.stg_orders                 view  0 test(s)
  marts.orders                       table 2 test(s)
```

```bash
docker run --rm -v ./project:/project:ro areteacademy/brevis-sql:0.6.0 \
  graph --project /project
```

```text
staging.stg_orders
  └─ raw.orders (source)
marts.orders
  ├─ staging.stg_orders
```

`build` et `test`, contre un Postgres :

```bash
docker run --rm -v ./project:/project:ro \
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db areteacademy/brevis-sql:0.6.0 \
  build --project /project --dsn-from BREVIS_SQL_DSN
```

```text
  staging.stg_orders                 view  new   4ms
@brevis:{"type":"landed","target":"postgres://postgres/staging/stg_orders"}
  marts.orders                       table new   4ms
@brevis:{"type":"landed","target":"postgres://postgres/marts/orders","rows":2}
2 models built on postgres
```

```bash
docker run --rm -v ./project:/project:ro \
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db areteacademy/brevis-sql:0.6.0 \
  test --project /project --dsn-from BREVIS_SQL_DSN
```

```text
2 tests passed on postgres
```

La sortie réelle, issue de l'image publiée — compile et graph ne se connectent à rien. Contre un Postgres, build et test se terminent par « 2 models built » et « 2 tests passed ».

### Quand un test échoue

Avec une commande sans client dans `raw.orders`, `build` passe et le test `not_null` non. L'échec nomme le modèle, la colonne, le nombre de lignes et **la requête** — ce que vous collez dans une console et que vous continuez à affiner :

```text
FAIL  marts.orders                   not_null         customer_id          1 row(s)
      SELECT COUNT(*) FROM (SELECT customer_id FROM marts.orders WHERE customer_id IS NULL) AS v
brevis-sql: 1 of 2 tests failed
```

Le processus quitte avec le code 1, et le step du workflow échoue avec lui.

## Les tests sont des SELECT qui renvoient les lignes en infraction

`not_null`, `unique`, `accepted_values`, `relationships`. Zéro ligne est un succès, et il n'y a rien d'autre à interpréter.

## Il dit ce qu'il a écrit

Chaque modèle matérialisé affiche la ligne `@brevis:` que le moteur Brevis lit
déjà, de sorte qu'il apparaît dans `/data` à côté de tout le reste (voir la sortie
de `build` ci-dessus). Une vue ne rapporte pas de nombre de lignes plutôt que zéro :
elle n'en contient pas, et un zéro y est indiscernable d'une table réellement vide.

## Deux entrepôts, une suite

PostgreSQL et BigQuery (`--dialect bigquery`), et les deux exécutent la même suite
de tests — écrite quand il n'y avait qu'une seule implémentation, pour que la
seconde ne puisse pas diverger discrètement.

BigQuery s'authentifie avec les Application Default Credentials : sur une workload
identity, c'est l'identité propre du pod, et `--dsn-from` nomme la variable qui
contient l'id du projet.

## 13 Mo

Distroless, nonroot, sans shell. Le client BigQuery officiel pèse 31 Mo et 526
packages dans un binaire vide ; celui-ci parle donc à BigQuery via son endpoint
REST à la place — environ 200 lignes, avec un test pour chaque manière dont il
pourrait être discrètement faux. Une barrière dans le dépôt refuse ce client par
son nom, pour que la décision survive à une montée de version de dépendance
courante.

## Vérifié face à dbt

jaffle-shop, construit par cette image sur les mêmes données de seed que dbt a
utilisées :

|  | résultat |
|---|---|
| lignes et md5 de chaque ligne, par modèle | **identiques, 13 sur 13** |
| colonnes et types, par modèle | **identiques, 13 sur 13** |
| 13 modèles, premier build | 1,56 s |

## Ce qu'il ne fait pas

**Pas de compatibilité dbt**, et c'est une décision plutôt qu'une lacune. Environ
un tiers des modèles d'un vrai projet dbt n'utilisent rien au-delà de `ref`,
`source` et `config` ; le reste utilise des macros, et les packages sont du
templating par conception. Une demi-compatibilité que personne ne peut prévoir est
pire que rien.

## Où aller ensuite

| si vous voulez | allez à |
|---|---|
| voir où SQL se situe parmi les quatre composants | [Écosystème](/docs/ecosystem/) |
| exécuter brevis-sql comme step planifié | [Core](/docs/core/) |
| faire atterrir les données qu'il modélisera | [SDK Go](/docs/sdk/) ou [Ingestion](/docs/ingestion/) |
