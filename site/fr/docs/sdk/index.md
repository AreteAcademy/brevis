# SDK Go

> Écrire un fetcher — extraire depuis une source, transformer et charger vers une destination.

*https://brevis.sh/fr/docs/sdk/ · brevis.sh docs (fr)*

---

Quand le travail d'un step consiste à **récupérer des données et à les charger
quelque part**, le SDK Go vous donne le fetcher complet en quelques lignes. C'est
un module séparé, versionné indépendamment du moteur.

```bash
go get github.com/AreteAcademy/brevis/sdk@latest
```

Nécessite Go 1.23 ou plus récent — le SDK diffuse les lignes sous forme
d'`iter.Seq2`.

:::warning N'utilisez pas `v0.1.0`
Elle a été publiée avec un `go.mod` qui épingle une révision inexistante, et le
proxy de modules Go est immuable. Commencez à `v0.1.1`.
:::

## Trois étapes

```go
import (
	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to/bigquery"
)

rows, err := sdk.Extract(ctx, sdk.Source{
	From: from.HTTP{URL: "https://api.example.com/v1/events"},
})

rows = sdk.Transform(rows, sdk.Accept("id", "created_at", "amount"))

res, err := sdk.Load(ctx, rows, sdk.Target{
	To:      bigquery.Table{Dataset: "bronze", Name: "events"},
	Columns: []string{"id", "created_at", "amount"},
})
```

`Extract` lit, `Transform` remodèle, `Load` écrit. Chacune prend et renvoie une
séquence — rien n'est matérialisé en mémoire d'un seul coup.

![Le flux du SDK : sdk.Extract lit depuis une API HTTP avec from.HTTP, sdk.Transform conserve les champs acceptés et sdk.Load écrit les lignes dans une table BigQuery. Les trois appels s'exécutent à l'intérieur de votre propre step.](/assets/flow-sdk.svg)

## Le driver est une valeur, pas un réglage

`from.HTTP` porte tout ce dont une source HTTP a besoin : URL, en-têtes, nouvelle
tentative, pagination, et ce que signifie une réponse. `from.Files` porte un
chemin et un format. Aucun n'a à faire de la place aux champs de l'autre : il n'y
a donc pas de struct de source qui accumule quarante options dont chaque driver
en lit six.

Cela décide aussi de **ce que vous compilez**. Go élague les dépendances par
package importé, jamais par champ utilisé :

| ce que vous importez | packages | AWS | Google |
|---|---|---|---|
| `sdk` | 190 | non | non |
| `sdk` + `from` | 194 | non | non |
| `sdk` + `from` + `to` (fichiers) | 195 | non | non |
| `sdk` + `to/bigquery` | 456 | non | **oui** |
| `sdk` + `from` + `store/s3` | 265 | **oui** | non |

Un pipeline de fichiers complet — lecture et écriture — coûte 195 packages et
aucun SDK cloud. **Un driver adossé à un SDK de fournisseur vit dans son propre
package**, ce qui explique que BigQuery soit `to/bigquery` et que les stockages
d'objets soient `store/s3` et `store/gcs`.

## Sources et destinations

| source | package |
|---|---|
| HTTP | `from.HTTP` |
| fichiers (local, S3, GCS) | `from.Files` |
| Postgres | `from/postgres` |
| MySQL | `from/mysql` |
| plusieurs sources | `from.Many` |

| destination | package |
|---|---|
| fichiers | `to.Files` |
| BigQuery | `to/bigquery` |
| Postgres | `to/postgres` |
| MySQL | `to/mysql` |
| Redshift | `to/redshift` |
| Pub/Sub | `to/pubsub` |

Le schéma du chemin décide du backend, et le `Store` est **passé en paramètre**
plutôt que choisi à l'intérieur du driver :

```go
from.Files{Path: "s3://bucket/day=1/*.ndjson", Store: s3.New(client)}
to.Files{Path: "gs://bucket/landing/", Store: gcs.New(client)}
```

C'est ce qui évite à un programme de fichiers locaux de compiler la moindre ligne
de code AWS ou Google.

### Publier vers un topic

`to/pubsub` est la seule destination qui n'est pas une table, et elle se comporte
différemment d'une façon qu'il vaut mieux connaître avant de l'utiliser : **elle
n'ajoute rien à ce qu'elle envoie.**

```go
Target: sdk.Target{
    To: pubsub.Topic{
        Project: "acme-prod",
        Name:    "orders",

        // Nil is the default, and it means NO attributes at all.
        Attributes: func(e sdk.Envelope) map[string]string {
            row, ok := e.Payload.(map[string]any)
            if !ok {
                return nil
            }
            return map[string]string{"orderId": fmt.Sprint(row["order_id"])}
        },
    },
},
```

Un abonné reçoit votre payload, octet pour octet, plus exactement les attributs
que vous avez nommés. Rien d'autre — pas d'`ingestion_id`, pas de `provider`,
aucune enveloppe d'aucune sorte.

C'est une règle, pas du minimalisme. Une table est créée par le pipeline qui
l'écrit, donc le SDK peut décider de ses colonnes. **Un topic non** : il existe
avant votre pipeline, ses abonnés ont été écrits en premier et leurs filtres
aussi. Un attribut ajouté de son propre chef reviendrait à Brevis modifiant le
contrat de quelqu'un d'autre, et l'abonné le découvrirait à trois heures du
matin.

Trois choses en découlent :

- **Il ne crée jamais de topic.** Un pipeline qui peut en créer un peut créer le
  mauvais, et contrairement à une table mal orthographiée, personne ne s'en
  aperçoit — les messages vont quelque part et l'abonné qui aurait dû les
  recevoir reste silencieux.
- **`Schema`, `Dedup` et `PartitionBy` sont refusés**, en nommant l'option. Ils
  existent pour les tables : un `Schema` est la façon dont une destination en
  crée une, la déduplication a besoin d'une ligne à remplacer, une partition est
  une notion de table. Ce qui s'en rapproche le plus ici est `OrderingKey`, qui
  est autre chose — elle regroupe des messages qui doivent *arriver* dans l'ordre.
- **Un échec est partiel.** La publication n'est pas transactionnelle : 48 000
  messages qui échouent à 31 000 en ont *livré* 31 000. Le résultat rapporte ce
  qui est réellement parti, et l'erreur dit qu'une relance est une relivraison —
  l'abonné doit être idempotent, et Pub/Sub est de toute façon at-least-once.

Exécutable, contre l'émulateur :
[`examples/13-pubsub`](https://github.com/AreteAcademy/brevis/tree/master/examples/13-pubsub).

## Un fetcher complet

`sdk.Run` prend en charge les options, `-dry-run`, la journalisation, les
nouvelles tentatives, la pagination, la provenance, la création de table et le
code de sortie. Ce qui reste dans le fichier, c'est uniquement ce qui est propre à
cette source :

```go
package main

import (
	"time"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
)

func main() {
	sdk.Run(sdk.Pipeline{
		Source: sdk.Source{
			From: from.HTTP{
				URL:     "https://api.example.com/v1/events",
				Timeout: 15 * time.Second,
			},
			Guard:  sdk.RejectIf("error"),
			Expand: sdk.ArrayAt("results"),
		},
		Target: sdk.Target{
			Provider: "example",
			Entity:   "events",
			Key:      sdk.Key("id"),
			When:     sdk.Field("created_at"),
		},
	})
}
```

```bash
go run ./fetcher -dry-run   # extracts, counts rows and errors, writes nothing
go run ./fetcher
```

## Le contexte du run

Quand le fetcher s'exécute **comme step d'un workflow**, le moteur injecte dans
l'environnement ce qu'il ne pourrait pas savoir autrement : s'il s'agit du
premier run, avec quels paramètres il a été dispatché, de quel run il s'agit. Le
SDK le lit dans `Pipeline.Run` :

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	if p.Run.Params["load_full"] == "true" {
		p.Source.From = from.HTTP{URL: base + "?full=1"}
	}
	return nil
},
```

Exécuté à la main, `Run` revient à zéro — le lire est facultatif, et l'ignorer ne
coûte rien.

Sans historique, la réponse à « est-ce le premier run ? » est toujours **non** :
créer une table sans certitude est pire que de ne pas la créer.

### L'horloge

`p.Run.Auto` est ce que le moteur a calculé à propos de ce run — les
[paramètres automatiques](/fr/docs/parameters/#auto-params). `Auto.Now()` est
l'horloge à lire à la place de `time.Now()` : sur un run planifié, c'est le slot,
donc elle ne bouge pas quand le run est en retard et ne bouge pas quand le run
est relancé trois heures plus tard.

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	start, end, ok := p.Run.Auto.Window()
	if !ok { // no schedule: fall back to a fixed window
		start, end = p.Run.Auto.Now().Add(-24*time.Hour), p.Run.Auto.Now()
	}
	p.Source.From = from.HTTP{URL: base +
		"?from=" + start.Format(time.RFC3339) +
		"&to=" + end.Format(time.RFC3339)}
	return nil
},
```

Exécuté à la main, `Auto.Now()` *est* l'horloge murale et `Window()` renvoie
`false`, donc le développement local n'a besoin d'aucun cas particulier.

## À ne pas confondre avec les bibliothèques clientes

Ce SDK est de la mécanique ETL en Go : drivers, pagination, provenance, création
de table. Les [bibliothèques clientes](/fr/docs/libraries/index.md) sont autre chose — des
clients légers qui fournissent à un step le contexte et l'horloge du run, en
Python aujourd'hui et en Node.js et Rust plus tard. Un step qui utilise pandas ou
dbt veut les secondes, pas celui-ci.

## Des paramètres qui construisent le pipeline

`p.Run.Params` est lisible à l'intérieur du pipeline, ce qui est trop tard pour
une valeur qui décide de ce qu'**est** le pipeline — une source par état, une
table par tenant. `Pipeline.Flags` est parsé dans `Run`, donc c'est trop tard
pour la même raison.

```go
func main() {
	sdk.Run(pipeline(sdk.ParamList("ufs")))
}
```

`sdk.Param` et `sdk.ParamList` lisent d'abord l'environnement du moteur et
ensuite `-param name=value` :

```bash
./fetch-stations -param ufs=SP,RJ
```

L'environnement l'emporte — sous le moteur, c'est lui la valeur, et une option
laissée dans un manifeste ne doit pas écraser ce que l'opérateur a saisi. Sans
moteur, l'option est tout ce qu'il y a, ce qui vaut mieux que d'écrire
`BREVIS_RUN_PARAMS` en JSON à chaque exécution.

## La disposition d'atterrissage

Un pipeline peut faire atterrir **la même table qu'un gateway** — les mêmes
colonnes et le même `brevis_ingestion_id` — de sorte que choisir entre les deux
est une décision de déploiement et non de schéma. Une équipe peut lire les deux
ensemble, et passer de l'un à l'autre sans migration.

```go
const table = "landing.orders"

sdk.Run(sdk.Pipeline{
    Source:    sdk.Source{From: from.HTTP(/* ... */)},
    Transform: []sdk.Transformer{sdk.Landing(table, sdk.LandingKey("id"))},
    Target: sdk.Target{
        To:     postgres.Table{DSN: dsn, Name: table, CreateTable: true},
        Schema: sdk.LandingSchema(sdk.LandingOptions{}),
    },
})
```

`sdk.LandingSchema` compte neuf colonnes : les huit que la disposition possède et
celle que le producteur possède. `sdk.Landing` les remplit et place
l'enregistrement, en entier, dans `data`. Ajoutez vos propres colonnes au schéma
si vous le souhaitez, ou prenez `sdk.LandingControlColumns` et donnez aux champs
de l'enregistrement des colonnes à eux.

### Deux formes : l'enregistrement entier, ou une colonne par champ

Ce qui précède est la forme **document** — l'enregistrement va dans `data` en
JSON, et la table est la même quel que soit le contenu de l'enregistrement.
Ajoutez `sdk.LandingColumns()` et chaque champ de l'enregistrement obtient sa
propre colonne :

```go
sdk.Landing(table, sdk.LandingKey("source_key"), sdk.LandingColumns())
```

La table est alors `sdk.LandingControlColumns` plus vos colonnes — **pas**
`sdk.LandingSchema`, qui porte `data` :

```go
Target{
    To: postgres.Table{DSN: dsn, Name: table, CreateTable: true},
    Schema: append(sdk.LandingControlColumns(sdk.LandingOptions{Keyed: true}),
        sdk.Column{Name: "source_key", Type: sdk.TypeString},
        sdk.Column{Name: "series", Type: sdk.TypeString},
        sdk.Column{Name: "data", Type: sdk.TypeString},
        sdk.Column{Name: "valor", Type: sdk.TypeString},
    ),
}
```

**Vous déclarez ces colonnes.** Le SDK n'infère jamais de schéma, et cette
option ne change pas cela : elle décide de la façon dont la *ligne* est
construite, jamais de ce qu'est la table. Un gateway infère parce que son
producteur est un inconnu qui poste ce qu'il a ; ici, l'auteur du pipeline connaît
la forme et l'écrit, dans un diff que quelqu'un relit.

C'est la même forme qu'un gateway fait atterrir avec
[`shape: columns`](/fr/docs/ingestion-sinks/#two-shapes-one-contract), par le même
code — mêmes colonnes, mêmes valeurs, même `brevis_ingestion_id`.

**Un nom de champ doit pouvoir être un nom de colonne** : une lettre ou un
tiret bas, puis des lettres, des chiffres et des tirets bas. C'est la règle de
BigQuery, la plus étroite des quatre destinations, donc un nom qui passe ici
fonctionne partout. Un enregistrement portant `my-field` est refusé par son nom,
avant que quoi que ce soit soit écrit. La forme document n'a pas cette règle — un
tiret est une clé JSON parfaitement valide.

**Il n'aplatit pas, par défaut.** Un objet imbriqué devient UNE colonne `JSON`
sous la clé qui le contenait : `customer` contient `{"id": 7, "uf": "SP"}`, pas
`customer_id` et `customer_uf`. Un tableau est aussi `JSON`. Un enregistrement qui
veut une ligne *par élément de tableau* veut `sdk.ArrayAt` dans le `Expand` de la
source — il s'exécute avant `Landing`, et c'est une autre opération avec un autre
nom. [`BREVIS_NORMALIZE_DATA`](#one-column-per-nested-field) change la première
moitié de cela pour un déploiement entier ; un tableau reste `JSON` dans les deux
cas :

```go
Source: sdk.Source{From: from.HTTP(/* ... */), Expand: sdk.ArrayAt("results")},
```

**`null` reste `NULL`**, jamais `""`. Un champ envoyé à null et un champ envoyé
vide sont des faits différents, et une colonne ne peut pas les distinguer ensuite.

**`data` devient à vous.** Dans cette forme, la disposition n'a pas de colonne de
ce nom, donc un producteur avec un champ appelé `data` — ce qu'envoie justement la
série Bacen — obtient une colonne à lui avec sa propre valeur dedans. Sous
`document`, la sienne est une clé à l'intérieur du JSON de la disposition.

### Le préfixe `brevis_` est à vous de le changer

```
BREVIS_LANDING_PREFIX=acme
```

et les huit colonnes de la disposition deviennent `acme_ingestion_id`,
`acme_record_key` et les autres. Non défini, elles restent ce qu'elles ont
toujours été.

**Choisissez-le à la création de la table, car le changer plus tard n'est pas un
renommage.** Rien dans ce SDK ne supprime jamais une colonne : un stream qui
change de préfixe n'en renomme donc pas huit — il en ajoute huit et en abandonne
huit. Toute ligne ensuite a des NULL dans l'ancien jeu, et

```sql
qualify row_number() over (partition by brevis_record_key
                           order by brevis_received_at desc) = 1
```

partitionne sur une colonne que plus rien n'écrit. Les lignes arrivent, le
tableau de bord s'aplatit, et rien ne journalise quoi que ce soit.

Ainsi, une table qui porte déjà la disposition sous un préfixe **refuse** un
chargement qui en déclare un autre, avant l'extraction, en nommant les deux.
Pointez-le vers une nouvelle table, ou renommez d'abord vous-même les huit
colonnes.

**Ce que vous saisissez est normalisé**, pour qu'il n'y ait qu'une orthographe à
rater au lieu de quatre :

| vous définissez | vous obtenez |
|---|---|
| rien, ou vide | `brevis_` |
| `_NAME` | `name_` |
| `_NAME_` | `name_` |
| `NAME` | `name_` |

On retire `_` aux deux extrémités, on met en minuscules, on en ajoute exactement
un. Deux valeurs sont refusées plutôt que devinées : des tirets bas et rien
d'autre, car cela laisserait `ingestion_id` — exactement la colonne qu'écrit
`sdk.IngestionID()` pour tout pipeline ordinaire — et tout ce qui ne peut pas
commencer un nom de colonne. **Un processus avec une valeur inutilisable ne
démarre pas**, ce qui est la seule issue honnête : l'alternative, ce sont des
tables nommées d'après le préfixe que vous remplaciez.

**C'est par PROCESSUS, et c'est le but plutôt qu'une limitation.** Tous les
streams d'un même gateway le partagent, donc deux sinks ne peuvent pas être en
désaccord — pas plus que le schéma qui déclare une table et le transformer qui la
remplit.

**La règle des noms réservés suit.** Sous `acme_`, un producteur qui envoie
`acme_region` est refusé, car quelqu'un qui peut forger une colonne de contrôle en
forge une qui a l'air vraie. Et `brevis_region` devient un champ ordinaire.

`data` ne porte jamais le préfixe. C'est la colonne du producteur, pas celle de la
disposition.

### Une colonne par champ imbriqué

```
BREVIS_NORMALIZE_DATA=true
```

et les champs d'un objet imbriqué obtiennent des colonnes à eux, à la manière de
`json_normalize` de pandas — sur un niveau :

```python
data = [
    {"id": 1, "name": {"first": "Coleen", "last": "Volk"}},
    {"name": {"given": "Mark", "family": "Regner"}},
    {"id": 2, "name": "Faye Raker"},
]
```

```
id  name_first  name_last  name_given  name_family  name
```

**Choisissez-le avant la première table, car il n'AJOUTE pas seulement des
colonnes.** Il les renomme, et rien dans ce SDK n'en supprime jamais — l'ancien
nom reste donc avec les lignes qu'il contient déjà tandis que tout ce qui suit
atterrit dans le nouveau, et la requête que quelqu'un a écrite contre lui continue
de tourner et cesse de voir les nouvelles lignes. Deux renommages ont lieu :

- `userName` devient `username`, qu'il y ait ou non quelque chose d'imbriqué
- `name`, qui contenait des objets, est remplacé par `name_first` et `name_last`

Une table qui porte déjà l'une ou l'autre forme **refuse** un chargement avec
cette option activée, avant l'extraction, en nommant les deux colonnes. Pointez-le
vers une nouvelle table, ou renommez d'abord vous-même les colonnes.

**`name` peut apparaître à la fois aplati et entier**, et l'exemple ci-dessus le
montre : le troisième enregistrement porte `name` comme chaîne, donc la table a
`name_first`, `name_last` ET `name`. Un champ qui est un objet dans un
enregistrement et un scalaire dans un autre produit les deux.

**Un niveau, et les tableaux sont des valeurs.** `{"a": {"b": {"c": 1}}}` donne
`a_b` contenant `{"c": 1}` — une colonne `JSON`, pas `a_b_c`. `{"a": [1, 2]}` donne
`a`, aussi `JSON`. Un enregistrement qui veut une ligne par élément de tableau
veut toujours `sdk.ArrayAt`.

**Joint et mis en minuscules, pas découpé sur le camelCase.** `userName` devient
`username` et non `user_name`, car l'autre lecture n'a pas de réponse convenue
pour les acronymes — `HTTPStatus`, `userID` — et chaque bibliothèque en diffère.

**Une collision est refusée, jamais résolue.** `{"name_first": 1, "name":
{"first": 2}}` ferait une colonne à partir de deux champs, et l'itération des maps
de Go est aléatoire : laisser l'un gagner donnerait au même lot une table
différente d'une exécution à l'autre. Le refus nomme les deux.

**Le préfixe réservé est vérifié sur la colonne, pas sur le champ.** Un producteur
qui envoie `{"brevis": {"stream": "x"}}` créerait sinon `brevis_stream` par une
porte que la vérification du champ ne surveille pas. Il est refusé, en nommant
`brevis.stream` pour qu'il puisse retrouver le champ.

**C'est par PROCESSUS**, comme le préfixe, et pour la même raison : tous les
streams d'un même gateway le partagent, donc deux sinks ne peuvent pas être en
désaccord sur une table dans laquelle ils pourraient tous deux écrire. Il ne
s'applique qu'à la forme `columns` — sous `document`, l'enregistrement va en
entier dans une colonne `JSON` et il n'y a rien dans quoi aplatir.

### Du texte qui est déjà du JSON

Une colonne déclarée `sdk.TypeJSON` reçoit une **valeur** JSON — un objet, un
tableau, un nombre. Sur BigQuery, la ligne est encodée en entier, donc une
`string` Go devient un littéral de chaîne JSON et la colonne finit par contenir le
TEXTE de votre objet plutôt que l'objet :

```sql
SELECT JSON_TYPE(payload) ...;            -- "string", not "object"
SELECT JSON_VALUE(payload, '$.a') ...;    -- NULL
```

La colonne est du bon type et les données qu'elle contient sont inaccessibles, ce
qui est le pire des trois cas parce que rien n'échoue. **La destination BigQuery
le refuse** depuis `v0.74.0`, en nommant la colonne.

Passez l'objet lui-même quand vous en avez un. Quand ce que vous avez est déjà du
texte JSON — une colonne lue dans une autre base, un corps de réponse que vous ne
voulez pas re-parser — dites-le :

```go
row["payload"] = sdk.JSONText(body)
```

Le `json.RawMessage` d'`encoding/json` fonctionne aussi, pour la même raison :
les deux écrivent le JSON qu'ils contiennent au lieu d'une chaîne entre
guillemets de celui-ci.

**Postgres et MySQL ne sont pas concernés et ne refusent pas.** Leur serveur
parse une chaîne dans une colonne JSON, et l'a toujours fait.

### Quand un champ que la table n'a pas apparaît

Un producteur ajoute un champ et le chargement s'arrête, en le nommant : la ligne
porte une colonne que rien n'a déclarée, et l'écrire vers une destination qui ne
l'a jamais mentionnée la perdrait en silence. Ce refus est juste par défaut — mais
sur une table d'atterrissage, c'est le seul changement qui ne peut pas casser un
lecteur, et la destination peut le faire :

```go
postgres.Table{DSN: dsn, Name: table, Evolve: sdk.EvolveAdditiveFromPayload}
```

Le lot **complète** alors votre déclaration : un champ qu'il porte et que la
déclaration n'a pas devient une colonne, `STRING` sauf si le champ est un objet ou
un tableau, auquel cas `JSON`. C'est la même règle et le même code avec lesquels
un gateway fait atterrir `shape: columns`, donc les deux produisent la même table.

Deux autres valeurs, et la différence entre elles est l'origine de la colonne :

| `Evolve` | ajoute |
|---|---|
| `sdk.EvolveNone` (défaut) | rien. Toute différence est refusée, en nommant les deux côtés |
| `sdk.EvolveAdditive` | une colonne que votre **déclaration** a et que la table n'a pas |
| `sdk.EvolveAdditiveFromPayload` | cela, plus une colonne que le **lot** porte |

**Uniquement `ADD COLUMN`.** Une colonne qui disparaît de la source cesse d'être
écrite et reste dans la table ; rien ne se renomme, rien ne se supprime, et aucun
type n'est jamais rétréci. Ce n'est pas une lacune — une table d'atterrissage est
un historique, et la seule modification qui ne peut pas casser quelqu'un qui la lit
est une nouvelle colonne nullable.

**Il complète une déclaration ; il n'en remplace pas une.** Sans rien de déclaré,
il est refusé avant l'extraction, en nommant la table : créer une table entière à
partir d'un payload est une autre décision, et le SDK ne la prend pas. Sur la
disposition d'atterrissage, le tort serait concret — `brevis_received_at` sortirait
en texte au lieu d'un timestamp, et `brevis_loaded_at` ne voyage jamais dans la
ligne, de sorte que la mesure de latence de bout en bout manquerait simplement.

**Le SDK n'infère toujours pas.** Le type vient de la FORME du champ, jamais de sa
valeur : `21129` et `8.89` déclarent tous deux `STRING`, donc le jour où la série
publie un entier, rien ne change. Un type lu sur le premier lot est l'échec que
cette règle existe pour empêcher.

**Et un `null` ne décide de rien — mais il reste dans la ligne.** Ce sont deux
questions différentes, et leur donner une seule réponse a coûté la v0.77.0.

Un null n'a pas de forme, donc le **schéma** n'a rien à lire : un champ qui arrive
`null` ne déclare aucune colonne, et la colonne apparaît à partir du premier
enregistrement qui porte une VALEUR, avec la forme de cette valeur. Dans l'autre
sens, un null deviendrait `STRING` faute de réponse, et le premier tableau après
lui ferait refuser tout le lot par la destination — sans migration de `STRING` vers
`JSON` pour le défaire.

La **ligne** porte ce que le producteur a envoyé, null compris. Il le faut : une
colonne que vous avez DÉCLARÉE dans `Target.Schema` et qui est arrivée vide doit
être dans la ligne, sinon la vérification qui compare la déclaration à celle-ci lit
une chaîne qui a cessé de la produire — et refuse le chargement. C'est ce que la
v0.77.0 a fait, et ce que la 0.78.0 a défait.

La destination règle le reste : un champ null dans TOUS les enregistrements du
lot, sans colonne dans la table, n'est écrit nulle part et ne refuse rien. Un null
et un champ absent atterrissent en `NULL` identique, donc rien n'est perdu.

**Un champ qui change de forme est refusé, pas absorbé.** Si `valor` est arrivé
comme scalaire et a fait une colonne texte, un objet arrivant plus tard est un
changement de nature et le lot s'arrête — avec le commentaire de la colonne dans
le message, pour que vous voyiez que personne ne l'a déclarée. Renommez la
colonne, ou migrez-la vous-même.

**Sur BigQuery, deux orthographes d'un même nom sont une seule colonne.**
BigQuery ignore la casse des colonnes : un lot portant `nationalID` dans un
enregistrement et `nationalId` dans un autre déclare **une** colonne, pas deux, et
l'orthographe retenue est la première dans l'ordre trié — `nationalID`, parce que
`D` se trie avant `d`. Pas la première arrivée : le même lot rejoué ne doit pas
créer une colonne à l'orthographe différente. Une déclaration qui dit `createdat`
face à une table dont la colonne est `createdAt` est aussi la même colonne, donc
rien n'est ajouté et rien n'est refusé.

**Un enregistrement portant les deux orthographes est refusé, en nommant les
deux.** La destination n'a qu'une colonne et l'une des deux valeurs l'emporterait
silencieusement, donc le chargement s'arrête : renommez l'une d'elles dans
`Transform`, ou faites correspondre les deux à un seul champ. Deux
*enregistrements* portant chacun une orthographe sont acceptables — c'est du trafic
CDC ordinaire, et c'est ce que cette règle existe pour faire atterrir.

**Rien n'est migré.** Une table qui a déjà deux colonnes ne différant que par la
casse garde les deux ; aucune colonne n'est renommée et aucune n'est supprimée.

**Postgres et MySQL ne replient pas la casse.** Un identifiant entre guillemets y
est distinct, donc deux orthographes font deux colonnes — à dessein, et le même
fetcher pointé vers BigQuery les fera atterrir dans une seule.

**Chaque colonne créée par un lot le dit, dans la table.** Un commentaire sur
Postgres et MySQL, une description de champ sur BigQuery, portant la date :

```
brevis: added from a batch on 2026-09-29; the type is the landing rule
(scalar text, object and array json), not a decision
```

Six mois plus tard, c'est la seule chose qui reste pour répondre à « quand cette
colonne est-elle apparue, et qui l'a décidé ». Une ligne de log aura
alors disparu avec la rotation des logs.

**Ce n'est pas un mode.** `Target.Schema` reste vous qui déclarez ce qu'est la
table — cela vous remet une disposition qui existe déjà au lieu de vous la faire
taper. Rien dans le SDK ne se comporte différemment parce que vous l'avez
utilisée, et un pipeline qui déclare son propre schéma n'est pas affecté.

**Écrivez le nom de la table une seule fois.** Il va à `Landing`, parce que c'est
le deuxième emplacement de l'id, et à la destination, parce que c'est là que vont
les lignes. **Rien ne vérifie que les deux concordent** : le `Writer` connaît sa
propre table mais ne l'expose que par `Describe`, qui est le nom pour les logs et
les erreurs, et un id construit dessus lierait chaque id déjà écrit à une chaîne
de log. Si vous les désynchronisez, les lignes atterrissent correctement avec des
ids frappés pour une table dans laquelle personne n'a écrit. Elles auront l'air
correctes. Elles ne correspondront pas à celles du gateway.

**Trois colonnes sont `NULL`, et c'est la réponse honnête.** `brevis_stream` et
`brevis_gateway` nomment des choses qu'un pipeline n'a pas —
`brevis_gateway IS NULL` est la façon de distinguer la ligne d'un pipeline de
celle d'un gateway. `brevis_received_bytes` est `NULL` aussi : celle du gateway
compte ce qui est arrivé sur le fil, enveloppe comprise, et le JSON propre de
l'enregistrement fait environ 30 % de moins. Une colonne à deux sens rendrait
fausse toute somme portant sur des lignes des deux chemins, d'une erreur égale à
la part venant de l'un ou de l'autre. `length(data)` répond exactement pour la version du
pipeline.

**Fusionner exige `DedupKey`, et sans lui il n'y a aucune fusion.** Chaque driver
fait la correspondance sur `ingestion_id` sauf indication contraire, et la colonne
d'identité de cette disposition est `brevis_ingestion_id` :

```go
Target{
    To:       postgres.Table{DSN: dsn, Name: table, CreateTable: true},
    Schema:   sdk.LandingSchema(sdk.LandingOptions{UniqueID: true, Keyed: true}),
    Dedup:    sdk.DedupMerge,
    DedupKey: sdk.LandingColumnID,
}
```

`UniqueID` est ce qui pose la contrainte UNIQUE sur l'id, et Postgres comme MySQL
refusent de fusionner sans elle — `ON CONFLICT` n'a rien à faire correspondre. Un
`DedupKey` que votre `Schema` ne déclare pas est refusé avant que quoi que ce soit
s'exécute, car une fusion sur une colonne que la table n'a pas ne correspond à
rien, et une fusion qui ne correspond à rien ressemble exactement à une fusion qui
correspond à tout ce qu'elle devrait.

**Par destination, et aujourd'hui MySQL ne peut pas.** BigQuery n'a pas de
contraintes d'unicité et refuse carrément la déclaration. Le `string` de MySQL est
`LONGTEXT`, et MySQL n'indexe pas un `LONGTEXT` sans longueur — une table
d'atterrissage avec fusion ne peut donc pas y être *créée* du tout :

```
Error 1170 (42000): BLOB/TEXT column 'brevis_ingestion_id' used in key
specification without a key length
```

Postgres fonctionne, parce que son `string` est `TEXT`. Sur MySQL, atterrissez avec
`DedupNone` et résolvez la version courante en aval, ou créez vous-même la table
avec `CreateSQL` et une colonne d'id dimensionnée.

**L'id est adressé par le contenu.** Le même enregistrement produit le même id par
l'un ou l'autre chemin, et un enregistrement modifié en produit un nouveau — ce qui
permet à une relance d'être un no-op au lieu d'un doublon.

## Référence

- [pkg.go.dev](https://pkg.go.dev/github.com/AreteAcademy/brevis/sdk) — l'API complète
- [`examples/`](https://github.com/AreteAcademy/brevis/tree/master/examples) — douze exemples exécutables
- [`CHANGELOG.md`](https://github.com/AreteAcademy/brevis/blob/master/CHANGELOG.md) — l'historique version par version
