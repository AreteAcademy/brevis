---
title: Sinks d'ingestion
description: Les six destinations, la façon dont chaque base écrit un enregistrement, et pourquoi l'image ne contient que ce que vous utilisez.
group: Ingestion
order: 19
slug: ingestion-sinks
---

Six destinations, et la liste est délibérément explicite — un connecteur
*prévu* et un connecteur qui *tourne* sont deux choses différentes pour quelqu'un
qui choisit cet outil.

| sink | état | ce qu'est une écriture | éprouvé contre |
|---|---|---|---|
| `pubsub` | **tourne** | une publication par lot, attributs et clé d'ordonnancement issus des champs de l'événement lui-même | l'émulateur |
| `postgres` | **tourne** | `COPY FROM STDIN`, ou un `INSERT` en staging dans une transaction | un vrai Postgres |
| `mysql` | **tourne** | `INSERT` multi-lignes dans une transaction, ou `INSERT IGNORE` | un vrai MySQL |
| `files` | **tourne** | des objets NDJSON dans un répertoire, `gs://` ou `s3://` | le système de fichiers, et S3 |
| `bigquery` | **tourne** | lignes chargées, ou mises en staging puis `MERGE` sur `ingestion_id` | **configuration seulement — aucun émulateur n'existe** |
| `redshift` | **tourne** | le lot vers S3, puis `COPY`, puis `MERGE` | **configuration seulement — aucun émulateur n'existe** |

**Deux des six ne sont pas éprouvées de bout en bout**, et le tableau le dit
plutôt que de laisser le mot « tourne » porter une affirmation que personne n'a
vérifiée. BigQuery et Redshift n'ont pas d'émulateur local ; ce qui est testé, ce
sont donc la configuration, les refus et le driver en dessous — que la suite du
SDK couvre déjà. La première utilisation en production de l'un ou l'autre est la
vraie preuve, et elle devrait porter sur un stream à faible enjeu.

Un `type:` inconnu est **refusé au démarrage**, en nommant ce que *ce binaire*
contient. Un gateway qui démarre sur un sink qu'il n'a pas est un gateway qui perd
des événements pour une raison que personne ne voit.

`gs://` et `s3://` ne sont pas des connecteurs séparés : `files` lit les trois
formes de chemin, de sorte que la dead letter est un dossier sur un portable et un
bucket en production sans aucun changement ici.

## Comment chacun écrit

`write` est **requis et jamais défini par défaut** sur les quatre destinations qui
écrivent dans une table, parce que les deux modes sont réellement différents et
que seul le propriétaire de la table sait lequel convient :

| | `append` | `merge` |
|---|---|---|
| **postgres** | `COPY FROM STDIN` | `INSERT … ON CONFLICT (ingestion_id) DO NOTHING` |
| **mysql** | `INSERT` multi-lignes par bloc | `INSERT IGNORE` |
| **bigquery** | lignes chargées (via GCS au-delà de la limite inline) | `MERGE … WHEN NOT MATCHED THEN INSERT` |
| **redshift** | `COPY` depuis S3 | `COPY` vers une table de staging, puis `MERGE` |

`merge` signifie **la même chose dans les quatre** : l'insertion idempotente, et
**la première livraison l'emporte**. Une relivraison est ignorée, pas appliquée —
ce qui est juste pour un événement, qui s'est produit une fois et ne change pas, et
faux pour une ligne portant un état modifiable.

Postgres et MySQL **exigent un index `UNIQUE` sur `ingestion_id`** pour `merge`, et
refusent sans lui plutôt que d'ajouter silencieusement :

```sql
CREATE UNIQUE INDEX CONCURRENTLY ON landing.orders (ingestion_id);
```

Sans l'index, `ON CONFLICT` n'a rien à faire correspondre, et chaque relivraison
dupliquerait dans une table dont le propriétaire a demandé l'inverse. Le refus
n'est pas un lot perdu : il est réessayé, puis enterré dans la dead letter avec la
raison et le `CREATE INDEX` sur l'enregistrement — personne ne perd d'événements
pendant que quelqu'un crée l'index.

**`upsert` est refusé par son nom.** `ON CONFLICT DO UPDATE`, où la *dernière*
livraison l'emporte, est un vrai mode qui **n'est pas implémenté**. Accepter le mot
tout en se comportant comme `merge` est exactement l'échec que ce champ existe pour
empêcher, donc la configuration le dit.

## Chacune avec ce qui n'est vrai que d'elle

```yaml
# Postgres: schema-qualified name, and the table has to exist.
sink: {type: postgres, dsn_from: BREVIS_DSN, table: landing.clicks, write: merge}

# MySQL: no COPY, so throughput is an order below Postgres on the same
# hardware. Flush wider here.
sink: {type: mysql, dsn_from: BREVIS_DSN, table: landing.clicks, write: merge}

# BigQuery: no dsn_from. It authenticates with the pod's own credentials, which
# is what a workload identity is for. The name has no dots — the project and
# the dataset are their own fields.
sink: {type: bigquery, project: acme-prod, dataset: landing, table: clicks, write: merge}

# Redshift: two hops, not one. It is columnar, a row-by-row INSERT pays the cost
# of a block, and the only workable load is COPY from S3. So every batch becomes
# an object in the staging prefix and then a COPY — a stream flushing every
# second writes 86,400 objects a day. Flush much wider here.
sink:
  type: redshift
  dsn_from: BREVIS_RS_DSN
  table: landing.clicks
  staging: s3://acme-staging/gateway/
  iam_role: arn:aws:iam::123456789012:role/redshift-copy
  write: merge
```

`dsn_from` nomme la **variable d'environnement** qui contient la chaîne de
connexion, jamais la chaîne : un DSN contient un mot de passe et ce fichier est
dans git. C'est la même séparation que fait `secrets:` dans un workflow.

`iam_role` est un rôle et le driver **n'accepte pas de clé** : une clé dans l'URL
d'un `COPY` finit dans le log des requêtes du cluster, que bien des gens lisent.

## Aucune déclaration de colonnes

Le gateway n'envoie aucune liste de colonnes. Un pipeline déclare la sienne parce
que ses lignes ont une seule forme qu'il contrôle ; le lot d'un gateway, c'est ce
que *N* clients ont posté dans une fenêtre de flush, et les formes ont le droit de
différer.

Le driver résout la liste à partir de la **table elle-même**, croisée avec ce que
porte le lot, **avant** de toucher au serveur. Un champ que la table n'a pas est
refusé avec le message qui le corrige, au lieu d'échouer en plein `COPY` avec
`column "x" of relation "y" does not exist` ; un champ qu'un événement omet est
écrit en `NULL`.

## La liste des imports est la sélection

Les sinks sont **compilés dedans**, comme les hooks, et le binaire enregistre ce
qu'il a :

```go
func main() {
    sinks := gateway.NewSinks()
    sinks.MustRegister(postgres.Sink, postgres.New)
    sinks.MustRegister(files.Sink, files.New)
    gateway.Main(nil, gateway.WithSinks(sinks))
}
```

Le linker Go élague ce que rien ne référence ; un binaire qui n'importe jamais le
driver BigQuery ne contient donc pas BigQuery — ni Arrow, ni la Storage Write API.
C'est ainsi que `database/sql` a toujours fonctionné.

Cela vaut ce que cela laisse penser :

| build | packages | binaire |
|---|---|---|
| les six sinks, les deux object stores, les deux metastores | environ 900 | 55,0 Mo |
| `postgres` + `files` local | environ 240 | **10,2 Mo** |

Les object stores forment un **second** registre, parce qu'un schéma n'est pas une
destination : `files` est un sink qui écrit dans un répertoire, vers `gs://` et vers
`s3://`, et un build qui n'écrit qu'en local ne devrait pas embarquer le SDK AWS
pour le faire.

### Deux images publiées

| image | contient | pull (amd64) |
|---|---|---|
| `brevissh/brevis-gateway:0.32.0` | six sinks, S3, GCS, Redis, memcached | 19,1 Mo |
| `brevissh/brevis-gateway:0.32.0-slim` | `postgres`, `files` local, `auto_table` | **5,2 Mo** |

Les deux issues d'un même build d'un même arbre, donc les deux tags correspondent
toujours au même commit. Il n'y a pas de `latest-slim` — `latest` est déjà un tag
que personne ne devrait déployer.

**Envie d'une autre paire ?** `cmd/gateway-slim` fait quinze lignes et le bloc
d'imports en est toute la configuration. Copiez-le, changez les imports,
compilez. Quiconque veut un hook compile déjà son propre binaire, donc choisir les
sinks ne lui coûte rien de plus.

### Le refus dit quel build vous tenez

```
sink type "bigquery" is not one this binary carries (it has: files, postgres).
Sinks are compiled in, so this is a build that left it out rather than a
destination that does not exist.
```

Une liste fixe enverrait cette personne chercher une erreur de configuration
qu'elle n'a pas commise. Il en va de même pour un object store : une dead letter
`s3://` dans un binaire sans backend S3 est refusée **au démarrage**, en nommant le
schéma, plutôt qu'au premier lot qu'elle devrait enterrer.

## Une route, N tables, rien de déclaré

```yaml
sink:
  type: auto_table
  shape: columns
  naming: {pattern: '^(app|svc)_[a-z0-9_]{2,40}$', allow: [app_, svc_], max_new_per_hour: 20}
  metastore: {type: redis, addr_from: BREVIS_METASTORE_ADDR, ttl: 60s}
  into: {type: bigquery, project: acme-prod, dataset: landing, write: merge}
```

`auto_table` **route et n'écrit pas** : c'est `into` qui écrit, et il ne porte
aucune `table` — la table vient de chaque événement. Un `into` avec une `table` est
refusé au démarrage, parce qu'une table fixe sous un routeur l'emporterait en
silence et que chaque événement y atterrirait.

### L'enveloppe

Le corps est une **enveloppe** : les champs de contrôle en haut, l'enregistrement
dans `data`.

```json
{
  "table_name": "app_orders",
  "description": "App orders",
  "operation": "INSERT",
  "unique_key": "id",
  "data": {
    "id": "A-1",
    "total": 150,
    "customer": {"id": 7, "uf": "SP"},
    "items": [{"sku": "X", "qty": 2}]
  }
}
```

| champ | | ce que c'est |
|---|---|---|
| `table_name` | **requis** | où cela atterrit |
| `data` | **requis** | l'enregistrement, et lui seul |
| `unique_key` | `id` par défaut | quel champ de `data` identifie l'enregistrement — **requis sous `merge`** |
| `operation` | `INSERT` par défaut | `INSERT`, `UPDATE` ou `DELETE` |
| `description` | facultatif | **accepté et pas encore utilisé** — voir ci-dessous |

`table_name` posé à côté de `total` et de `customer` était un champ du
**transport** qui se faisait passer pour un champ de l'**enregistrement**. Les
séparer est la forme de tous les formats CDC — et c'est ce qui rend possible la
réservation d'un préfixe, car il existe désormais un seul endroit où seul le
producteur écrit.

### La clé relève de `merge`, pas de l'enveloppe

`unique_key` est facultatif, et le fait qu'il soit **requis** dépend du mode
d'écriture — parce que les deux modes veulent des choses différentes :

| | |
|---|---|
| `write: merge` | **requis.** Le mode existe pour garder une ligne par enregistrement, et `brevis_record_key` est ce que veut dire « par enregistrement » : le `qualify` qui résout la version courante partitionne dessus. Une table de merge pleine de lignes ne nommant aucun enregistrement est une table que personne ne peut résoudre. |
| `write: append` | **facultatif.** Une table d'append est un journal, et une entrée de journal n'a pas besoin de concerner *un enregistrement* : une ligne d'audit, un webhook, un échantillon de métrique. Exiger un `id` là obligerait les producteurs à en inventer un, ce qui est pire que `NULL` parce que cela a l'air vrai. |

Sous `append`, la colonne est créée nullable et une ligne sans clé porte `NULL` —
pas `""`, qui est un autre fait. Et `brevis_ingestion_id` existe toujours : sans
clé, **le contenu est la clé**, donc le même document deux fois donne le même id.

**Nommer un champ qui n'est pas dans `data` est une erreur dans les deux modes.**
« Vous ne l'avez pas dit » et « vous avez dit quelque chose qui n'est pas là » sont
des erreurs différentes, et seule la première passe :

```json
{"accepted":0,"rejected":["event 0: \"unique_key\" names \"pedido_id\" as this
 record's identity and \"data\".\"pedido_id\" is missing or empty. Drop
 \"unique_key\" to fall back to \"id\", or send the field"]}
```

**`description` est lu et ne mène aujourd'hui nulle part.** Il est dans le contrat
pour la page d'ingestion de la console, qui n'a pas été écrite. Cela est dit ici
parce qu'un champ accepté en silence est un champ dont quelqu'un croit qu'il est
stocké.

### Huit colonnes fixes, plus ce que porte l'enregistrement

| colonne | | |
|---|---|---|
| `brevis_ingestion_id` | `STRING` | l'identité, et la clé de merge |
| `brevis_record_key` | `STRING` | `data[unique_key]` — de quel **enregistrement** il s'agit ; `NULL` sous `append` |
| `brevis_operation` | `STRING` | `INSERT`, `UPDATE` ou `DELETE` |
| `brevis_received_at` | `TIMESTAMP` | l'arrivée, sur **notre** horloge — la colonne de partition |
| `brevis_loaded_at` | `TIMESTAMP` | l'écriture, horodatée par la **destination** |
| `brevis_stream` | `STRING` | quelle route l'a écrit |
| `brevis_gateway` | `STRING` | quel déploiement |
| `brevis_received_bytes` | `INT64` | la taille de l'événement à son **arrivée**, enveloppe comprise |

Ces huit sont **ceux du SDK**, pas ceux de ce sink : `sdk.LandingSchema` les déclare
et `sdk.LandingID` frappe l'identité, de sorte qu'un pipeline peut faire atterrir la
même table avec les mêmes ids. Une seule définition, parce que deux divergeraient.
Voir [la disposition d'atterrissage du SDK](/docs/sdk/#the-landing-layout).

**`brevis_` est réservé.** Un `data` portant une clé avec ce préfixe est refusé
**par événement** — sinon un producteur forge un champ de contrôle, et un
`brevis_received_at` forgé est pire que rien parce qu'il a l'air vrai.

**`brevis_loaded_at` est un `DEFAULT` de la base**, pas une valeur envoyée par le
gateway. Le gateway connaît l'heure de **dispatch**, la destination connaît l'heure
d'**écriture**, et l'écart entre les deux colonnes est la vraie latence de bout en
bout, par ligne, sans rien instrumenter.

**`brevis_received_at` est la colonne de partition, jamais l'horloge d'un
client** : celle d'un producteur peut être fausse ou absente, et une colonne de
partition que le client contrôle, c'est un client qui peut écrire en 2035. Une
table d'atterrissage non partitionnée est une facture de requêtes qui ne fait que
croître. Le clustering se fait par `brevis_record_key`, parce que lire l'historique
d'un enregistrement est ce que chacun fait avec une table de ce genre.

Une seule déclaration sert toutes les destinations, parce que le générateur de DDL
du SDK traduit chaque type : `STRING` devient `TEXT` sur Postgres, `LONGTEXT` sur
MySQL et `STRING` sur BigQuery ; `JSON` devient `JSONB`, `JSON` et `JSON`.

### Du volume sans infrastructure pour le mesurer

`brevis_received_bytes` est **gratuit** : le gateway compte déjà ces octets au
décodage pour piloter `buffer.flush.size`, et jusqu'ici le nombre mourait là.
Mesurer l'enregistrement seul coûterait un `json.Marshal` par événement — 2,0 µs
contre les 3,1 µs que le parse dépense déjà, **67 % de plus sur le chemin chaud** —
pour affiner un nombre dont le rôle est la tendance et l'attribution.

Il répond à ce que les métriques **ne peuvent structurellement pas** dire. Chaque
série `brevis_gateway_*` est par *stream* et ne porte aucun label `table`,
délibérément : avec `auto_table`, une route devient N tables et ce label est au
choix du producteur. Donc « quelle table grossit, et depuis quand » n'avait de
réponse nulle part.

```sql
select date(brevis_received_at) day,
       count(*) events,
       sum(brevis_received_bytes)/1e9 gb
from app_orders
group by 1 order by 1 desc
```

Sans la colonne, la même question signifie scanner toute la colonne JSON —
`sum(length(to_json_string(data)))` — soit environ **cinquante fois les octets
scannés**, à chaque fois que quelqu'un pose la question. La colonne se rentabilise
dès la première requête.

**C'est de l'entrée, pas du stockage.** La destination garde la ligne typée et
compressée : 400 octets de JSON peuvent en faire 80 sur disque. En faire la somme
donne ce qui est *arrivé*, jamais ce qui est facturé pour le conserver.

Et **le producteur ne peut pas l'écrire**. Le gateway pose la valeur après le hook
et écrase ce que l'enveloppe portait — un client qui enverrait le champ
rapporterait son propre volume, et en faire la somme ne voudrait plus rien dire.

### Les séries de volume sont opt-in

```bash
BREVIS_INGESTION_METRICS=true
```

```
brevis_gateway_ingested_bytes_total{stream,table}
brevis_gateway_ingested_events_total{stream,table}
```

**Désactivées par défaut**, et la valeur par défaut est tout l'intérêt : `table`
est un label que le *producteur* choisit. Tout le reste de ce fichier est étiqueté
par ce qu'un opérateur a écrit dans le YAML, donc le nombre de séries est connu
avant le démarrage du processus. Personne ne devrait découvrir une facture de
métriques parce qu'il a mis à jour.

Une valeur que personne ne sait parser se lit comme **désactivé**, jamais comme un
échec : c'est de l'observabilité, et une faute de frappe ici ne doit pas être la
raison pour laquelle un endpoint d'ingestion ne démarre pas.

La colonne est là dans tous les cas. La métrique achète le *maintenant* ; la
colonne achète le *depuis quand* et le *par qui*.

### Deux formes, un contrat

`shape` décide de ce que l'**enregistrement** apporte à la ligne. Le contrat du
producteur est le même dans les deux cas — l'enveloppe ne change jamais. Ce qui
change, c'est l'aspect de la table, qui est la décision de l'opérateur et non du
producteur.

```yaml
shape: document   # the default
shape: columns
```

Avec **`document`**, `data` va en entier dans une colonne `JSON` :

```
 brevis_ingestion_id | brevis_record_key | … | data
 d7179bfe-…          | A-1               |   | {"id": "A-1", "total": 150, "customer": {…}}
```

Il n'y a jamais de DDL après la création. Un nouveau champ est une nouvelle clé :
pas de quota de changement de schéma, pas de réouverture de write stream, pas de
course entre réplicas — et il est interrogeable le jour de son arrivée. C'est là
que le marché a atterri : Fivetran, Airbyte et Snowpipe font tous atterrir dans une
couche brute qui ne peut pas échouer et la modélisent en aval.

Avec **`columns`**, chaque champ de l'enregistrement obtient sa propre colonne :

```
 id   | total | customer              | items
 A-1  | 150   | {"id": 7, "uf": "SP"} | [{"qty": 2, "sku": "X"}]
```

**Les scalaires deviennent `STRING`, les objets et les tableaux deviennent `JSON`,
et il n'y a d'inférence nulle part.** `150` est arrivé comme un nombre et est
devenu la chaîne `150`. C'est un choix plutôt qu'une limitation : un champ qui
arrive entier aujourd'hui et fractionnaire demain changerait le type d'une colonne
sans que personne écrive quoi que ce soit, et la ligne qui ne rentre plus part dans
la dead letter. Le typer est le chemin de **promotion** — écrit dans le YAML et
relu dans un diff.

**Un `null` ne devient aucune colonne.** Il n'a pas de forme, donc il n'y a rien à
lire : le champ apparaît dans la table à partir du premier événement qui porte une
VALEUR, avec la forme de cette valeur. L'événement qui a envoyé `null` atterrit
quand même en `NULL`, ce qu'il voulait dire — et là où la colonne existe déjà,
parce que vous l'avez déclarée ou qu'un autre événement l'a créée, le `null` y est
écrit au lieu d'être omis.

**Et une colonne `JSON` reçoit une VALEUR JSON, jamais une chaîne de celle-ci.**
Cela ressemble à un détail et c'est la différence entre une table interrogeable et
une table qui a l'air correcte :

```sql
SELECT JSON_TYPE(customer) FROM bronze.orders;     -- object
SELECT JSON_VALUE(customer, '$.uf') ...;           -- SP
```

Si `JSON_TYPE` répond `string` et `JSON_VALUE` répond NULL, l'objet a été encodé
deux fois — la colonne contient le TEXTE de l'objet plutôt que l'objet. Les
versions du gateway jusqu'à **0.15.0** incluse faisaient cela sur BigQuery, dans les
**deux** formes ; voir le [changelog du gateway](https://github.com/AreteAcademy/brevis/blob/master/gateway/CHANGELOG.md)
pour la vérification en une ligne et la réparation. Postgres et MySQL n'ont jamais
été touchés : leur serveur parse le texte.

Un nom de champ doit respecter la règle de BigQuery, la plus étroite des quatre.
Postgres accepterait presque n'importe quoi entre guillemets, et c'est le piège : la
table y est créée et elle casse le jour où quelqu'un pointe un stream vers BigQuery.

Les deux formes ont un équivalent dans le SDK, et c'est le même code plutôt qu'un
code correspondant : `sdk.Landing(table, sdk.LandingKey("id"))` fait atterrir
`document`, et ajouter
[`sdk.LandingColumns()`](/docs/sdk/#two-shapes-the-record-whole-or-one-column-per-field)
fait atterrir `columns`. Mêmes colonnes, mêmes valeurs, même
`brevis_ingestion_id` — donc un stream peut passer d'un gateway à un pipeline sans
migration, et les deux peuvent être lus comme une seule table.

Le pipeline peut faire grandir la même colonne de la même façon : définissez
[`Evolve: sdk.EvolveAdditiveFromPayload`](/docs/sdk/#when-a-field-the-table-does-not-have-shows-up)
sur la destination et un champ que le lot porte devient une colonne, selon cette
règle, par ce code.


**Le préfixe est configurable, et c'est une seule décision pour tout le
processus.** `BREVIS_LANDING_PREFIX=acme` les rend `acme_ingestion_id` et les
autres — pour chaque stream de ce gateway, de sorte que deux sinks ne peuvent pas
être en désaccord sur une table dans laquelle ils pourraient tous deux écrire. La
règle de réservation ci-dessus suit : sous `acme_`, un producteur qui envoie
`acme_region` est refusé et `brevis_region` est un champ ordinaire.

**Choisissez-le avant la création de la première table.** Le changer plus tard
ajoute huit colonnes et en abandonne huit — rien ne supprime de colonne — donc une
table qui porte déjà la disposition sous un préfixe refuse un chargement qui en
déclare un autre. Voir [la page du SDK](/docs/sdk/#the-brevis-prefix-is-yours-to-change)
pour la normalisation et la sortie.

**Les champs d'un objet imbriqué peuvent obtenir leurs propres colonnes.**
`BREVIS_NORMALIZE_DATA=true` sur le processus du gateway aplatit un niveau —
`{"name": {"first": "x"}}` atterrit comme `name_first` — et chaque stream le
partage, de sorte que deux sinks ne peuvent pas être en désaccord sur une table
dans laquelle ils pourraient tous deux écrire. Voir
[la page du SDK](/docs/sdk/#one-column-per-nested-field) pour ce qu'il renomme,
la moitié à lire avant de l'activer.

**Sur BigQuery, `nationalID` et `nationalId` sont une seule colonne.** BigQuery
ignore la casse des colonnes : un flush portant une orthographe dans un `DELETE` et
l'autre dans un `INSERT` crée **une** colonne — la première dans l'ordre trié —
plutôt que de faire échouer le `CREATE`. Un seul événement portant les deux est
refusé, en nommant les deux champs. Postgres et MySQL les gardent comme deux
colonnes, ce qui est correct pour eux. Voir [la page du SDK](/docs/sdk/) pour
savoir quelle orthographe l'emporte et ce qui n'est pas migré.

### `UPDATE` et `DELETE` sont enregistrés, pas appliqués

`brevis_operation` est une colonne. Une table d'atterrissage est un **historique** :
un `UPDATE` appliqué sur place perd la version précédente, et le jour où vous voulez
cette version est le jour où quelque chose a cassé. Résoudre « la version courante
de chaque enregistrement » est le travail du modèle en aval :

```sql
qualify row_number() over (partition by brevis_record_key
                           order by brevis_received_at desc) = 1
   and brevis_operation <> 'DELETE'
```

C'est ce que font Debezium, Fivetran et Airbyte — et cela signifie que le CDC ne
coûte **rien** sur le chemin d'écriture : pas de mode upsert, pas de verrou, pas de
suppression.

### L'identité ne contient aucune horloge

```
brevis_ingestion_id = uuid5(auto_table | table | data[unique_key] | sha256(canonical(data)))
```

Il n'y a pas d'`occurred_at` dans la formule, et c'est délibéré : l'arrivée est
devenue de **notre** responsabilité, et notre horloge est `time.Now()` — différente
à chaque livraison. Une identité qui la contiendrait ferait de chaque relivraison un
nouvel événement, et `merge` cesserait d'absorber quoi que ce soit.

```
same record, same content, twice  →  same id, merge absorbs
same record, content changed      →  a different id, both versions land
```

La réserve est la réserve documentée : deux événements réellement distincts avec un
`data` identique à l'octet près se confondent en un seul. Pour du CDC, c'est
**correct** — deux mises à jour identiques d'un même enregistrement avec les mêmes
valeurs sont le même fait.

L'empreinte porte sur le document sous forme **canonique** — clés triées à chaque
niveau, tableaux laissés dans l'ordre, tout entre guillemets. Go rend l'itération
des maps aléatoire, donc un hash naïf différerait entre deux livraisons du même
événement, ce qui est la seule chose qu'il existe pour empêcher.

### La table gagne une colonne toute seule

Un `data` portant un champ que la table n'a pas fait apparaître la colonne.
**Additif, et seulement additif** : rien n'est supprimé, rien n'est rétréci.

Sur BigQuery, cela n'est devenu vrai qu'en **0.13.0**. Avant, `auto_table`
déclarait `Evolve: additive` sur chaque destination et seuls les drivers Postgres
et MySQL lisaient la déclaration — la forme y était donc figée à la création et
rien ne le disait.

> **En venant de 0.11.0 ou 0.12.0**, les tables créées avant `0.11.0` demandent une
> migration ponctuelle, parce que la huitième colonne fixe est entrée dans la
> déclaration et non dans la table :
>
> ```sql
> ALTER TABLE `<dataset>.<table>` ADD COLUMN IF NOT EXISTS brevis_received_bytes INT64
> ```
>
> Postgres et MySQL n'ont jamais été touchés.

Un lot qui perd la course face à l'`ALTER` échoue, et la nouvelle tentative
ordinaire du tuyau règle le problème — un lot qui attend du DDL et un lot qui
attend un worker sont la même chose, donc il n'y a pas de second buffer. Entre
réplicas, le metastore tient un *debounce* d'une seconde par table et par forme,
pour que dix réplicas qui voient le même nouveau champ ne deviennent pas dix
`ALTER` contre le quota de BigQuery de cinq opérations de métadonnées par table et
par dix secondes.

**Un debounce et non un verrou** : rien n'est libéré et aucun bail n'est renouvelé.
Celui qui perd est *réessayé*, pas bloqué, et un réplica qui meurt en en tenant un
coûte une seconde aux autres. C'est ce que fait le marché — Delta Lake et Iceberg
valident de façon optimiste et réessaient, Kafka Connect obtient la sérialisation de
l'ordonnancement des partitions, Fivetran garde un seul écrivain par table.

### Le nom est la surface d'attaque

`auto_table` transforme une chaîne d'un payload en DDL, donc chaque champ sous
`naming` est un refus :

- **`pattern`** vaut par défaut `^[a-z][a-z0-9_]{2,48}$` — l'intersection de ce que
  Postgres, MySQL, BigQuery et Redshift acceptent sans guillemets. Un nom qui
  passe n'a besoin de guillemets nulle part et ne peut pas porter d'injection.
- **`allow`** restreint davantage, par préfixe.
- **`max_new_per_hour`** (20 par défaut) borne les **créations** sur une heure
  glissante, jamais les écritures : une table qui existe déjà n'est jamais limitée
  en débit. Sous `metastore: memory`, le budget vit dans le processus et quatre
  réplicas en admettent quatre fois autant ; sous `redis` ou `memcached`, ils
  partagent un seul compteur.

Un nom hors des règles est refusé **par événement**, dans la réponse, et tout
événement bien formé de la même requête atterrit quand même :

```json
{"accepted": 0, "rejected": ["event 0: the table name \"orders\" does not match ^(app|svc)_[a-z0-9_]{2,40}$"]}
```

Par événement et non par lot, délibérément. Le refuser au moment de l'écriture ferait
échouer le lot entier — un événement malformé d'un producteur enterrant les
événements de tous les autres producteurs dans la même fenêtre de flush, sans que
l'un d'eux soit prévenu.

**`auto_table` est refusé sur un endpoint sans `listen.auth`**, y compris sous
`BREVIS_ENV=local`. Un producteur qui peut nommer une table peut en créer une, donc
il a besoin d'une authentification même là où un stream ordinaire s'en passerait.
Une NetworkPolicy ne couvre pas cela : elle limite qui atteint le port, pas quel
nom de table ils demandent.

### Le metastore est un cache

Il existe pour qu'une écriture par événement ne soit pas une recherche par
événement, et il contient trois choses : quelles tables existent, le *debounce* de
DDL, et le compteur derrière `max_new_per_hour`.

C'est un cache et **pas** une source de vérité. La destination tranche si une table
existe, et *N* réplicas qui se disputent la création de l'une d'elles est le cas
normal : `AlreadyExists` est un succès, et ceci ne fait que réduire la course. Le
TTL est la durée pendant laquelle il peut être **dans l'erreur**.

| backend | quand |
|---|---|
| `memory` | la valeur par défaut, n'a besoin de rien. Avec **un** réplica, c'est la bonne réponse |
| `redis` | `addr_from` requis. Un `SETNX` est la réclamation, un `INCR` est le compteur |
| `memcached` | `addr_from` requis. Un `Add` est la réclamation, et l'expiration a une granularité à la **seconde** |

**Redis est le plus rapide pour un stream avec une clé de routage**, et la
différence mérite un chiffre plutôt qu'un adjectif. Avec le déclencheur par table,
une fenêtre demande une réclamation par table : Redis répond à 94 d'entre elles en
un aller-retour pipeliné, et tous les autres backends y répondent en parallèle,
borné à 32. Mesuré contre un backend à 25 ms — 94 réclamations :

| backend | une à la fois | tel que livré |
|---|---:|---:|
| `redis` | 2,589 s | **29,8 ms** |
| `memcached` | 2,577 s | **89,4 ms** |

Trois vagues contre un aller-retour. Les deux sont dans les 500 ms accordées à une
réclamation, donc memcached est le choix le plus lent et non un choix refusé.

Avec plusieurs réplicas, `memory` donne un metastore à chacun : le debounce ne
debounce rien et `max_new_per_hour` borne un processus. C'est la seule raison
d'être des deux autres.

**Un metastore en panne ne doit pas entraîner une écriture avec lui.** Un `Get` qui
échoue est un échec de cache, une réclamation qui échoue se comporte comme gagnée,
un compteur qui échoue relâche la limite. Un cache capable d'arrêter l'ingestion est
pire que pas de cache.

**Et « en panne » inclut « lent ».** La réclamation s'exécute pendant que le verrou
du buffer est tenu, donc un backend qui met deux secondes à répondre, c'est deux
secondes pendant lesquelles ce stream n'accepte rien. Chaque appel est borné à
**500 ms** et passe en mode ouvert au-delà. Redis y obéit comme contexte de
l'appelant ; `gomemcache` n'accepte aucun contexte, donc memcached y obéit comme
échéance de socket — la même borne atteinte de deux façons, parce que l'un des deux
clients ne peut pas recevoir l'ordre de s'arrêter.

**`ttl: 0` signifie jamais**, et ne rien dire prend quand même la valeur par défaut
d'une minute — trois états, pas deux.

L'horloge n'est **pas** ce qui récupère d'une entrée erronée. Une écriture que la
destination refuse appelle `invalidate`, qui supprime l'entrée aussitôt et laisse la
nouvelle tentative du tuyau revenir contre un cache froid : une table supprimée à la
main coûte **une tentative échouée**, pas une minute.

L'expiration a un prix. `sinkFor` impute `naming.max_new_per_hour` quand il ne
**connaît** pas une table — un échec de cache impute donc une table qui existe
depuis des semaines, et un pod qui redémarre une minute après le dernier événement
d'une table ne tire rien du backend partagé qu'il paie. Avec des tables de longue
durée, `ttl: 0` est la réponse.

> Sur `0.12.0` et `0.13.1`, cette écriture **ne se parse pas** — YAML lit un zéro
> nu comme un entier, et `0.13.2` est la première qui accepte les deux. Sur ces
> versions, écrivez `ttl: 0s`.

`addr_from` nomme la **variable d'environnement** qui contient l'adresse, jamais
l'adresse : elle contient assez souvent un mot de passe.

### BigQuery a un plancher de flush

**BigQuery autorise 1 500 load jobs par table et par jour.** La fenêtre par défaut
d'une seconde en fait 86 400 — 57 fois le quota, épuisé en environ vingt-cinq
minutes. Ce n'est pas un problème pour Pub/Sub, et le nombre arriverait ici *depuis*
une configuration Pub/Sub.

Un stream qui écrit vers BigQuery, directement ou via un routeur, est donc **refusé
au chargement** avec une fenêtre inférieure à 60s. La vraie réponse est la Storage
Write API, qui n'a pas été écrite ; d'ici là, la configuration refuse une fenêtre
qu'elle ne peut pas honorer plutôt que de laisser le premier déploiement le
découvrir.

`auto_table` ne peut pas router vers **Redshift** : ce driver ne crée aucune table,
donc chaque nouveau nom échouerait au chargement. Refusé par son nom, au démarrage.

## Ce qui n'existe pas encore

`sqlite`, `dynamodb`, `kinesis`, `sqs`, `sns`, `kafka` et `rabbitmq` sont prévus et
rien n'a été écrit. (`redis` et `memcached` existent comme **metastore**, ce qui est
autre chose : un cache de ce qui est connu des tables, jamais une destination.) Le
tableau en haut de cette page dit l'état de chacun, et il est mis à jour quand
l'état change — pas avant.
