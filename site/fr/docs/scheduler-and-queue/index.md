# Scheduler et file

> Deux boucles indépendantes — qui crée les runs, qui les exécute, et pourquoi elles sont séparées.

*https://brevis.sh/fr/docs/scheduler-and-queue/ · brevis.sh docs (fr)*

---

brevis.sh sépare **créer** un run de l'**exécuter**. Deux boucles tournent
ensemble et ne dépendent d'aucune des deux.

```
scheduler                          queue
─────────                          ─────
reads the schedule
materialises the slot
creates the Run  ─────────────►  (pending in the database)
                                   claims it
                                   walks the graph
                                   executes each step
                                   records state and log
```

## Pourquoi les séparer

Une boucle unique qui planifie et exécute a un défaut : quand l'exécution se
bloque, la planification s'arrête avec elle. Les douze prochaines heures de
slots n'existent tout simplement pas, et personne ne le remarque tant que
quelqu'un ne cherche pas des données qui ne sont jamais arrivées.

Avec les deux séparées :

- le **scheduler** peut tomber et la file continue de vider ce qui a été créé ;
- la **file** peut tomber et les slots continuent d'être matérialisés, pour être
  exécutés à son retour ;
- **le retraitement ne dépend pas de l'horloge** — `backfill` crée les runs
  passés par le même chemin que celui qu'utiliserait cron.

## Le processus

Les deux boucles vivent dans la même commande :

```bash
brevis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| option | défaut | |
|---|---|---|
| `--interval` | `10s` | intervalle entre les cycles du scheduler |
| `--concurrency` | `5` | **runs** simultanés |
| `--max-pods` | `5` | **steps** simultanés au total |

`--concurrency` et `--max-pods` comptent des choses différentes, à dessein : cinq
runs de trois steps parallèles chacun feraient quinze pods si la seule limite
portait sur les runs.

:::note `serve` ne matérialise pas les planifications
Le processus de l'API démarre le même scheduler, mais **sans la boucle** — il
n'est là que pour servir les déclenchements manuels depuis l'interface, de sorte
que la règle « le scheduler crée les runs » garde un seul propriétaire. Pour que
les planifications se déclenchent, un `brevis scheduler` doit tourner à côté.
:::

## L'instantané de la définition

Quand le scheduler crée un run, il stocke **la définition du workflow à
l'intérieur du run**. L'exécution lit cet instantané, pas le YAML sur disque.

La raison est concrète : entre le déclenchement et l'exécution, quelqu'un a pu
modifier le fichier et déployer. Sans l'instantané, un run créé à 05:00 avec une
définition s'exécuterait à 05:02 avec une autre — et le log n'aurait aucun moyen
d'expliquer la différence.

## Nouvelles tentatives

Un run en échec est tenté **trois fois**, et le délai **double** : avec les
valeurs par défaut, elles tombent à **0s, 30s et 1m30s**.

```bash
brevis scheduler --max-attempts 3 --retry-backoff 30s --retry-backoff-max 1h
```

| option | défaut | |
|---|---|---|
| `--max-attempts` | `3` | tentatives par run, en comptant la première |
| `--retry-backoff` | `30s` | le premier délai, doublé à chaque tentative suivante |
| `--retry-backoff-max` | `1h` | plafond de ce délai |

Le processus affiche le calendrier au démarrage, pour qu'on n'ait jamais à le
déduire de deux nombres :

```
scheduler and dispatcher are up  max_attempts=3 retry_backoff=30s retry_at="0s, 30s, 1m30s"
```

:::tip
**Un fournisseur qui limite le débit veut plus de temps.** Ces pipelines
échouent pour une raison avant toutes les autres : un amont transitoire — une API
qui répond 200 avec « vous avez atteint votre limite de requêtes », un à-coup de
quota d'entrepôt. Cela met environ une minute à se résorber, et une nouvelle
tentative qui se termine avant est une tentative qui n'a jamais eu lieu.
`--retry-backoff 1m` place les tentatives à 0s, 1m et 3m.
:::

La nouvelle tentative porte sur le **run**, et elle ne relance que les steps qui
ont échoué — un `dbt_build` en échec ne relance pas le fetch qui le précède et
qui a réussi.

La tentative est persistée, ce n'est pas un état du processus. Redémarrer le
worker n'efface pas ce qui était déjà connu de ce run.

Le numéro de tentative du **run** entre dans le nom du pod. Sans lui, une
nouvelle tentative redémarrerait le run de zéro, rencontrerait le pod de la
tentative précédente sous le même nom et resterait bloquée en `Pending` pour
toujours.

## Backfill

Matérialise les slots passés d'un workflow déjà publié :

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

```
  31 run(s) de backfill enfileirados para daily (2026-01-01 a 2026-01-31)
  rode `brevis scheduler` para executa-los
```

Il **met en file, il n'exécute pas.** L'exécution est le travail du
`scheduler` — la même séparation encore une fois. `--to` inclut la journée
entière.

## Déclenché par un atterrissage

Un workflow peut s'exécuter parce que des données sont arrivées, plutôt que
parce que l'horloge l'a dit :

```yaml
trigger:
  on_landed:
    - bigquery://acme-prod/bronze/orders
    - bigquery://acme-prod/bronze/*
  debounce: 5m
```

Chaque step déclare déjà ce qu'il a écrit, sur le canal qui porte les phases —
c'est ce qui alimente `/data`. Un workflow s'abonne à l'un de ces noms et démarre
quand quelque chose y atterrit.

Un `/*` final abonne au dataset entier. Il existe pour `auto_table`, qui crée une
table par route : le dataset est la seule chose qu'on puisse nommer pour des
tables que personne n'a déclarées. Il correspond à `bronze/orders` et non à
`bronze_raw/orders`.

### La fenêtre, et pourquoi dix atterrissages font un seul run

`debounce` fusionne une rafale. Dix tables qui atterrissent en cinq minutes
démarrent **un** run, pas dix, et le run indique lesquelles :

```
BREVIS_AUTO_LANDED=bigquery://acme-prod/bronze/orders,bigquery://acme-prod/bronze/items
```

La liste exacte se trouve aussi dans `BREVIS_AUTO_PARAMS` au format JSON, ce
qu'il faut lire quand une cible pourrait contenir une virgule.

La fenêtre est découpée à partir du moment où **le moteur a enregistré
l'atterrissage**, pas du moment où le step dit qu'il a eu lieu. Un step qui
rapporte l'instant où sa requête a commencé rapporte quelque chose de vrai et
quelque chose d'ancien, et une horloge décalée scinderait sinon une rafale en
deux runs.

Omettez `debounce` et chaque atterrissage démarre un run.

### Ce qu'il ne fera pas

**Un workflow n'est jamais démarré par sa propre écriture.** Le moteur ignore un
atterrissage dont l'auteur est le workflow abonné et journalise pourquoi — sans
cela, un workflow qui lit et écrit la même table se déclencherait lui-même à
l'infini.

Il est refusé **au moment où il se déclenche** et non lors de la vérification du
fichier, et c'est une limite plutôt qu'un oubli : `brevis validate` lit des
fichiers et aucun fichier ne déclare ce qu'un step écrit. Une cible d'entrepôt
est décidée par la connexion, pas par le YAML. Ainsi `validate` accepte un
workflow qui sera refusé à l'exécution, à chaque fois, avec la raison dans le
log.

**Le premier cycle ne démarre rien.** Un trigger commence à compter à partir du
moment où il est mis en service, comme une planification — sinon, en publier un
déclencherait un run pour chaque atterrissage de l'historique du catalogue.

## Alertes

```bash
export BREVIS_SLACK_WEBHOOK='https://hooks.slack.com/services/...'
export BREVIS_UI_URL='https://brevis.example.com'
```

Sans le webhook, le processus avertit au démarrage que les échecs ne seront pas
signalés. Une installation qui échoue en silence a tendance à être découverte par
le client, pas par l'équipe.

`BREVIS_UI_URL` construit le lien du run dans l'alerte — sans lui, l'alerte dit
ce qui a échoué mais oblige le lecteur à chercher le run à la main.

## Étapes suivantes

- [Un pod par step](/fr/docs/pod-per-step/index.md) — où chaque step s'exécute réellement
- [CLI](/fr/docs/cli/index.md) — toutes les options de `scheduler` et `backfill`
