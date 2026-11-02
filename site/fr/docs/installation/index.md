# Installation

> Comment obtenir le binaire, ce qu'il exige et comment confirmer que l'installation est correcte.

*https://brevis.sh/fr/docs/installation/ · brevis.sh docs (fr)*

---

brevis.sh est livré sous la forme d'un **binaire unique** et d'une **image Docker**. Aucun
runtime n'est à installer à côté : les migrations, les ressources de l'interface
et les polices sont toutes à l'intérieur du binaire.

## Prérequis

| | |
|---|---|
| **PostgreSQL** | requis pour `serve`, `scheduler`, `migrate`, `publish` et `backfill` |
| **Go 1.25+** | uniquement si vous compilez depuis les sources |
| **Kubernetes** | facultatif — sans cluster, les steps s'exécutent comme des processus locaux |

`brevis run`, `validate`, `marca`, `hash` et `version` n'ont **pas** besoin de base de données.

## Compiler depuis les sources

C'est le chemin qui produit un binaire avec la version et le commit intégrés :

```bash
git clone https://github.com/AreteAcademy/brevis.git
cd brevis
make build
./bin/brevis version
```

```
brevis 0.11.2
  commit  bb832ff
  build   2026-09-05T12:00:00Z
  go      go1.25.7 darwin/arm64
```

:::warning Le moteur n'a pas encore de release taguée
Tous les tags du dépôt appartiennent au SDK (`sdk/v*`) ; le module racine n'en a
aucun. Un `go install github.com/AreteAcademy/brevis/cmd/brevis@latest` se
résout vers le dernier commit de la branche et produit un binaire qui
s'identifie comme `brevis dev`, sans commit ni date — les `-ldflags` ne sont
transmis que par `make build` et `docker build`. Pour une version traçable,
utilisez l'image ou `make build`.
:::

## Docker

Deux images du même binaire, car les deux rôles ont des besoins opposés :

| tag | base | pourquoi |
|---|---|---|
| `:0.11.2` | distroless | l'API ne fait que servir du HTTP et n'exécute rien — pas de shell, surface minimale |
| `:0.11.2-worker` | alpine + tini | les steps `run:` ont besoin d'un shell |

```bash
docker run --rm brevissh/brevis:latest version
```

Les deux images se trouvent sur [hub.docker.com/r/brevissh/brevis](https://hub.docker.com/r/brevissh/brevis),
pour `amd64` et `arm64`.

## Un environnement local complet

Le dépôt fournit un `docker-compose.yml` qui démarre Postgres, l'API et
le scheduler :

```bash
make up      # Postgres + API + scheduler
make smoke   # checks /health and /ready
make logs
make down
```

L'interface est disponible sur `http://localhost:8080`.

## Configuration minimale

Une seule variable est requise :

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
```

Sans elle, les sous-commandes qui utilisent la base échouent au démarrage — c'est voulu. Un
processus qui démarre sans identifiants ne découvre le problème qu'à la
première requête, et d'ici là la readiness a déjà menti à l'orchestrateur.

```
$ brevis migrate status
erro: BREVIS_DATABASE_URL e obrigatoria
```

La liste complète se trouve dans [Configuration](/fr/docs/configuration/index.md).

## Appliquer le schéma

Les migrations sont embarquées dans le binaire et appliquées par leur propre sous-commande —
**`serve` ne modifie jamais le schéma** :

```bash
brevis migrate up
brevis migrate status
```

## Vérifier

```bash
brevis version
brevis validate examples/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencias  cron 0 2 * * *
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

Si les deux répondent, l'installation est correcte. Passez au
[Démarrage rapide](/fr/docs/quickstart/index.md).
