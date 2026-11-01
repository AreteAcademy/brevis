# CLI

> Les douze sous-commandes du moteur, avec leurs options, leurs arguments et ce que chacune exige.

*https://brevis.sh/fr/docs/cli/ · brevis.sh docs (fr)*

---

Le moteur est un binaire unique avec des sous-commandes, pas plusieurs binaires :
l'API, le scheduler et les workers sont des rôles du même système, et un seul
binaire garde une seule version, une seule image et un seul chemin de build.

```
brevis [command] [flags]
```

| commande | base de données | identifiants hors `local` | rôle |
|---|---|---|---|
| [`serve`](#serve) | **oui** | **requis** | API + interface |
| [`scheduler`](#scheduler) | **oui** | **requis** | les deux boucles |
| [`migrate`](#migrate) | **oui** | **requis** | schéma |
| [`publish`](#publish) | **oui** | **requis** | stocke le workflow et sa planification |
| [`backfill`](#backfill) | **oui** | **requis** | retraite une plage |
| [`cancel`](#cancel) | **oui** | **requis** | arrête un run |
| [`run`](#run) | non | — | exécute maintenant, sur l'instance elle-même |
| [`validate`](#validate) | non | — | valide le YAML d'un workflow |
| [`brand`](#brand) | non | — | valide le YAML de personnalisation de marque |
| [`hash`](#hash) | non | — | génère le hash du mot de passe |
| [`version`](#version) | non | — | version, commit, build |

Les six premières chargent la configuration, qui **échoue au démarrage** sans
`BREVIS_DATABASE_URL`. Les cinq autres non — c'est pourquoi `validate`
fonctionne en CI, où il n'y a pas de Postgres.

Ces six mêmes commandes héritent de la règle des identifiants : avec
`BREVIS_ENV` différent de `local`, démarrer sans les trois variables
d'authentification est une **erreur au démarrage**.

---

## serve

Démarre l'API HTTP et l'interface. Aucune option — tout vient de
l'environnement.

```bash
brevis serve
```

S'arrête proprement sur `SIGINT`/`SIGTERM`, afin qu'un déploiement ne coupe pas
les requêtes en cours. Ce processus **ne matérialise pas les planifications** :
cela demande un `brevis scheduler` à côté.

`CMD` de l'image `api` (distroless, sans shell).

---

## scheduler

Les deux boucles : le scheduler matérialise les planifications en runs, le
dispatcher prend dans la file et exécute.

```bash
brevis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| option | type | défaut | |
|---|---|---|---|
| `--interval` | duration | `10s` | intervalle entre les cycles |
| `--concurrency` | int | `5` | **runs** simultanés |
| `--max-pods` | int | `5` | **steps** simultanés au total |

`CMD` de l'image `worker` (alpine avec un shell, car les steps `run:` en ont
besoin).

---

## alert

Livre les alertes que le scheduler a enregistrées. Un troisième processus à côté
de `serve` et `scheduler`.

```bash
brevis alert --interval 1s --max-attempts 6
```

| option | type | défaut | |
|---|---|---|---|
| `--interval` | duration | `1s` | intervalle entre les cycles de livraison |
| `--max-attempts` | int | `6` | essais avant qu'une alerte soit enregistrée comme non livrée |

**Il existe à cause de l'endroit où une alerte est écrite, pas parce que la
livraison mérite un processus.** Le scheduler enregistre l'alerte dans la même
transaction que l'échec qui la justifie — soit le run n'a plus de tentatives et
l'alerte existe, soit ni l'un ni l'autre n'a eu lieu. Il faut ensuite que
quelque chose vide cette table, et qu'il survive à la fois à une panne de Slack
et à son propre redémarrage. Avant cela, un webhook en échec signifiait une ligne
de log et une alerte tout simplement disparue.

Il refuse de démarrer sans canal configuré (`BREVIS_SLACK_WEBHOOK`). Un processus
de livraison sans destination vide l'outbox vers « non livré » aussi vite qu'elle
se remplit, et ces lignes ressemblent exactement à du Slack qui les rejette.

Une alerte qu'il abandonne est **conservée** : « levée, non livrée, 4 tentatives,
403 de Slack » est la ligne qui compte, car c'est le cas où quelqu'un attend un
message qui ne viendra pas.

`CMD` de l'image `worker`.

---

## report

Envoie le résumé périodique de ce qui s'est exécuté.

```bash
brevis report --window 168h              # sends it
brevis report --window 168h --dry-run    # prints it, sends nothing
```

| option | type | défaut | |
|---|---|---|---|
| `--window` | duration | `168h` | jusqu'où remonter dans le temps |
| `--dry-run` | bool | `false` | afficher au lieu d'envoyer |

Une **commande**, pas une boucle, et c'est la différence entre un rapport et une
alerte : elle s'exécute depuis un CronJob, il n'y a donc pas de nouvelle boucle,
pas de nouvel état et pas d'élection de leader — le cluster a déjà un scheduler.
`--dry-run` est la façon d'en venir à faire confiance à un message hebdomadaire,
en lisant ce qu'il aurait dit.

Il ne passe **pas** par l'outbox des alertes. Les deux partagent un canal de
livraison et rien d'autre : perdre une alerte est une panne dont personne
n'entend parler, et rater le résumé d'une semaine, c'est le résumé de la semaine
suivante.

Le message contient les runs, les succès, les échecs, les pipelines qui ont le
plus échoué, ceux qui ont pris le plus de temps, et les lignes et octets que le
SDK a rapportés. Il ne contient **pas** le CPU ni la mémoire, et n'affiche pas
non plus zéro pour eux — le moteur ne les collecte pas, et le message dit où ils
se trouvent.

Une fenêtre vide le dit plutôt que de rapporter 100 %. Un taux de succès calculé
sur rien est le chiffre le plus rassurant qu'un rapport puisse afficher et le
moins vrai : une semaine vide signifie généralement que le scheduler était
arrêté.

`deployments/kubernetes/report.yaml` l'exécute chaque semaine.

---

## migrate

```bash
brevis migrate up      # applies what is missing
brevis migrate down    # reverts the last one
brevis migrate status  # shows the state
```

Prend exactement un argument. Les migrations sont embarquées dans le binaire et
appliquées par leur propre sous-commande — **`serve` ne modifie jamais le
schéma**.

---

## validate

Valide un ou plusieurs fichiers de workflow. Accepte un fichier **ou un
répertoire**.

```bash
brevis validate examples/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

Elle ne touche à aucune base de données et ne démarre aucun serveur, elle
s'exécute donc dans l'éditeur et en CI. Elle quitte avec un code non nul et
compte les échecs.

---

## run

Exécute un workflow **maintenant, sur l'instance elle-même** : pas de file, pas
de base de données, pas de scheduler.

```bash
brevis run examples/hello.yaml
brevis run wf.yaml --param load_full=true --retries 3 --timeout 5m
```

| option | type | défaut | |
|---|---|---|---|
| `--param` | répétable | — | `key=value` pour un paramètre déclaré |
| `--workdir` | string | le répertoire du fichier | répertoire de travail des steps |
| `--retries` | int | `1` | tentatives par step (`1` = aucune nouvelle tentative) |
| `--timeout` | duration | `0` | délai maximal par step (`0` = sans limite) |

`--param` sans `=` est une **erreur**, pas un avertissement : `--param load_full`,
en oubliant la valeur, s'exécuterait avec la valeur par défaut — et l'opérateur
croirait que la valeur a été appliquée.

:::warning Quatre limites
`run` ne fonctionne qu'avec `BREVIS_ENV=local` ; un step avec `image:` s'exécute
sur l'instance elle-même et le dit ; il n'y a pas de registre de tâches Go, donc
une `action:` pour une tâche non enregistrée échoue en nommant celles qui sont
disponibles ; et `run` ne lit pas `BREVIS_HOSTS`, donc un step avec `host:` ne
peut pas du tout s'exécuter ici — il échoue en nommant l'hôte, même avec la
variable définie.
:::

---

## publish

Stocke les workflows et leurs planifications dans la base de données. Accepte un
fichier ou un répertoire.

```bash
brevis publish examples/hello.yaml
brevis publish workflows/ --project acme --prune
```

| option | type | défaut | |
|---|---|---|---|
| `--project` | string | `default` | slug du projet |
| `--prune` | bool | `false` | retire du projet les workflows absents de la liste |

Avec `--prune`, l'historique des workflows retirés est conservé.

---

## backfill

Matérialise les slots passés d'un workflow déjà publié. Il **met en file, il
n'exécute pas.**

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

| option | | |
|---|---|---|
| `--from` | **requis** | date de début, `YYYY-MM-DD` |
| `--to` | **requis** | date de fin, `YYYY-MM-DD` |
| `--param` | | s'applique à **chaque** slot de la plage |

`--to` inclut la journée entière : en interne, la fin devient `23:59:59` de cette
date.

---

## cancel

Annule un run qui attend encore ou qui est en cours d'exécution. Le flux
d'événements affiche `cli` comme auteur de l'annulation.

```bash
brevis cancel 3f1c9a52-6d0e-4b7a-9c41-2b8e5d7f0a13
```

| réponse | ce qui s'est passé |
|---|---|
| `canceled` | le run attendait dans la file ; il l'a quittée et ne s'exécutera pas |
| `cancel requested` | le run est en cours ; son dispatcher arrête les steps dans la limite de `--cancel-poll` (2s) |
| `already ended: <status>` | le run était déjà terminé ; rien ne change |

Un run annulé n'est pas relancé. Un id qui n'existe pas est une erreur.

---

## gateway

Comment un gateway apparaît dans `/data` : sa configuration, publiée comme acte
délibéré — jamais son trafic, et jamais à l'exécution.

```bash
gateway describe gateway.yaml > manifest.json   # the gateway's own binary
brevis gateway publish manifest.json
brevis gateway unpublish events_gateway
```

`publish` vérifie le manifeste en entier avant d'écrire quoi que ce soit, en
nommant le stream et le rôle du premier problème, et remplace les destinations de
ce gateway en une seule transaction. `unpublish` retire un gateway mis hors
service, et c'est une erreur si aucun gateway de ce nom n'a été publié.

Comment un `brevis-sql-api` apparaît dans Data → Endpoints : ce que chaque
endpoint prend et lit, publié comme acte délibéré — jamais son trafic.

```bash
brevis-sql-api describe --config api.yaml --out manifest.json   # the API's own binary
brevis endpoints publish manifest.json
brevis endpoints unpublish metrics-api
```

`publish` vérifie le manifeste en entier avant d'écrire quoi que ce soit et
remplace les endpoints de cette API en une seule transaction ; un nom, une valeur
par défaut ou une note qui ressemble à un identifiant secret refuse le tout.

Une commande à part plutôt qu'un mode de `brevis publish`, parce que les
workflows et les gateways ont des sémantiques d'ensemble différentes :
`publish --prune` ne doit jamais atteindre les lignes d'un gateway.

---

## prune

Applique la politique de rétention. C'est la **seule** chose dans Brevis qui
supprime quoi que ce soit, et elle ne s'exécute jamais toute seule — un opérateur
la planifie.

```bash
brevis prune --dry-run          # what would go, changing nothing
brevis prune
brevis prune --purge-after 0    # trim only, never delete a run
```

| option | défaut | |
|---|---|---|
| `--trim-after` | `720h` | vide le log, les phases et la sortie publiée |
| `--purge-after` | `8760h` | supprime entièrement le run ; `0` ne supprime jamais |

---

## brand

Valide un fichier de personnalisation de marque sans démarrer le serveur.

```bash
brevis brand brand.yaml
```

```
  ok    Brevis · Orchestration
        logo      /assets/logo.svg  (built-in symbol)
        accent    (the product's own)
        Powered by Brevis
```

Une différence de comportement par rapport au démarrage : **un fichier manquant
est une erreur**. Dans `serve`, l'absence signifie « utiliser l'identité par
défaut » ; celui qui a demandé à valider un chemin s'attend à ce qu'on lui dise
qu'il n'existe pas.

---

## hash

Génère la valeur de `BREVIS_AUTH_PASSWORD_HASH`.

```bash
brevis hash
```

Le mot de passe est lu depuis le terminal **sans écho**, jamais depuis un
argument : un argument apparaît dans le `ps` de n'importe quel processus et reste
dans l'historique du shell. Il refuse les mots de passe de moins de 12
caractères.

Le hash va seul sur **stdout** et les libellés sur **stderr**, de sorte que
`brevis hash > hash.txt` ne stocke que ce qui compte.

---

## version

```bash
brevis version
```

```
brevis 0.11.2
  commit  bb832ff
  build   2026-09-05T12:00:00Z
  go      go1.25.7 darwin/arm64
```

Compilé directement avec `go build`, il affiche `brevis dev` sans commit ni
date — et distinguer cela d'un artefact de release compte quand quelqu'un
signale un comportement étrange.
