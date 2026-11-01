---
title: Paramètres
description: Ce qui change entre deux runs du même workflow, sans modifier le fichier.
group: Référence
order: 9
slug: parameters
---

Un workflow peut déclarer des **paramètres de run** : des valeurs qui changent
entre deux déclenchements sans que le fichier change.

## Déclarer

```yaml
params:
  - name: load_full
    type: boolean
    default: "false"

  - name: start_date
    type: string
    pattern: '^\d{4}-\d{2}-\d{2}$'

steps:
  - id: run
    run: dbt build --vars '{"load_full":"{{ .load_full }}"}' --select bronze+
```

| champ | | |
|---|---|---|
| `name` | **requis** | le nom utilisé dans le template et dans `--param` |
| `type` | | `string`, `boolean`, `integer`, ou `list|<type>` pour plusieurs valeurs |
| `default` | | utilisé quand le déclenchement ne fournit aucune valeur |
| `pattern` | | expression régulière que la valeur doit satisfaire |
| `title` | | le libellé affiché par le formulaire, jusqu'à 60 caractères ; le nom sinon |
| `group` | | regroupe les paramètres dans le formulaire, jusqu'à 60 caractères |
| `warn` | | ce que coûte une valeur différente de la valeur par défaut, affiché sous le champ, jusqu'à 200 caractères |
| `uses` | | la clé du connecteur concerné par le paramètre (`bigquery`, `postgres`), pour son logo |

Un paramètre **sans `default`** est requis : un déclenchement qui l'omet échoue
avant d'exécuter le moindre step.

`title`, `group`, `warn` et `uses` sont la façon dont le formulaire présente le
paramètre, et **aucun d'eux ne change ce qu'une valeur peut être**. Chacun tient
sur une ligne ; `uses` se compose de lettres minuscules, de chiffres et de `-`.
La publication refuse tout autre contenu, en nommant le paramètre.

```yaml
params:
  - name: load_full
    type: boolean
    default: "false"
    title: Full load
    group: Load
    warn: reprocesses the whole table; it costs
    uses: bigquery
```

## Une liste de valeurs

`list|<type>` déclare un paramètre qui porte plusieurs valeurs — les tables à
charger, les couches à reconstruire, les fenêtres à retraiter :

```yaml
params:
  - name: tables
    type: list|string
    default: "users,orders"

  - name: layers
    type: list|string
    enum: [bronze, silver, gold]     # an enum restricts each ITEM
    default: "bronze"

  - name: days
    type: list|integer
    default: "1,7,30"
```

L'élément peut être `string`, `integer` ou `boolean`. Chaque item est validé
séparément : `list|integer` recevant `1,x,30` échoue en nommant l'**item 2**
plutôt que la valeur entière.

C'est **une chaîne séparée par des virgules** partout : dans la base, dans
`BREVIS_RUN_PARAMS` et dans la commande. C'est ce qui permet de l'utiliser dans
un step sans aucun cas particulier —

```yaml
run: dbt build --select {{ .tables }}      # dbt build --select users,orders
```

— et c'est pourquoi une virgule ne peut pas apparaître dans un item. Un item vide
et un item répété sont aussi refusés : une répétition double silencieusement ce
que le step fait par item.

### Comme tableau, dans un step

```go
for _, table := range p.Run.ParamList("tables") {
    load(table)
}
```

```python
from brevis import run

for table in run.param_list("tables"):
    load(table)
```

Les deux renvoient `[]` pour un paramètre que personne n'a renseigné, plutôt
qu'une liste contenant une chaîne vide : itérer sur « rien à faire » ne doit rien
faire. Les items reviennent sous forme de chaînes — y compris pour
`list|integer` — parce que le step sait mieux que la bibliothèque quoi faire d'un
nombre.

### Dans l'interface

Une liste avec un `enum` s'affiche sous forme de cases à cocher, qui n'ont pas
besoin de JavaScript et que le navigateur soumet à raison d'une valeur par case
cochée. Sans `enum`, c'est un champ texte dont les items deviennent des puces
supprimables, et avec JavaScript désactivé il reste un champ contenant
`users,orders` — exactement ce que le serveur attend.

### En ligne de commande

```bash
brevis run wf.yaml --param tables=events,sessions --param days=1,30
```

:::note Un moteur plus ancien ne la lira pas
Le type est stocké avec le workflow, et un moteur antérieur à cette
fonctionnalité refuse `list|string` à la publication comme un type inconnu.
Mettez le moteur à jour avant de publier un workflow qui en utilise un.
:::

## L'utiliser dans la commande

La valeur entre dans `run` via un template, entre doubles accolades avec un
point :

```yaml
run: ./load.sh --since {{ .start_date }}
```

## La passer en ligne de commande

```bash
brevis run wf.yaml --param load_full=true
brevis run wf.yaml --param load_full=true --param start_date=2026-01-01
```

`--param` est répétable. Une entrée sans `=` est une **erreur** :

```
error: --param "load_full": use key=value
```

C'est voulu. `--param load_full`, en oubliant la valeur, s'exécuterait avec la
valeur par défaut — et l'opérateur croirait que le backfill s'est fait avec le
paramètre qu'il visait.

## Dans un backfill

Les valeurs s'appliquent à **chaque** slot de la plage. C'est le cas d'usage
central : « retraiter tout janvier avec `load_full=true` ».

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

## Dans l'interface

Un workflow avec `params` obtient un **formulaire** à la place du simple bouton
de déclenchement. Les champs viennent de la déclaration, et `pattern` est validé
avant la soumission.

## Dans l'environnement du step

Les paramètres de run entrent aussi dans l'**environnement** de chaque step, avec
un préfixe. Cela existe pour qu'un fetcher écrit avec le [SDK](/docs/sdk/) puisse
les voir sans rien recevoir en argument :

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    if p.Run.Params["load_full"] == "true" {
        p.Source.URL += "&full=1"
    }
    return nil
},
```

## Paramètres automatiques

Chaque run porte aussi des **paramètres automatiques** : des valeurs que le
moteur calcule lui-même. Personne ne les déclare, chaque run les a, et ils sont
sur l'écran du run.

Le plus important est `adjusted_at`, et le bug qu'il supprime mérite d'être
nommé. Un fetcher lit `now()`, soustrait sa fenêtre et demande au fournisseur les
deux dernières heures. Sur un run qui démarre à l'heure, c'est juste. Sur un run
que la file a retardé de quarante minutes, c'est faux de quarante minutes — et
ces quarante minutes n'appartiennent à **aucun run**, parce que le slot suivant
lit lui aussi son propre `now()`. Rien n'échoue ; les données manquent
simplement, et on s'en aperçoit des semaines plus tard.

Le moteur fournit donc une horloge à la place. Lisez `adjusted_at` là où vous
auriez lu `now()` :

| param | | |
|---|---|---|
| `adjusted_at` | toujours | l'horloge que ce run doit lire : le slot quand il y en a un, le démarrage sinon |
| `date` | toujours | `adjusted_at` au format `YYYY-MM-DD`, UTC — la partition, le dossier, le `WHERE` |
| `delay_seconds` | toujours | de combien cette tentative était en retard sur son slot ; `0` sur un run manuel sans slot |
| `previous_error` | toujours | le run précédent n'a pas réussi |
| `scheduled_at` | avec un slot | le slot demandé par le cron |
| `started_at` | | quand cette tentative a commencé ; une nouvelle tentative le déplace |
| `interval_start` | avec un slot | le slot **précédent** |
| `interval_end` | avec un slot | ce slot, **exclu** |
| `previous_success_at` | | le slot du dernier run qui a réussi |

Un run a un slot quand la planification l'a créé, quand un backfill l'a créé, ou
quand il a été déclenché à la main **pour un slot passé** : le champ `slot` du
déclenchement (`2026-10-27T04:00:00Z`) relance ce slot, avec les params de ce
slot. Un slot que le cron ne déclenche jamais est refusé, avec indication du plus
proche qui le précède.

**Tous les horodatages ici sont en UTC**, écrits en RFC 3339 avec un `Z`, et
l'écran du run les affiche aussi en UTC — la page et
`$BREVIS_AUTO_ADJUSTED_AT` ne sont donc jamais en désaccord. `date` est le **jour
UTC**, qui pour un workflow dont le slot passe minuit UTC n'est pas le jour
local : un `0 22 * * *` en `America/Sao_Paulo` a pour `date` le jour suivant. Le
`ds` d'Airflow se comporte de la même façon, pour la même raison — le slot est
un instant, et un instant n'a un seul jour qu'une fois le fuseau horaire choisi.

`interval_start` et `interval_end` viennent du **cron**, pas de l'historique : un
backfill d'un slot de mars produit la fenêtre qu'avait mars, pas la fenêtre que
les runs de ce workflow décrivent aujourd'hui. Un pipeline qui demande
`[start, end)` ne chevauche jamais et ne laisse jamais de trou, quel que soit son
retard et le nombre de ses nouvelles tentatives.

Ce sont un **instantané**, calculé au démarrage du run et stocké sur celui-ci.
`previous_error` est un fait sur l'instant où ce run a commencé ; le recalculer
demain — après que le run précédent a été relancé et a réussi — répondrait à une
autre question avec le même nom.

### Les lire

Dans un step shell, une variable par valeur, pour que rien n'ait à être parsé :

```yaml
steps:
  - id: extract
    run: |
      curl "https://api.example.com/events?since=$BREVIS_AUTO_INTERVAL_START&until=$BREVIS_AUTO_INTERVAL_END" \
        > "/data/$BREVIS_AUTO_DATE.json"
```

L'ensemble complet se trouve aussi dans `$BREVIS_AUTO_PARAMS` sous forme d'un
objet JSON, pour un step qui préfère parser une seule fois.

En Go, le [SDK](/docs/sdk/) les lit pour vous :

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    start, end, ok := p.Run.Auto.Window()
    if !ok { // no schedule: fall back to a fixed window
        start, end = p.Run.Auto.Now().Add(-24*time.Hour), p.Run.Auto.Now()
    }
    p.Source.URL += "&from=" + start.Format(time.RFC3339) +
        "&to=" + end.Format(time.RFC3339)
    return nil
},
```

`Auto.Now()` est l'horloge à lire à la place de `time.Now()`. En dehors du
moteur — un fetcher que quelqu'un lance à la main — c'est l'horloge murale, donc
rien n'a besoin d'être traité à part pour le développement local.

En Python, `pip install brevis` lit les mêmes valeurs :

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

`window()` renvoie `None` quand le workflow n'a pas de planification, plutôt
qu'une paire de dates nulles — une requête depuis l'epoch sélectionne tout, et
cet échec ne doit pas être silencieux. Le `or` ci-dessus est tout le repli.

### Dans dlt, rien à lire du tout

[dlt](https://dlthub.com) garde son propre curseur, dans un état qu'il écrit vers
la destination. Cet état n'avance que **vers l'avant**, ce qui convient jusqu'à ce
qu'on relance un ancien slot : le curseur indique toujours aujourd'hui, donc le
backfill lit la fenêtre d'aujourd'hui, et la réponse de dlt à cela est de
supprimer l'état.

La fenêtre que Brevis fournit vient du cron, donc le run de mars produit la
fenêtre de mars quel que soit le nombre de nouvelles tentatives. dlt l'accepte —
les noms sont les siens, lus avant qu'il aille chercher Airflow, et le moteur les
définit à chaque run planifié :

```python
@dlt.resource(primary_key="id")
def events(
    updated_at=dlt.sources.incremental(
        "updated_at",
        initial_value=datetime(1970, 1, 1, tzinfo=timezone.utc),
        allow_external_schedulers=True,   # this line is the whole integration
    )
):
    yield from api.events(since=updated_at.start_value, until=updated_at.end_value)
```

`allow_external_schedulers=True` est toute l'intégration. `$DLT_INTERVAL_START` et
`$DLT_INTERVAL_END` deviennent l'`initial_value` et l'`end_value` de dlt,
semi-ouverts des deux côtés — la ligne située exactement sur le début est
incluse, celle située exactement sur la fin ne l'est pas, ce qui fait que des
slots consécutifs n'ont ni trou ni chevauchement.

Un incremental avec un `end_value` **ne touche pas à l'état persisté**. C'est la
partie qui compte : relancer un slot est idempotent, et un backfill de mars ne
déplace pas le curseur dont dépend le run nocturne.

Deux choses à savoir avant que cela fonctionne :

- **Le curseur doit être un datetime, pas une chaîne.** dlt ne convertira pas un
  curseur `text` pour la comparaison et refuse de rejoindre le scheduler, avec
  une `JoinSchedulerError` qui ne le dit pas clairement. Convertissez-le sur la
  resource — `add_map` — ou typez l'`initial_value` comme ci-dessus.
- **Un workflow sans planification ne fournit rien**, et dlt lève alors
  `ExternalSchedulerNotAvailable` au lieu de retomber discrètement sur son
  curseur. Si un pipeline doit fonctionner dans les deux cas, laissez
  `allow_external_schedulers` désactivé et passez vous-même la fenêtre depuis
  `run.window()`.

Déclarer `tools: [dlt]` sur le step est une chose distincte, et ne fait que
dessiner la puce sur le graphe — la fenêtre est fournie dans tous les cas.

## L'instantané

Les paramètres sont stockés **dans le run**, pas lus depuis le workflow au moment
de l'exécution. Le run porte l'entrée avec laquelle il a été déclenché, et le log
d'une exécution de janvier continue d'afficher les valeurs de janvier même après
le changement de la valeur par défaut.

## Étapes suivantes

- [Configuration](/docs/configuration/) — variables d'environnement du processus
- [SDK](/docs/sdk/) — comment un fetcher lit le contexte du run
- [Contexte entre steps](/docs/context/) — ce qu'un step publie pour le suivant
