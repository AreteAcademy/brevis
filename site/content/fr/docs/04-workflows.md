---
title: Workflows
description: Le format YAML complet — steps, dépendances, images, ressources et planification.
group: Concepts
order: 4
slug: workflows
---

Un workflow est un fichier YAML. Il déclare **ce qu'il faut exécuter**, **dans
quel ordre** et **avec quel runtime** — et rien d'autre.

## Structure minimale

```yaml
name: hello
steps:
  - id: only
    run: echo done
```

`name` identifie le workflow dans la base et dans l'interface. `steps` est la
liste des steps, chacun avec un `id` unique et un `run`.

### `description` — à quoi il sert

Facultatif, de forme libre, et lu par rien dans le moteur :

```yaml
name: daily-orders
description: >
  Pulls yesterday's orders from the vendor and lands them in bronze.
  Runs at 04:00 because the vendor closes its books at 03:30.
steps:
  - id: extract
    run: python fetch.py
```

Il apparaît en haut de l'écran du workflow, au-dessus des compteurs et de la
planification. Le graphe dit déjà ce qu'un flux **fait** ; c'est le seul endroit
qui dit pourquoi il existe, ce dont a réellement besoin la personne qui l'ouvre
pour la première fois à trois heures du matin.

Un workflow sans description n'affiche rien à cet endroit — pas un bloc vide, qui
se lit comme quelque chose qui n'a pas réussi à se charger.

## Ordre : chain ou dag

```yaml
type: chain   # order is the file's order
type: dag     # order comes from depends_on
```

`chain` est du sucre syntaxique : le parseur transforme la séquence en arêtes, et
**le moteur ne connaît que les DAG**. Utilisez `dag` quand l'ordre n'est pas
linéaire :

```yaml
type: dag

steps:
  - id: prepare
    run: ./prepare.sh

  # Siblings: they depend on the same step, so they run in parallel.
  - id: extract
    run: ./extract.sh
    depends_on: [prepare]

  - id: validate
    run: ./validate.sh
    depends_on: [prepare]

  - id: publish
    run: ./publish.sh
    depends_on: [extract, validate]
```

Le runner parcourt le graphe **par niveaux** : tout ce qui se trouve dans un
niveau s'exécute en parallèle, et le niveau suivant ne démarre que lorsque le
précédent est entièrement terminé.

:::warning Un échec arrête tout le niveau
Le runner s'arrête au **premier** échec d'un niveau, sans démarrer le suivant.
Continuer après une erreur produirait un résultat partiel qui a l'air complet.
:::

## Planification

```yaml
schedule: "0 5 * * *"   # five-field cron
concurrency: 1
```

Sans `schedule`, le workflow est manuel : il ne s'exécute que par déclenchement
dans l'interface, par `brevis run` ou par `backfill`.

`concurrency: 1` limite les runs simultanés d'un même workflow — c'est ce qui
empêche un `*/15` de se chevaucher lui-même quand un run dépasse quinze minutes.

Deux workflows **différents** qui écrivent les mêmes tables ont besoin d'un
**groupe**. Un workflow quotidien et un `*/15` qui construisent tous deux les
mêmes modèles dbt déclarent chacun :

```yaml
concurrency:
  group: jaffle_models   # the shared resource, named the same in both files
  limit: 1               # optional; a group without one means 1
```

Les runs de tous les workflows du groupe comptent pour une seule limite, quoi
qu'il les ait déclenchés — la planification, un atterrissage, un backfill ou un
clic. Un run qui trouve le groupe plein **attend dans la file** ; il n'est pas
ignoré et n'échoue pas. Un troisième workflow qui écrit les mêmes tables le
rejoint en nommant le groupe. Pendant l'attente, la page du run l'indique : quel
groupe, sa limite, et quels runs occupent les slots.

- Chaque membre déclare la **même limite**. `validate` et `publish` refusent un
  groupe déclaré avec 1 dans un fichier et 3 dans un autre, y compris face à des
  membres déjà publiés.
- Un groupe sérialise les **runs Brevis**. Un `dbt run` lancé par quelqu'un sur
  sa propre machine n'est pas dans cette file, et échoue de la même façon.

## Image et ressources

```yaml
image: us-central1-docker.pkg.dev/example/apps/dbt:1.10.3
resources:
  cpu: 200m
  memory: 1Gi
  limits: {memory: 2Gi}
```

Déclarées en haut, elles sont la **valeur par défaut de chaque step**. Chaque
step peut remplacer la sienne :

```yaml
steps:
  - id: bronze
    run: dbt build --select bronze+          # inherits the top-level image

  - id: notify
    image: ghcr.io/example/notify:0.3        # another runtime
    shell: false                             # distroless has no shell
    run: /notify --channel data
    resources: {cpu: 25m, memory: 32Mi, limits: {memory: 64Mi}}
    depends_on: [bronze]
```

C'est ce qui permet à un fetcher Go de coûter 12 Mo et 32Mi à côté d'un
`dbt build` de 1,9 Go, au lieu que les deux paient la taille du plus gros. Voir
[Un pod par step](/docs/pod-per-step/).

## Champs d'un step

| champ | | |
|---|---|---|
| `id` | **requis** | unique dans le workflow ; le nom affiché dans le graphe et les logs |
| `run` | | la commande |
| `image` | | l'image du step ; sans elle, hérite de celle du niveau supérieur |
| `host` | | envoie la commande à une machine que le moteur ne gère **pas** — une alternative à `image`, jamais un complément. Voir [`host:` dans le guide Kubernetes](https://github.com/AreteAcademy/brevis/blob/master/docs/KUBERNETES.md#a-third-place-a-step-can-run-host) |
| `shell` | `true` | `false` exécute sans shell — requis sur distroless |
| `depends_on` | | liste d'`id` qui doivent se terminer avant ; une entrée peut être `{step, label}` |
| `marker` | `false` | un step sans commande, pour un `start` ou un `end` |
| `resources` | | `cpu`, `memory` et `limits` pour ce step |
| `runtime`, `tools` | | le langage et les outils du step — voir [Runtime d'un step](/docs/runtime/) |
| `when` | `all_success` | sous quel état de ses dépendances ce step s'exécute — voir ci-dessous |
| `unless_empty` | | une clé de contexte qui décide s'il y a quelque chose à faire — voir ci-dessous |
| `for_each` | | une clé de contexte contenant une liste ; le step s'exécute une fois par élément — voir ci-dessous |
| `group` | | dessine ce step dans une boîte nommée et repliable — voir ci-dessous |
| `uses` | | un autre workflow dont les steps prennent la place de celui-ci — voir ci-dessous |
| `on_error` | | annonce les échecs de ce step — voir ci-dessous |

## Exécuter un step seulement quand il y a quelque chose à faire

```python
# in extract
context.set(has_rows=len(rows) > 0)
```

```yaml
  - id: transform
    run: ./transform.sh
    depends_on: [extract]
    unless_empty: extract.has_rows
```

`unless_empty` nomme une **clé**, pas une expression. Le step décide et publie la
réponse ; le moteur lit une clé et demande si elle est vide.

| compte comme vide | compte comme présent |
|---|---|
| `false`, `0`, `""`, `null`, `[]`, `{}` | tout le reste |

`"false"` comme **chaîne** est présent. Un step qui a publié ces cinq caractères
a publié quelque chose, et deviner qu'il voulait dire un booléen, c'est ainsi
qu'une règle se met à avoir des opinions que son auteur ne voit pas. Publiez un
vrai booléen.

### Pourquoi pas une expression

La conception tentante est `when: "{{ context.extract.rows > 0 }}"`. Un
mini-langage d'expressions est un engagement considérable : un parseur, un
système de types pour dire ce que `>` signifie sur un `any` JSON, un volet
sécurité parce que l'expression sort d'un YAML écrit par quelqu'un d'autre, et
des messages d'erreur qui pointent à l'intérieur d'une chaîne. Tous les
orchestrateurs qui en ont un ont un bug tracker plein de cela.

Ici, la décision vit dans le langage que son auteur écrit déjà, là où son propre
framework de test peut l'atteindre.

### Une clé manquante est un échec, pas une valeur vide

Si la clé n'est pas là, le step **échoue** et le message la nomme à côté de ce
que le publieur a réellement écrit :

```
step "transform" reads `extract.has_row`, and "extract" published "has_rows" but not "has_row"
```

L'alternative — lire une clé manquante comme « vide, donc on saute » — transforme
une faute de frappe en un step qui cesse de s'exécuter en silence et pour
toujours, sans que rien n'indique pourquoi. C'est le pire résultat que cette
fonctionnalité puisse avoir.

La clé est toujours qualifiée par le step qui la publie, et ce step doit être
l'un de ceux dont celui-ci dépend. Les deux sont refusés à la **publication** :
un step conditionné par une clé qu'il ne peut jamais voir serait sauté pour
toujours, et le découvrir quand un traitement nocturne cesse de tourner est trop
tard.

## Exécuter un step une fois par élément

```python
# in extract
context.set(partitions=["2026-01", "2026-02", "2026-03"])
```

```yaml
  - id: load
    run: ./load.sh "$BREVIS_MAP_VALUE"
    depends_on: [extract]
    for_each: extract.partitions
```

Le step s'exécute une fois par élément, chacun avec sa propre ligne, sa propre
nouvelle tentative et son propre code de sortie. Le graphe montre **un nœud avec
`[3]` dessus**, pas trois nœuds.

| variable | |
|---|---|
| `BREVIS_MAP_INDEX` | `0`, `1`, `2` … |
| `BREVIS_MAP_VALUE` | l'élément |

Une chaîne JSON arrive **sans ses guillemets** : `for_each` sur `["2026-01"]`
transmet à un shell `2026-01` et non `"2026-01"`. Tout le reste — un nombre, un
objet, une liste — arrive sous forme de son JSON.

Aucune des deux variables n'existe sur un step non mappé. Une variable toujours
présente et toujours vide apprend à celui qui lit l'environnement à l'ignorer.

### La forme du DAG ne change pas

Un step mappé reste un nœud avec un seul jeu d'arêtes. Seul le nombre de lignes
en dessous varie : la disposition est donc celle de toujours et le `[3]` est
compté à partir de ces lignes à la lecture — rien n'est stocké, donc rien à
garder synchronisé, et un compte erroné ne peut pas survivre à une requête
corrigée.

Tant que des instances sont en cours, la carte affiche `[2/3]`.

### Une instance en échec fait échouer le step

Trois partitions qui se chargent et une qui ne le fait pas, c'est un step qui n'a
pas fait son travail, et les steps en dessous le voient. Un nœud qui devient vert
parce que la plupart a fonctionné est un badge qui ment.

**Une nouvelle tentative ne refait que les instances en échec.** Dix-huit
partitions qui ont fonctionné ne sont pas rechargées parce que deux ont cassé.

### Une liste vide est `skipped`, pas un succès

Un step qui n'a rien fait parce qu'il n'y avait rien à faire n'a pas réussi à le
faire. Un nœud vert au-dessus de zéro instance est exactement le genre de chose
sur lequel quelqu'un construit un tableau de bord.

Une valeur qui **n'est pas une liste** échoue, tout comme une clé absente — la
même politique que `unless_empty:`, et pour la même raison : une faute de frappe
qui produirait silencieusement zéro instance désactiverait le step pour
toujours.

### Le fan-out est borné, et la borne est héritée

La liste voyage dans le contexte, et le contexte a un plafond de 4096 octets qui
vient du kubelet. Un workflow ne peut pas demander dix mille pods sans d'abord
trouver le moyen de le dire en quatre kilo-octets. C'est une limite qui vaut la
peine d'être gardée plutôt que contournée.

### Ce qu'un step mappé publie

Sa sortie est enregistrée sur la ligne de chaque instance et n'est **pas**
visible des steps en dessous. Quatre instances qui publient sous le nom d'un
même step, ce sont quatre valeurs pour une seule clé, et il n'y a pas de réponse
à `context.String("load.bucket")` qui ne soit pas une devinette. Le step le dit
dans son log plutôt que de l'ignorer discrètement.

## Regrouper des steps sur le graphe

```yaml
steps:
  - id: extract_orders
    group: sales_data_reporting
    run: ./extract.sh
  - id: load_orders
    group: sales_data_reporting
    depends_on: [extract_orders]
    run: ./load.sh
```

Les steps sont dessinés dans une boîte nommée qui se replie — le TaskGroup
d'Airflow, pour le cas où un DAG devient assez gros pour cesser d'être lisible.

**Purement visuel.** Les TaskGroups d'Airflow *préfixent* aussi les ids qu'ils
contiennent, de sorte que `extract` devient `sales.extract`. Ici non : préfixer
changerait chaque `depends_on`, chaque clé de contexte et chaque ligne
enregistrée d'un workflow existant, pour une fonctionnalité dont toute la valeur
est qu'un gros graphe se lit mieux. Les espaces de noms pourront être ajoutés
plus tard — c'est un ajout strict à ceci.

Un groupe est une **étiquette**, pas un conteneur. Les steps gardent leurs ids
globaux, un groupe peut s'étendre sur plusieurs niveaux, et rien ne change dans
l'exécution.

Cliquer sur le nom du groupe le replie : ses steps disparaissent et les flèches
qui traversaient la frontière pointent alors vers la boîte.

## Réutiliser un autre workflow

```yaml
# nightly.yaml
steps:
  - id: prepare
    run: ./prepare.sh

  - id: mlops
    uses: ml_training       # another workflow in the same publish
    depends_on: [prepare]

  - id: report
    run: ./report.sh
    depends_on: [mlops]
```

À la **publication**, les steps de `ml_training` prennent la place de ce step,
préfixés par son id, et le nœud `uses` disparaît :

```
prepare → mlops.train → mlops.evaluate → report
```

Ils arrivent sous forme d'un seul groupe repliable, de sorte que le graphe montre
ce que le fichier disait.

**Développé à la publication, pas exécuté à l'exécution.** Un step qui
déclenchait un *run* enfant et l'attendait est la conception qui a fait
provoquer des interblocages dans Airflow, et ce moteur a le même ingrédient : le plafond de pods est un
sémaphore par processus, donc un parent qui garde un slot en attendant un enfant
qui a besoin de slots dans le même pool se bloque — et seulement sous charge,
c'est-à-dire en production. Un run, un graphe, un pool.

Ce à quoi il renonce, c'est un run enfant avec son propre id et son propre
historique.

### Les règles

| | |
|---|---|
| l'enfant doit être dans la **même publication** | pas simplement déjà publié |
| flèches | vers le step : deviennent des flèches vers chacune des **racines** de l'enfant ; depuis le step : depuis chacune de ses **feuilles** |
| `image`, `env`, `secrets`, `resources` de l'enfant | matérialisés sur chaque step, qui s'exécute donc dans ce que son propre fichier disait |
| les clés `unless_empty` / `for_each` de l'enfant | suivent avec le préfixe |
| imbrication | aplatie, en profondeur d'abord |
| un cycle | refusé à la publication, avec la chaîne nommée |

**« Même publication », et non « déjà publié », est le point important.** Une
expansion qui lirait la base de données ferait répondre à `brevis validate` — qui
ne touche aucune base, à dessein — une autre question que `brevis publish`, et le
fichier qui a passé la CI serait celui qui échoue au déploiement.

L'enfant reste publiable seul : l'expansion **copie**, elle ne consomme pas. Un
step ne peut pas à la fois `uses:` et déclarer `run:`, `action:` ou `marker:` —
ce serait un fichier qui dit deux choses.

## Dire ce que signifie une flèche

Une dépendance peut porter une étiquette, affichée sur la flèche dans le graphe.

```yaml
steps:
  - id: determine_load_type
    run: ./decide.sh

  - id: load_full
    run: ./full.sh
    depends_on:
      - {step: determine_load_type, label: additional data}

  - id: load_delta
    run: ./delta.sh
    depends_on:
      - step: determine_load_type
        label: changed existing data
```

La forme nue — `depends_on: [extract]` — continue de fonctionner et reste la
forme normale. La plupart des dépendances n'ont rien à dire, et une étiquette sur
chaque flèche est du bruit.

Les étiquettes gagnent leur place sur une **branche** : deux flèches qui partent
du même step sans rien d'écrit dessus, c'est un schéma qu'il faut ouvrir dans la
source pour le lire, ce que justement un graphe existe pour éviter.

## Un step qui ne fait rien

```yaml
steps:
  - id: start
    marker: true

  - id: end
    marker: true
    depends_on: [load_full, load_delta, report]
```

`marker: true` est un step sans commande. Il n'exécute rien, réussit
instantanément, et apparaît sur le graphe.

Ce n'est pas de la décoration. Un `end` qui dépend de chaque branche transforme
*tout s'est-il terminé ?* en un seul nœud au lieu de six flèches à suivre — et il
se comporte comme n'importe quel autre step, donc un `end` sous une branche en
échec est **sauté**, pas vert.

**Un step sans `run:` et sans `marker: true` est toujours refusé.** Les deux cas
ne doivent pas se confondre : une commande vide est presque toujours une erreur,
et `marker: true` est la façon de dire qu'on l'a voulu. Un marker qui déclare
aussi `run:` est refusé lui aussi — c'est un fichier qui dit deux choses.

## Exécuter un step seulement quand quelque chose a échoué

Par défaut, un step s'exécute quand tout ce qui le précède a fonctionné. `when:`
change cela.

```yaml
steps:
  - id: extract
    run: python fetch.py

  - id: notify_failure
    run: ./notify.sh
    depends_on: [extract]
    when: any_failed          # runs precisely when extract did not make it

  - id: cleanup
    run: ./cleanup.sh
    depends_on: [extract, transform]
    when: all_done            # runs either way
```

| règle | |
|---|---|
| `all_success` | la valeur par défaut. Tout ce dont ce step dépend a réussi — ses **propres** dépendances, comme dans Airflow |
| `any_failed` | au moins un step dont celui-ci dépend a échoué |
| `all_done` | tout ce dont ce step dépend est terminé, quelle que soit la façon |

Une règle inconnue est refusée à la publication, en nommant ce qui est valide. Il
le faut : un `when: on_failure` lu comme « la valeur par défaut » s'exécuterait
en cas de **succès** — l'opposé de ce qu'il dit, découvert la nuit où cela
comptait.

### Un step qui ne s'exécute pas est `skipped`

C'est un état à part entière, dessiné dans sa propre couleur, et il porte la
raison : *`extract` était failed*. Avant cela, un step en dessous d'un échec
n'avait aucun enregistrement et l'écran l'affichait en attente pour toujours.

`skipped` n'est ni un échec ni un succès. Un step dont l'amont a cassé n'a pas
échoué — on ne lui a jamais donné la chance — et l'appeler succès est un
mensonge qui atteint le statut du run lui-même.

Une règle de déclenchement décide quels steps **s'exécutent**. Elle ne décide pas
du résultat du run : un `notify_failure` qui a délivré son message ne signifie
pas que le pipeline a fonctionné, et le run est toujours en échec.

### `all_success` est local, comme dans Airflow

La règle interroge les dépendances **propres** d'un step et rien d'autre. Un
échec dans une branche sans rapport n'arrête pas celle-ci :

```
extract_orders ──✕                 (failed)
extract_users  ──✓── transform_users ──✓     keeps going
```

Une branche **sous** l'échec s'arrête quand même — le fait que `all_success` soit
local ne signifie pas qu'il est absent.

Ce moteur interrompait autrefois tout le graphe au premier échec. Il y avait une
raison : continuer après une erreur produisait un résultat partiel qui avait l'air
complet, et un pipeline a tourné avec 28 jours de retard sans que personne ne le
remarque.

**Ce qui remplace cette protection**, et ce n'est pas rien :

- le run **échoue** toujours, avec la même erreur ;
- le graphe montre le step en échec en rouge et chaque step sauté dans sa propre
  couleur, chacun indiquant quel step l'a arrêté ;
- l'alerte part toujours quand le run abandonne.

Un résultat partiel n'a plus l'air complet, parce que le run dit qu'il ne l'est
pas.

**Ce à quoi l'on renonce**, dit clairement : une branche sans rapport écrit
désormais ses données lors d'un run qui a échoué ailleurs.

### Une nouvelle tentative ne relance que ce qui a échoué

La nouvelle tentative d'un run ne refait pas tout le graphe. Un step qui a déjà
réussi lors d'une tentative antérieure du **même run** garde son résultat et
n'est pas relancé — comme se comporte un run de DAG effacé dans Airflow.

```
attempt 1   extract ✓    load ✕    report (skipped)
attempt 2   extract –    load ✓    report ✓            extract is not re-run
```

Il garde sa ligne d'origine : sa durée, son log et ce qu'il a publié sont ceux de
la première tentative, car c'est là que le travail a eu lieu. **Le step en
dessous lit toujours ce qu'il a publié** — le contexte du run est stocké, pas
reconstruit.

La distinction qui compte : il s'agit des tentatives antérieures de **ce run**.
Un step qui a réussi lors du run d'*hier* s'exécute normalement aujourd'hui.
(Cette autre question existe aussi, et c'est elle qui dit au SDK s'il s'agit de
la première fois d'un step — voir `BREVIS_RUN_FIRST`.)

Un step qui a été **sauté** n'est pas réglé : il n'a jamais tourné, et la
nouvelle tentative en décide de nouveau avec les résultats de la nouvelle
tentative.

## Annoncer les échecs d'un step

Chaque échec est déjà annoncé : avec un webhook configuré, un run qui abandonne
envoie un message, sans aucun bloc répété dans un fichier. `on_error` est pour le
step qui a besoin du sien.

```yaml
steps:
  - id: fetch_observations
    run: python fetch.py
    on_error:
      type: SLACK
```

| champ | | |
|---|---|---|
| `type` | **requis** | `SLACK`. Une valeur inconnue est refusée à la publication, en nommant ce qui est valide |
| `when` | `give_up` | `attempt` annonce chaque tentative en échec, pas seulement la dernière |

Deux messages arrivent au lieu d'un, et c'est tout l'intérêt : l'alerte au niveau
du run dit *`id_verification` a échoué*, ce dont a besoin celui qui possède le
pipeline ; celle au niveau du step dit *`fetch_observations` a échoué*, ce dont a
besoin celui qui possède cette intégration.

**Il n'y a pas de champ `webhook` ni `url`, et il n'y en aura pas.** La
destination est un identifiant secret — celui qui la détient poste dans le canal
comme s'il était la plateforme — et un fichier de workflow est écrit par
quelqu'un qui n'a pas forcément le droit de choisir où vont les alertes de
l'entreprise. Le fichier dit **si** et **comment** ; l'installation dit **où**,
via `BREVIS_SLACK_WEBHOOK`.

**`when: give_up` est la valeur par défaut à cause de la nuit de qui est en
jeu.** Un step qui échoue deux fois et passe à la troisième enverrait deux
messages avec l'autre valeur par défaut, et le second arriverait après la
disparition du problème.

**Un step qui n'a pas échoué n'est jamais annoncé.** Dans un workflow où une
branche casse, celui qui possède l'autre branche n'est pas réveillé.

La livraison est le travail de `brevis alert` — l'alerte est enregistrée dans la
même transaction que l'échec, de sorte qu'une panne de Slack la retarde au lieu
de la perdre.

## Tags

```yaml
tags: [analytics, dbt, daily]
```

Ils servent de filtres dans l'interface. Ils n'affectent pas l'exécution.

## Validation

La validation n'a pas besoin de base de données, elle s'exécute donc en CI avec
les tests :

```bash
brevis validate workflows/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencias  cron 0 2 * * *
```

Elle accepte un fichier ou un répertoire. Dans un répertoire, elle sélectionne
`*.y*ml` et trie — deux exécutions produisent le même log, et la différence
entre deux déploiements ne devient pas du bruit.

## Publication

```bash
brevis publish workflows/
brevis publish workflows/ --project acme --prune
```

`--prune` retire du projet les workflows absents de la liste publiée, en
préservant leur historique. Ce n'est **pas** la valeur par défaut, pour une
raison pratique : `publish one-file.yaml` ne doit pas supprimer les quarante-huit
autres simplement parce qu'ils n'ont pas été nommés sur la ligne de commande.

## Étapes suivantes

- [Paramètres](/docs/parameters/) — ce qui change d'un run à l'autre
- [Scheduler et file](/docs/scheduler-and-queue/) — comment un workflow devient une exécution
- [Contexte entre steps](/docs/context/) — ce qu'un step dit au suivant
