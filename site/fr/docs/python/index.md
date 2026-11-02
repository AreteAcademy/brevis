# Python

> pip install brevis — le contexte entre steps, l'horloge du run et les métriques propres à votre pipeline, sans aucune dépendance.

*https://brevis.sh/fr/docs/python/ · brevis.sh docs (fr)*

---

```bash
pip install brevis
```

```python
from brevis import context, metrics, run

bucket = context.get("extract.bucket")
since, until = run.window()          # the window this run covers

context.set(rows=48213, watermark="2026-09-07T03:00:00Z")
metrics.set("rows_loaded", 48213)    # reaches the engine's /metrics
```

**Aucune dépendance, jamais.** Elle lit une variable d'environnement, écrit un
fichier et affiche une ligne sur stdout. Nécessite Python 3.10+.

Ce n'est pas un portage du SDK Go : pas de drivers, pas de pagination, pas d'ids
d'ingestion. Elle existe pour qu'une équipe puisse utiliser pandas, Polars ou dbt
tout en obtenant ce pour quoi un orchestrateur existe.

## Lire le contexte

Une clé est toujours qualifiée par le step qui l'a publiée :

```python
context.get("extract.bucket")            # the value
context.get("extract.nope", default=0)   # absent is not an error
context.of("extract")                    # everything that step published
context.published()                      # what THIS step has published so far
```

`context.get("bucket")` sans step est **refusé**, en nommant les steps qui l'ont
publiée. Les steps sont isolés — `extract` et `transform` peuvent tous deux
publier `bucket` sans que l'un le perde — et une clé nue jette cela à la poubelle
dès que deux d'entre eux le font. Chercher et en choisir un est une priorité par
accident : cela marche jusqu'à ce que, silencieusement, cela ne marche plus.

Lire un step dont celui-ci ne dépend pas est une erreur, et le message indique le
correctif.

## Écrire

```python
context.set(name="Daniel")
context.set(label="Name")
# both survive: {"name": "Daniel", "label": "Name"}
```

Les appels **fusionnent**. La même clé deux fois garde la dernière écriture ; une
clé différente laisse la première intacte. **Il n'y a pas d'argument de step** — le
seul espace de noms qu'un processus puisse écrire est le sien, ce qui rend
l'isolation structurelle plutôt qu'une règle.

Rien n'atteint le disque avant la sortie du processus : un step qui meurt
brutalement ne publie donc rien. C'est correct : la sortie d'un step planté
décrivait un travail qui ne s'est pas terminé.

## L'horloge que ce run doit lire

Chaque run porte aussi des **paramètres automatiques** : des valeurs que le moteur
calcule lui-même. Personne ne les déclare, chaque run les a, et ils répondent à la
question qu'un pipeline résoudrait autrement avec `datetime.now()`.

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

:::warning Pourquoi pas `datetime.now()`
`run.now()` est le **slot** sur un run planifié. Un fetcher qui lit
`datetime.now()` et soustrait deux heures est juste sur un run qui démarre à
l'heure et faux de quarante minutes sur un run que la file a retardé — et ces
quarante minutes n'appartiennent à **aucun run**, parce que le slot suivant lit
lui aussi son propre `now()`. Rien n'échoue, et le trou est découvert des semaines
plus tard.
:::

| | |
|---|---|
| `run.now()` | l'horloge : le slot, ou le démarrage du run |
| `run.window()` | `(start, end)`, fin exclue — ou `None` |
| `run.auto()` | les paramètres automatiques, ci-dessous |
| `run.context()` | id, tentative, déclencheur, params et auto |
| `run.params()` · `run.param(name)` | les paramètres déclarés dans le workflow |
| `run.map_index()` · `run.map_value()` | l'instance, sous `for_each:` |

`window()` vient du **cron**, pas de l'historique : un backfill d'un slot de mars
produit la fenêtre qu'avait mars. Demander `[start, end)` ne chevauche jamais et ne
laisse jamais de trou, quel que soit le retard du run et le nombre de ses
nouvelles tentatives.

Elle renvoie `None` plutôt qu'une paire de dates nulles quand le workflow n'a pas
de planification — une requête depuis l'epoch sélectionne tout, et cet échec ne
doit pas être silencieux. Le `or` ci-dessus est tout le repli.

### Les paramètres automatiques

| champ | |
|---|---|
| `date` | `"2026-09-08"` — la partition, le dossier, le `WHERE` |
| `adjusted_at` | l'horloge à lire à la place de `now()` |
| `scheduled_at` | le slot demandé par le cron ; `None` sur un run manuel |
| `started_at` | quand **cette tentative** a commencé ; une nouvelle tentative le déplace |
| `delay_seconds` | de combien cette tentative était en retard ; jamais négatif |
| `interval_start` · `interval_end` | la fenêtre, fin exclue |
| `previous_error` | le run précédent n'a pas réussi |
| `previous_success_at` | le slot du dernier run qui **a** réussi — ce qui rend `previous_error` exploitable |

Exécutez le script à la main et `run.now()` **est** l'horloge murale et `window()`
est `None`, donc il n'y a aucune branche à écrire pour le développement local.

## Une instance d'un step mappé

Sous [`for_each:`](/fr/docs/workflows/index.md), le moteur exécute un processus par élément
et dit à chacun lequel il a reçu :

```python
partition = run.map_value()
if partition is None:
    raise SystemExit("this step is meant to run under `for_each:`")

load(f"/data/{partition}.csv")
```

Une chaîne JSON arrive **sans ses guillemets** — `for_each` sur `["2026-01"]`
transmet au step `2026-01`, pas `"2026-01"`. Tout le reste — un objet, un nombre,
une liste — arrive sous forme de son JSON, donc `json.loads(run.map_value())`.

Les deux valent `None` sur un step qui n'est pas mappé. `None`, et non `-1` ou
`""` : une valeur sentinelle est un nombre sur lequel quelqu'un finit par faire de
l'arithmétique, et une chaîne vide est indiscernable d'un élément qui *est* la
chaîne vide.

Un step mappé ne publie **aucun contexte** en aval — quatre instances ne peuvent
pas partager une clé. Un step qui doit transmettre quelque chose écrit un fichier,
ou un step après lui compte ce qui a atterri.

## Des métriques que seul votre pipeline connaît

Le moteur mesure déjà ce qu'il peut voir : durée, tentatives, profondeur de la
file. Ce qu'il n'a aucun moyen de savoir, c'est combien de lignes ont atterri,
combien le fournisseur en a rejeté, ou combien d'octets ont été écartés. Le step
le sait, et la bibliothèque le transporte.

```python
from brevis import metrics

metrics.set("rows_loaded", 48213)          # a gauge: the last value wins
metrics.inc("vendor_rejected_total")       # a counter: it adds up
metrics.inc("bytes_discarded_total", 4096)
```

Elles ressortent du `/metrics` **du moteur**, sur le port du scheduler, étiquetées
avec le workflow et le step :

```
brevis_step_rows_loaded{workflow="daily_sales",step="load"} 48213
brevis_step_vendor_rejected_total{workflow="daily_sales",step="load"} 5
```

| | |
|---|---|
| `metrics.set(name, value)` | une gauge. Après la sortie du step, la valeur reste jusqu'à ce que le run suivant en écrive une autre |
| `metrics.inc(name, by=1)` | un compteur. S'additionne entre steps et entre runs |

### Pourquoi cela n'ouvre pas de port

Parce qu'**un step n'est pas scrutable**. `:9090` appartient au scheduler et à
l'API — des processus de longue durée avec une adresse stable, scrutés toutes les
quinze à soixante secondes. Un step démarre dans son propre pod, s'exécute pendant
quarante secondes et s'arrête : un port qu'il ouvrirait ne serait jamais scruté, ou
une fois par chance.

Les trois réponses connues sont un Pushgateway (un composant à opérer, et des
séries qui restent tant que personne ne les supprime), un collecteur OTLP (un
composant **et** une dépendance dans cette bibliothèque, qui n'en a aucune et
n'en aura pas), ou laisser l'**orchestrateur** les porter. Brevis lit déjà la
sortie standard de chaque step à la recherche de lignes `@brevis:` — c'est ainsi
que les phases du SDK atteignent le graphe — et il a déjà un compteur et un
endpoint Prometheus.

La bibliothèque écrit donc **une ligne sur stdout** et le moteur fait le reste. La
métrique arrive étiquetée avec le workflow et le step gratuitement, parce que le
moteur sait ce qu'il exécutait ; via un Pushgateway, cette bibliothèque devrait
s'étiqueter elle-même, et se tromperait.

L'écriture est un `print(flush=True)`, parce que le moteur lit ce canal **en
direct**. Une ligne mise en tampon arrive à la sortie du processus — ce qui, pour
un step qui dure une heure, a une heure de retard, et pour un step tué, n'arrive
jamais.

### Ce qu'une métrique refuse

**Un nom invalide est refusé, bruyamment.** Prometheus accepte les lettres, les
chiffres et le tiret bas, et le premier caractère ne peut pas être un chiffre. Un
nom qu'il refuse ne produit pas une métrique cassée — il produit une exposition
qu'il **refuse de parser**, et le **scrape entier** est perdu, avec toutes les
autres métriques. Donc `rows-loaded` lève `MetricError` ici, là où l'erreur a été
commise, plutôt que d'arriver sous un nom que personne n'a écrit.

| refusé | parce que |
|---|---|
| `rows-loaded`, `rows loaded`, `2rows` | Prometheus ne le parsera pas, et le scrape entier part avec |
| `metrics.set("ok", True)` | `bool` est un `int` en Python, et rapporter `1` est un nombre dont le sens dépend de le savoir. Rapportez le compte ou la durée, pas l'état |
| `metrics.inc("x", -1)` | un compteur ne fait qu'augmenter. Chaque `rate()` dessus le suppose, et la casse apparaît dans un graphique des semaines plus tard |

Exécutez le script à la main et la ligne va quand même sur stdout, où elle se lit
pour ce qu'elle est. Rien ne la collecte et rien n'échoue — comme `context.set()`
sur un portable.

## Ce qu'elle refuse

| | parce que |
|---|---|
| plus de 4096 octets | la plateforme **tronque** au lieu de refuser, donc un objet trop gros arrive coupé en deux et se lit en aval comme « n'a rien publié » |
| une valeur qui n'est pas du JSON | un `datetime` devient `"..."` ou disparaît selon qui sérialise. L'erreur nomme la clé et suggère `.isoformat()` |
| une clé qui n'est pas une chaîne | `{1: "x"}` revient sous la forme `{"1": "x"}` et la recherche rate |

Le plafond est le nombre de la plateforme, pas le nôtre. Toute fonctionnalité de
« contexte entre tâches » échoue de la même façon — quelqu'un met des **données**
là où seul du **contexte** tient. Quatre kilo-octets contiennent un watermark, un
compteur ou une liste de partitions. Publiez une référence et laissez les lignes où
elles sont.

:::danger Ce n'est pas un coffre-fort de secrets
Tout ce qui est publié est visible de quiconque peut voir le run — c'est dans le
statut du pod et dans l'historique du run. Publiez un chemin, pas une URL signée.
:::

## L'exécuter à la main

En dehors de Brevis, il n'y a pas d'entrée et rien ne lit la sortie. `get()` renvoie
la valeur par défaut, `set()` accepte et jette, et la bibliothèque le dit une fois
au niveau INFO. Elle **ne lève pas d'erreur** : un script qui ne peut pas être
exécuté à la main ne peut pas être développé. La validation s'exécute quand même,
donc une valeur qui fonctionne sur votre portable fonctionne en production.

## Tester votre step

Tout le contrat tient en deux variables d'environnement, il n'y a donc rien à
mocker :

```python
def test_my_step(monkeypatch, tmp_path):
    monkeypatch.setenv("BREVIS_INPUT", '{"extract": {"rows": 42}}')
    monkeypatch.setenv("BREVIS_OUTPUT", str(tmp_path / "out"))
    assert context.get("extract.rows") == 42
```

## Versions

La bibliothèque est versionnée de façon autonome, séparément du moteur et du SDK
Go.

:::warning N'utilisez pas `0.2.0`
Elle a été publiée en wheel seule sur PyPI. `0.2.1` est le même code, complet.
:::

`brevis.run` a besoin d'un moteur en **`v0.9.0` ou plus récent** pour avoir quelque
chose à lire ; sur un moteur plus ancien, chaque champ est vide et `run.now()` est
l'horloge murale — ce qu'elle fait aussi en dehors du moteur.

## Étapes suivantes

- [Bibliothèques clientes](/fr/docs/libraries/index.md) — le contrat et les autres langages
- [Contexte entre steps](/fr/docs/context/index.md) — le concept
- [Paramètres](/fr/docs/parameters/index.md) — les paramètres déclarés
