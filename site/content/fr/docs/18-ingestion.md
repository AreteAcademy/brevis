---
title: Ingestion
description: Un endpoint HTTP qui fait atterrir les données — valider, identifier, regrouper en lots, livrer. Sa propre image, sa propre version.
group: Ingestion
order: 18
slug: ingestion
---

Le gateway d'ingestion est un **endpoint HTTP qui fait atterrir les données**. Un
client envoie un `POST`, reçoit un `202`, et ce qui est arrivé devient un
enregistrement dans un topic, dans une table ou dans un bucket.

![Le flux du Gateway : un POST vers /v1/clicks est décodé, reçoit son ingestion_id, obtient immédiatement la réponse 202 et est ajouté à un lot ; un worker livre le lot au sink, ici des fichiers. Le Gateway n'a pas de base de données.](/assets/flow-gateway.svg)

C'est **un autre binaire, un autre module Go, une autre version et une autre
image**. Le moteur orchestre et ne touche jamais aux données des clients ; le
gateway ne fait rien d'autre. La numérotation est indépendante à dessein : le
moteur en est à 0.17 et le gateway à 0.27, ce qui est la vérité.

Avec le `gateway.yaml` de la section ci-dessous, et la clé qu'il demande dans `keys_from` :

```bash
docker run -p 8080:8080 -e BREVIS_INGEST_KEYS=dev-key \
  -v ./gateway.yaml:/etc/brevis/gateway.yaml:ro \
  areteacademy/brevis-gateway:0.32.0
```

```text
2026/10/09 01:03:01 drain budget 30s; set terminationGracePeriodSeconds >= 40
2026/10/09 01:03:01 connections idle out after 1m30s and are recycled after 1m0s
2026/10/09 01:03:01 listening on :8080
2026/10/09 01:03:01 metrics on :9090/metrics
```

Un `POST` revient immédiatement avec `202` :

```bash
curl -X POST localhost:8080/v1/clicks -H 'Authorization: Bearer dev-key' \
  -d '{"event_id":"e-1","occurred_at":"2026-10-08T12:00:00Z","page":"/pricing"}'
```

```text
{"accepted":1,"rejected":null}
```

Sans identifiants Google Cloud, le lot est réessayé et part dans la dead letter —
voir [Quand le sink refuse](#quand-le-sink-refuse).

L'image `-slim` ne contient que les sinks `auto_table`, `files` et `postgres` ;
le `pubsub` de cet exemple et les autres sont dans l'image complète. Une image
slim avec un sink qu'elle ne contient pas est refusée au démarrage, avec la
raison.

## Le fichier est le contrat

Il n'y a aucun code à écrire, hormis les hooks. Streams, chemins, formats,
identité, nouvelles tentatives, destination et dead letter — tout est dans le
YAML, et chaque champ est refusé quand il ne peut pas être honoré.

```yaml
name: events_gateway

listen:
  addr: :8080
  max_body: 1MiB
  idle_timeout: 90s                  # the default; it cannot be turned off
  max_conn_age: 60s                  # off unless set — see below
  auth:
    type: bearer
    keys_from: BREVIS_INGEST_KEYS   # the NAME of the variable, never the keys

metrics:
  addr: :9090                        # its own port, never the ingest one

streams:
  - name: clicks
    path: /v1/clicks
    format: json                     # json | array | ndjson — declared, never sniffed

    identity:
      provider: web
      entity: click
      source_key: event_id
      record_ts: occurred_at

    buffer:
      flush: {every: 1s, records: 500}

    sink:
      type: pubsub
      project: acme-prod
      topic: clicks

    dead_letter:
      type: files
      path: /var/dead-letter/
```

`format` est déclaré et jamais deviné : deviner, c'est ainsi qu'un lot de mille
devient une seule ligne contenant un tableau.

## L'`ingestion_id`, qui est tout l'intérêt

Chaque enregistrement part avec un `ingestion_id` : un **UUID v5 figé** calculé
sur `provider|entity|source_key|record_ts`.

C'est le **même id** qu'un fetcher batch calcule pour le même enregistrement. Une
ligne que le gateway fait atterrir et une ligne qu'un pipeline du [SDK](/docs/sdk/)
fait atterrir sont *une seule ligne*, sans réconciliation entre elles.

Deux conséquences pratiques :

- **Un `POST` réessayé est le même enregistrement.** Un client peut renvoyer sans
  crainte ; une destination avec `merge` absorbe la nouvelle tentative au lieu
  d'écrire une seconde ligne.
- **La formule est figée.** Les quatre champs sont requis, et en omettre un
  produit un id *différent* plutôt qu'un id plus faible. Le fichier refuse la
  configuration incomplète au lieu de l'accepter.

Les outils d'ingestion déplacent très bien des octets et n'ont aucune opinion sur
ce qu'*est* un enregistrement. Cette opinion, c'est ce qu'ajoute le gateway.

## La requête n'attend jamais le sink

Il décode, exécute le hook, calcule l'identité, ajoute à un buffer et répond.
**C'est tout.** Un lot plein part vers un pool de workers, et la publication, le
`COPY`, les nouvelles tentatives et la dead letter se passent tous là.

Ce n'est pas cosmétique. Avant que cela existe, la requête qui remplissait par
hasard un lot exécutait la livraison en ligne — un appelant sur chaque
`flush.records` payait donc l'aller-retour complet, et quand le sink était
indisponible, toute la fenêtre de nouvelles tentatives. Un p99 façonné par
l'appelant malchanceux n'est pas un p99 sur lequel on peut agir.

```yaml
buffer:
  flush:
    every: 1s         # age
    records: 500      # count
    size: 8MiB        # bytes — whichever is crossed FIRST sends the batch
    max_age: 30s      # and this one sends it REGARDLESS
  workers: 4          # batches delivered at once
  queue: 64           # full batches that may wait for a worker
  max_records: 10000  # events held in memory before the gateway says no
  max_bytes: 256MiB   # the same ceiling in bytes
```

### L'horloge de flush appartient au déploiement

`buffer.flush.every` est un `time.AfterFunc` dans **chaque réplica**. Les load
jobs qu'une table reçoit sont donc fonction du nombre de réplicas, pas du trafic :

```
jobs / day / table  =  replicas × 86400 / every
```

Sur BigQuery, face à 1 500 par table et par jour, **deux réplicas à 60s font déjà
192 % d'un quota qu'on ne peut pas relever** — avant que le moindre HPA agisse. Et
le sens est opposé à l'intuition : le gateway passe à l'échelle pour la
concurrence HTTP tandis que le sink veut de la concentration.

```yaml
buffer:
  flush:
    every: 60s
    max_age: 300s
    claim: true
```

Avec `claim`, un réplica fait un set-if-absent pour le stream et la fenêtre avant
de flusher sur le timer — et le timer se réveille sur la **frontière de la
fenêtre**, de sorte que tous les réplicas demandent la même clé. Un flush par
fenêtre, quel que soit le nombre de réplicas. Et cela s'ajuste tout seul : un
réplica n'attend que lorsqu'un autre est réellement en concurrence, donc réduire le
nombre de réplicas rétablit la latence plus faible sans que personne n'édite une
configuration.

Mesuré avec deux réplicas et un Redis, fenêtres de 5s : **8 load jobs sont devenus
6, un par fenêtre, en alternance 3/3** — aucune fenêtre avec deux.

**Il faut un backend partagé.** Avec `memory`, chaque réplica gagne sa propre
réclamation et rien ne coordonne. Et il **exige `max_age`**, que la configuration
refuse de se passer : la réclamation transforme `every` en cible, et une cible sans
plafond est une latence que rien ne borne.

**Un metastore injoignable flushe quand même.** Perdre la coordination coûte des
load jobs ; refuser de flusher retient des données. Vérifié en tuant Redis en cours
d'exécution : 40 événements sur 40 ont atterri, rien n'a été retenu.

**`outcome="unreachable"` est celui qui mérite une alerte.** Le backend n'a pas pu
être interrogé, donc la réclamation a échoué en mode ouvert : la coordination est
désactivée et les load jobs se multiplient de nouveau par le nombre de réplicas —
en silence, car chaque flush ressemble toujours à un flush.

Et la réclamation donne l'**exclusivité, pas l'équité** : celui qui demande en
premier gagne. Un vrai run à deux réplicas a mesuré 4/1, pas un partage égal.
`max_age` est ce qui empêche le malchanceux de mourir de faim.

Avec `claim: true` et `metastore: memory`, le gateway avertit **une seule fois au
démarrage** : chaque réplica gagne sa propre réclamation, donc tout semble correct
et rien ne coordonne.

### `every` est une cible ; `max_age` est une promesse

Les trois déclencheurs ci-dessus décident quand un lot **essaie** de partir.
`max_age` décide quand il part **quoi qu'il arrive**.

Aujourd'hui la différence est inerte, car rien ne diffère la cible. Elle commence à
compter dès que l'horloge de flush cesse d'être par processus :
`buffer.flush.every` est un `time.AfterFunc` dans chaque réplica, donc le nombre de
load jobs qu'une table reçoit est fonction du **nombre de réplicas**, pas du trafic
— `replicas × 86400 / every`. Sur BigQuery, face à 1 500 par table et par jour,
**deux réplicas à 60s font déjà 192 % du quota**.

Dès qu'un réplica peut céder une fenêtre à un autre, il continue de se remplir, et
`max_age` est ce qui empêche quiconque de retenir des données indéfiniment en
attendant son tour.

Il règle aussi le drain : ce qu'un buffer peut **contenir** au `SIGTERM` se
retrouve borné par lui, et le `terminationGracePeriodSeconds` que le gateway
affiche au démarrage en est dérivé.

Un plafond **inférieur** à la cible est refusé au chargement — cela voudrait dire
que la cible ne s'applique jamais.

### Pourquoi il y a un déclencheur de taille

Les deux autres sont des **comptages**, et un comptage ne distingue pas 500
événements de 2 Ko de 500 de 2 Mo — 1 Mo contre 1 Go, le même nombre dans le
fichier. `max_records: 10000` a le même angle mort : 20 Mo ou 20 Go, et rien dans
le YAML ne pourrait dire lequel.

Un producteur qui se met à envoyer le document entier au lieu de son id fait
tomber le pod, et tant que cela n'existait pas, **aucun champ n'aurait pu exprimer
la limite**.

La taille est mesurée sur ce qui est **arrivé** — pas sur ce que l'événement pèse
en mémoire (une `map[string]any` fait plusieurs fois son JSON) ni après un hook qui
le gonfle. Une approximation, et la bonne : elle est gratuite au décodage, elle suit
le payload, et ce contre quoi elle protège est un enregistrement qui a été
multiplié par dix.

**Sur BigQuery, `size` est un plafond, pas une cible.** Cette destination autorise
1 500 load jobs par table et par jour, ce qui explique que `every` ait un plancher
de 60 secondes. Mais le plancher régit le **timer** : une taille qui se déclenche
toutes les quelques secondes le franchit sans effort et dépense le même quota. Rien
ne refuse cela au chargement, car le débit d'arrivée n'y est pas connaissable. Ce
qui existe à la place, c'est de la visibilité :

```
brevis_gateway_flushes_total{stream,trigger}   trigger = time | records | size
```

`trigger="size"` qui grimpe sur un stream BigQuery vous dit que ce n'est pas la
fenêtre qui fait le regroupement.

Ce compteur est par stream, et le quota est par **table** : avec `auto_table`, une
route devient plusieurs tables, et 94 d'entre elles qui rapportent un seul nombre
ne disent rien d'aucune. Derrière `BREVIS_INGESTION_METRICS`, il en existe un qui
la nomme :

```
brevis_gateway_table_flushes_total{stream,table,trigger}
```

Un flush d'une table est un load job pour elle ; c'est donc la série sur laquelle
alerter à 1 200 des 1 500 que BigQuery autorise. La moitié inconditionnelle est une
ligne au démarrage, qui dit ce que coûte `flush.every` par table et par jour avant
qu'un événement arrive.

**C'est borné, pas fire-and-forget.** Une goroutine par lot transformerait une
panne du sink en mémoire non bornée. Quand la file et le buffer sont tous deux
pleins, la réponse est **`503` avec `Retry-After`** plutôt qu'un `202` pour un
événement qui n'a nulle part où aller. Un événement accepté qui n'est jamais livré
est le seul résultat que ce service existe pour ne pas avoir.

Et ce `503` est **sans risque à réessayer**, d'une façon dont presque aucun service
ne peut se prévaloir : l'`ingestion_id` est une fonction figée de l'événement
lui-même, donc le même corps renvoyé est le *même enregistrement*.

## Une connexion décide quel réplica reçoit la charge

Derrière un Service Kubernetes `ClusterIP`, kube-proxy choisit un backend **une
fois par connexion TCP**, pas par requête. La plupart des clients gardent les
connexions ouvertes et les réutilisent, donc un producteur qui en a une poignée
envoie tout au même ou aux deux mêmes réplicas quel que soit leur nombre — et un
réplica que le HPA ajoute ne reçoit rien, parce que personne ne s'y connecte.

```yaml
listen:
  idle_timeout: 90s     # closes a connection nobody came back to
  max_conn_age: 60s     # after this, the next response asks the client to redial
```

**`max_conn_age` est désactivé par défaut**, et c'est délibéré : la durée pendant
laquelle un client peut garder une connexion est une politique, et elle échange un
handshake dans le cluster par connexion et par âge contre une charge qui se répartit.
La requête elle-même est toujours servie — l'en-tête est ajouté à la réponse, rien
n'est réessayé et aucun lot n'est perdu.

**`idle_timeout` a une valeur par défaut et ne peut pas être désactivé.** Servir
sans en avoir, c'est un descripteur de fichier par client abandonné jusqu'au
redémarrage du processus. Définissez-en une longue si c'est ce que vous voulez ;
c'est alors un nombre que quelqu'un a choisi.

Les deux sont indépendants : une connexion rencontre la borne qu'elle atteint en
premier, et une connexion n'atteint l'âge qu'en étant utilisée — exactement le
producteur de longue durée que l'âge existe pour rééquilibrer.

## Quand le sink refuse

Le lot est réessayé — quatre tentatives sur environ sept secondes par défaut, avec
doublement et jitter — puis il part dans la **dead letter**, avec la raison sur
chaque enregistrement :

```json
{
  "event_id": "dl-9",
  "ingestion_id": "84aaee4b-66af-5cc0-b97b-6856dec95b25",
  "_dead_letter_reason": "rpc error: code = NotFound desc = Topic not found",
  "_dead_letter_sink": "pubsub:acme-prod/clicks",
  "_dead_letter_at": "2026-09-25T02:02:32Z"
}
```

La raison voyage **sur l'enregistrement** et pas seulement dans un log, parce que
celui qui trouve ce fichier plus tard a les événements et pas le log — et
*pourquoi est-ce ici* est sa première question.

**Un stream sans `dead_letter` est refusé au chargement.** En faire un silence par
défaut placerait la décision là où personne ne la prend, et un lot refusé qui n'est
qu'une ligne de log, c'est perdre des données discrètement.

## Le hook est du Go, compilé dedans

```go
hooks := gateway.NewHooks()
hooks.MustRegister("enrich_clicks", enrichClicks)
gateway.Main(hooks, gateway.WithSinks(sinks))

func enrichClicks(e map[string]any) (map[string]any, error) {
    host, _ := e["host"].(string)
    e["tenant"] = strings.Split(host, ".")[0]
    return e, nil     // returning nil DROPS the event, on purpose
}
```

Le YAML nomme un hook ; il n'en porte pas. Mesuré avant de choisir : une fonction
Go coûte **93 ns/op** contre 959 pour Starlark et 1 281 pour yaegi, sans rien
ajouter au binaire — et `plugin.Open` n'est pas une option du tout, car sous
`CGO_ENABLED=0`, qui est la façon dont chaque artefact ici est compilé, il renvoie
`plugin: not implemented`.

Ce que cela coûte, dit là où quelqu'un le lira : **ajouter un hook est une
recompilation et un déploiement, pas un changement de configuration.** C'est le
bon compromis tant que les hooks sont écrits par ceux qui livrent le binaire, et le
mauvais le jour où un client doit en changer un sans release.

Une erreur dans un hook envoie *cet* événement sur le chemin de la dead letter et
ne fait jamais échouer le lot à côté. Un panic aussi : il est récupéré par
événement, parce qu'un enregistrement malformé ne doit pas faire tomber un
processus qui sert tous les autres streams.

## Quand un événement est trop gros

```yaml
oversize:
  larger_than: 256KiB
  archive: {type: files, path: gs://acme-oversize/clicks/}
  hook: strip_heavy_clicks
```

L'événement est écrit **en entier** dans `archive`, et une version réduite continue
dans le stream en portant un pointeur de retour :

```json
{
  "event_id": "big-1",
  "ingestion_id": "bd8ac083-9796-50f5-bab6-77a4e2e57ad0",
  "_oversize": true,
  "_oversize_archive": "files:gs://acme-oversize/clicks/",
  "_oversize_bytes": 2149
}
```

C'est le pattern **Claim Check**, et il vaut mieux qu'un `413` sec car un `413`
perd l'événement — et un payload surdimensionné est généralement le plus
intéressant que quelqu'un ait : c'est la requête avec le document entier en pièce
jointe, qui est le cas qui vaut la peine d'être débogué.

`larger_than` mesure un **événement**, après le hook. `listen.max_body` est autre
chose : il plafonne une **requête** entière et refuse avec `413` avant qu'un seul
octet soit parsé.

## Ce qu'il compte

Onze séries Prometheus, sur **son propre port** — jamais celui d'ingestion, et cette
règle compte davantage ici que dans le moteur : le port d'ingestion est public par
construction, c'est là que les clients font leur `POST`, donc un `/metrics` dessus
publierait chaque nom de stream, chemin et destination à quiconque trouverait le
chemin. Nommer la même adresse pour les deux est refusé au chargement.

```
brevis_gateway_events_received_total{stream,format}     counter
brevis_gateway_events_rejected_total{stream,reason}     counter  per event, after decode
brevis_gateway_requests_refused_total{stream,reason}    counter  per request: unauthorized|body_too_large|malformed|empty|saturated
brevis_gateway_batches_total{stream,sink,outcome}       counter  delivered|retried|buried
brevis_gateway_reconciled_total{stream,sink}            counter  landed after the send window; also delivered
brevis_gateway_flushes_total{stream,trigger}            counter  time|records|size
brevis_gateway_flush_windows_total{stream,outcome}      counter  won|yielded|ceiling|unreachable
brevis_gateway_ingested_bytes_total{stream,table}        counter  opt-in
brevis_gateway_ingested_events_total{stream,table}      counter  opt-in
brevis_gateway_table_flushes_total{stream,table,trigger} counter  opt-in
process_start_time_seconds                              gauge    unprefixed, on purpose
brevis_gateway_saturated_total{stream}                  counter  the 503s
brevis_gateway_delivery_seconds{stream,sink}            histogram
brevis_gateway_buffer_records{stream}                   gauge
brevis_gateway_queue_batches{stream}                    gauge
```

`process_start_time_seconds` n'est **pas** préfixé par `brevis_`, et c'est
délibéré : les collecteurs cherchent exactement ce nom. Il indique quand le
processus a démarré, et sans lui un collecteur qui ajuste l'heure de départ — le
récepteur Prometheus d'OpenTelemetry, sur lequel Google Managed Prometheus est
construit — ancre chaque série cumulative à son **premier scrape** et dépense la
valeur de ce scrape comme base. Résultat : chaque compteur lit trop bas, avec des
deltas justes et des totaux faux.

Les trois derniers sont ceux que personne ne réclame avant le premier incident.
`saturated_total` est le seul nombre qui dit qu'on a demandé à un client de
reculer ; `buffer_records` rapporté à `buffer.max_records` est le seul qui le
**prédit**.

`reason` est un ensemble fermé et jamais le texte de l'erreur : une chaîne d'erreur
contient un nom de table, une colonne, parfois une ligne, et un seul client
malformé créerait une nouvelle série par requête. Le texte reste dans la dead
letter, sur l'enregistrement.

## `202`, pas `200`

Le gateway a **accepté** les événements. Avec `durability: memory` — le seul niveau
implémenté — c'est tout ce qu'il peut honnêtement affirmer. Le niveau qui mérite un
`200` est celui qui les a déjà écrits durablement, et il n'existe pas.

`disk` est refusé **par son nom**, et non accepté et traité comme de la mémoire :
celui qui écrit `disk` croit que ses événements survivent à un crash, et approuver
sans livrer est exactement l'échec que ce champ existe pour empêcher.

Ce qui se trouve dans un buffer à l'arrêt est livré, pas abandonné — par le même
chemin que prend un lot plein, réessayé et enterré.

## Ce qu'il ne fait pas

- **Il n'apparaît pas dans la console.** Il ne partage aucune base avec l'interface
  du moteur, donc il n'y a pas de page `/ingestion` — et l'évolution de schéma
  qu'effectue `auto_table` n'y apparaît pas non plus. C'est la prochaine étape et
  elle n'a pas été écrite.
- **Un stream avec une `table` ne fait pas évoluer le schéma.** Quand vous nommez la
  table, le contrat est celui de la table : un champ qu'elle n'a pas est refusé avec
  le message qui le corrige, plutôt que de modifier une table que quelqu'un a
  relue. Faire grandir une colonne tout seul, c'est ce que fait `auto_table`, et
  c'est la différence entre les deux.
- **Un stream avec une `table` ne crée ni table ni index.** Un service qui crée des
  tables transforme une faute de frappe en une seconde table que personne ne lit —
  c'est pourquoi la création n'a lieu que sous `auto_table`, où `naming` borne les
  noms qui peuvent exister et où `listen.auth` est obligatoire.
- **L'image publiée n'a aucun hook**, ce qui est l'artefact honnête pour une
  conception à hooks compilés : elle sert les streams qui ne déclarent pas de
  `hook:`.

## Une route, N tables

`auto_table` route chaque événement vers la table que l'**enveloppe** nomme, crée
cette table si elle est absente, et ajoute une colonne quand un nouveau champ
apparaît :

```json
{"table_name": "app_orders", "operation": "INSERT", "unique_key": "id",
 "data": {"id": "A-1", "total": 150, "customer": {"id": 7, "uf": "SP"}}}
```

Les champs de contrôle en haut, l'enregistrement dans `data`, et sept colonnes de
suivi `brevis_*` sur chaque table. Une route, N tables, rien de déclaré.

C'est dans [Sinks d'ingestion](/docs/ingestion-sinks/#une-route-n-tables-rien-de-declare).

## Où aller ensuite

| si vous voulez | allez à |
|---|---|
| les destinations et la façon dont chacune écrit | [Sinks d'ingestion](/docs/ingestion-sinks/) |
| l'`ingestion_id` vu de l'autre côté | [SDK Go](/docs/sdk/) |
| des tableaux de bord et des alertes | [Observabilité](/docs/observability/) |
