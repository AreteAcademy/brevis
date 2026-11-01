---
title: Configuration
description: Chaque variable d'environnement, avec sa valeur par défaut, son format et ce qui se passe quand elle manque.
group: Référence
order: 10
slug: configuration
---

Toute la configuration vient de l'**environnement**, lu une seule fois au
démarrage. Rien ne consulte l'environnement ensuite : un processus dont la
configuration change en cours d'exécution est un processus dont personne ne peut
reproduire le comportement.

## Requis

| variable | |
|---|---|
| `BREVIS_DATABASE_URL` | Postgres. Manquante = **erreur au démarrage** dans les cinq sous-commandes qui utilisent la base |

## Processus

| variable | défaut | |
|---|---|---|
| `BREVIS_ENV` | `local` | `local` journalise en texte et laisse l'interface ouverte ; toute autre valeur exige des identifiants et journalise en JSON |
| `BREVIS_HTTP_ADDR` | `:8080` | adresse d'écoute |
| `BREVIS_LOG_LEVEL` | `info` | |
| `BREVIS_SHUTDOWN_TIMEOUT_SECONDS` | `15` | entier ; une valeur non numérique est une erreur au démarrage |
| `BREVIS_BRAND_FILE` | `brand.yaml` | personnalisation de marque ; absent = valeur par défaut |
| `BREVIS_UI_URL` | — | base du lien du run dans les alertes |
| `BREVIS_METRICS_ADDR` | — | port des métriques Prometheus ; vide = non exposé. Voir [Observabilité](/docs/observability/) |
| `BREVIS_SLACK_WEBHOOK` | — | destination des alertes d'échec. Vide = personne n'est prévenu |

## Authentification

| variable | |
|---|---|
| `BREVIS_AUTH_USER` | utilisateur de l'interface |
| `BREVIS_AUTH_PASSWORD_HASH` | hash `pbkdf2-sha256$…`, généré par `brevis hash` |
| `BREVIS_AUTH_SECRET` | 32 octets ou plus pour signer la session (`openssl rand -base64 48`) |

Les trois vont ensemble, ou aucune.

:::danger Une configuration à moitié faite est une erreur au démarrage
Celui qui a renseigné le nom d'utilisateur croit que la porte est fermée.
L'interface déclenche des pipelines : un `POST /workflows/<slug>/trigger`
exécute un `dbt build` qui écrit dans l'entrepôt. Ouverte sur Internet, c'est une
télécommande pour l'entrepôt — et un avertissement dans le log ne défait pas
cette croyance, car personne ne lit le log d'un processus qui fonctionne.
:::

Hors de `BREVIS_ENV=local`, démarrer sans identifiants est refusé :

```
error: BREVIS_ENV=production requires a credential: set BREVIS_AUTH_USER,
BREVIS_AUTH_PASSWORD_HASH (generate one with `brevis hash`) and BREVIS_AUTH_SECRET
```

:::note Les anciens noms fonctionnent toujours
Quatre variables ont été renommées le 2026-09-10, lorsque les derniers noms
portugais ont quitté le côté opérateur du projet : `BREVIS_AUTH_USUARIO`,
`BREVIS_AUTH_SENHA_HASH`, `BREVIS_AUTH_SEGREDO` et
`BREVIS_POD_MANTER_EM_FALHA`. Une installation qui définit l'ancien nom continue
de fonctionner et journalise un avertissement nommant le nouveau.

Ajoutez le nouveau nom, déployez, puis retirez l'ancien — le nouveau nom l'emporte
quand les deux sont définis. Les anciens noms disparaîtront dans une version
majeure.

Dans le SDK, `BREVIS_SDK_LIMITE_INLINE` est devenue `BREVIS_SDK_INLINE_LIMIT`
selon la même règle.
:::

## Environnement des tâches

| variable | défaut | |
|---|---|---|
| `BREVIS_TASK_ENV` | vide | ce que chaque step reçoit |

Une tâche n'hérite **pas** de l'environnement de l'orchestrateur. La raison est
concrète : le processus porte `BREVIS_DATABASE_URL` avec le nom d'utilisateur et
le mot de passe Postgres, et un workflow est une commande arbitraire écrite par
quelqu'un d'autre — hériter donnerait les identifiants de la base à chaque step de
chaque pipeline.

Ce dont la tâche a besoin est déclaré :

```bash
BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE   # forwards these two from the process
BREVIS_TASK_ENV=STAGE=prod                # sets a literal
BREVIS_TASK_ENV='*'                       # forwards everything EXCEPT BREVIS_*
```

`PATH` et `HOME` sont toujours inclus — sans `PATH`, aucune commande ne se
résout, et l'erreur serait un « not found » qui n'explique rien.

Un nom manquant ne devient **pas** une chaîne vide : `GOOGLE_PROJECT_ID=""`
ferait échouer dbt avec un message pire qu'une variable manquante.

## Kubernetes

Des décisions d'**installation**, pas de workflow.

| variable | défaut | format |
|---|---|---|
| `BREVIS_PODS` | `auto` | `auto`, `on` ou `off` ; toute autre valeur est une erreur au démarrage |
| `BREVIS_POD_NAMESPACE` | celui du cluster | |
| `BREVIS_POD_SERVICE_ACCOUNT` | — | |
| `BREVIS_POD_PULL_SECRETS` | — | liste séparée par des virgules |
| `BREVIS_POD_ENV_FROM_SECRETS` | — | liste séparée par des virgules |
| `BREVIS_POD_ENV_FROM_CONFIGMAPS` | — | liste séparée par des virgules |
| `BREVIS_POD_ALLOWED_SECRETS` | — | liste séparée par des virgules |
| `BREVIS_POD_CREDENTIAL_PATH` | — | chemin monté dans le pod |
| `BREVIS_POD_CREDENTIAL_PVC` | — | PVC contenant les identifiants |
| `BREVIS_POD_NODE_SELECTOR` | — | `key=value,other=value` |
| `BREVIS_POD_TOLERATIONS` | — | `key=value:effect,…` ; uniquement l'opérateur `Equal` |
| `BREVIS_POD_KEEP_ON_FAILURE` | `false` | `true` conserve le pod pour inspection |

Dans les listes, les entrées vides sont ignorées : `a,,b` est une faute de
frappe, et un nom de secret vide ferait rejeter tout le pod par le serveur.

## Tests

| variable | |
|---|---|
| `BREVIS_TEST_DATABASE_URL` | base de données pour les tests d'intégration ; sans elle, ils sont sautés |

## Endpoints de santé

| | |
|---|---|
| `GET /health` | liveness — n'interroge **pas** la base de données |
| `GET /ready` | readiness — interroge, et nomme la dépendance qui a échoué |

La séparation est voulue : une sonde de liveness qui dépend d'un service externe
fait **tuer le pod** par Kubernetes quand la base vacille, au lieu de simplement
le retirer du load balancer.

## Un exemple complet

```bash
# required
export BREVIS_DATABASE_URL='postgres://brevis:secret@db:5432/brevis?sslmode=require'

# production requires credentials
export BREVIS_ENV=production
export BREVIS_AUTH_USER=operator
export BREVIS_AUTH_PASSWORD_HASH="$(brevis hash < password.txt)"
export BREVIS_AUTH_SECRET="$(openssl rand -base64 48)"

# what the tasks need, and nothing more
export BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE,DBT_KEYFILE

# pods
export BREVIS_PODS=on
export BREVIS_POD_NAMESPACE=data
export BREVIS_POD_SERVICE_ACCOUNT=brevis-runner
export BREVIS_POD_ENV_FROM_SECRETS=bigquery-cred

# alerts
export BREVIS_SLACK_WEBHOOK='https://hooks.slack.com/services/...'
export BREVIS_UI_URL='https://brevis.example.com'
```
