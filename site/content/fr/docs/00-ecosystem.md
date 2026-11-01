---
title: Écosystème
description: Les quatre composants de brevis.sh, le rôle de chacun et le chemin que prennent les données.
group: Prise en main
order: 0
slug: ecosystem
---

Le Core planifie, met en file et surveille — sans jamais toucher aux données. Ce qui les touche, c'est le SDK, qui va les chercher ; le Gateway, qui les reçoit ; et SQL, qui les modélise là où elles ont déjà atterri.

## Les quatre composants

| composant | ce qu'il fait | comment l'obtenir | guide |
|---|---|---|---|
| **Core** | Planifie, met en file et démarre un pod par step. Ne touche jamais aux données. | `areteacademy/brevis` | [Core](/docs/core/) |
| **SDK** | Une bibliothèque Go qui extrait, met en forme et charge, à l'intérieur de votre step. | `go get github.com/AreteAcademy/brevis/sdk` | [SDK Go](/docs/sdk/) |
| **Gateway** | Reçoit un HTTP POST, répond 202 et écrit par lots. Sans base de données propre. | `areteacademy/brevis-gateway` | [Ingestion](/docs/ingestion/) |
| **SQL** | De simples fichiers .sql deviennent des tables et des vues, dans l'ordre des dépendances. | `areteacademy/brevis-sql` | [SQL](/docs/sql/) |

## Plan de contrôle, plan de données

Le **plan de contrôle** est le Core : il décide quand chaque step s'exécute,
conserve la file et l'état dans Postgres et démarre un pod par step. Les
données ne passent jamais par lui.

Le **plan de données** regroupe les trois autres composants. Les données
viennent d'une API, d'une base de données ou de fichiers et atteignent leur
destination de deux façons : le **SDK les récupère**, dans un step à vous, ou
elles sont **poussées vers le Gateway** en HTTP, depuis des webhooks et des
applications. La destination est une table, un topic ou des fichiers dans un
bucket. Une fois les données arrivées, **SQL** modélise les tables sur place,
dans la base où elles ont atterri.

### Autour

Trois composants complémentaires :

- **[Console](/docs/console/)** — l'interface web du Core
- **[agent](/docs/pod-per-step/#where-each-step-runs)** — exécute un step dans un cluster que vous avez déjà
- **[`brevis` pour Python](/docs/python/)** — contexte d'exécution pour les steps Python

## N'utiliser qu'un seul composant

Oui. Chaque composant a son propre module, sa version et son image : le SDK est une bibliothèque Go qui s'exécute dans n'importe quel programme, le Gateway démarre seul avec un fichier YAML et sans base de données, et brevis-sql s'exécute comme un conteneur. Le Core orchestre les autres quand vous le souhaitez — il n'est un prérequis pour aucun d'eux.

## La même ligne

Une commande que le SDK récupère et une commande qui arrive au Gateway atterrissent dans la même table, avec le même brevis_ingestion_id. Choisir entre les deux devient une décision de déploiement, pas de schéma.

Via le SDK, avec `sdk.Landing` (détails dans [La disposition d'atterrissage](/docs/sdk/#the-landing-layout)) :

```go
sdk.Execute(ctx, &sdk.Pipeline{
	Source:    sdk.Source{From: from.Files{Path: "order.json"}},
	Transform: []sdk.Transformer{sdk.Landing("app_orders", sdk.LandingKey("id"))},
	Target: sdk.Target{
		To:     postgres.Table{DSN: dsn, Name: "app_orders"},
		Schema: sdk.LandingSchema(sdk.LandingOptions{}),
	},
}, nil)
```

Via le Gateway, un POST vers `/v1/tables` (détails dans [Une route, N tables](/docs/ingestion/#one-route-n-tables)) :

```bash
curl -X POST localhost:8080/v1/tables -H 'Authorization: Bearer dev-key' \
    -d '{"table_name":"app_orders","unique_key":"id",
         "data":{"id":"A-1","customer":"acme.example.com","total":150}}'
```

La réponse :

```text
{"accepted":1,"rejected":null}  202
```

Ce qui a atterri dans `app_orders` :

| colonne | via le SDK | via le Gateway |
|---|---|---|
| `brevis_ingestion_id` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` |
| `brevis_record_key` | `A-1` | `A-1` |
| `brevis_operation` | `INSERT` | `INSERT` |
| `brevis_gateway` | `NULL` | `acme_gateway` |
| `data` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` |

Sortie réelle : les deux chemins ont été exécutés sur le même Postgres local, avec les images et le SDK publiés. La seule différence est l'auteur de l'écriture — brevis_gateway est vide sur la ligne du SDK.
