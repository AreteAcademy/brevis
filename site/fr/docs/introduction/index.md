# Introduction

> Ce qu'est brevis.sh, le problème qu'il résout et les cas où ce n'est pas le bon choix.

*https://brevis.sh/fr/docs/introduction/ · brevis.sh docs (fr)*

---

brevis.sh est un **runtime d'orchestration de données écrit en Go**. Un seul
binaire réunit ce qui demande normalement quatre outils : la transformation
déclarative, l'orchestration de workflows, une file persistante, un
planificateur et une interface pour opérer le tout.

Il est open source sous licence MIT, et le projet est né à
[Aretê Academy](https://areteacademy.com.br/).

## Le problème

Une pile de données courante combine un outil de transformation, un
orchestrateur, une file et une interface. Chaque composant excelle dans son
rôle — et l'intégration entre eux devient un travail permanent : quatre schémas
de versionnement, quatre façons de configurer, quatre endroits où regarder
quand quelque chose échoue.

Ce coût n'apparaît jamais sur un schéma d'architecture. Il se voit dans le temps
qu'une équipe passe à entretenir la colle au lieu de travailler sur le domaine
qu'elle seule connaît.

## La proposition

Un seul artefact, avec la sous-commande que le rôle exige :

```bash
brevis serve        # API + interface
brevis scheduler    # materialises schedules and drains the queue
brevis run wf.yaml  # runs now, no database
```

Un workflow est un fichier YAML versionné. Relire un pipeline revient à relire
un diff, pas à suivre une séance de clics.

```yaml
name: ingest
schedule: "0 5 * * *"

steps:
  - id: bronze
    image: registry.example/dbt:1.10.3
    run: dbt build --select bronze+

  - id: notify
    image: ghcr.io/example/notify:0.3
    run: /notify --channel data
    depends_on: [bronze]
```

## Ce qui le distingue

**Chaque step s'exécute dans son propre pod, avec sa propre image.** Il n'y a
pas de worker générique qui attend du travail : le travail apporte son propre
runtime. Un step `dbt` démarre avec l'image dbt ; un fetcher Go à côté démarre
avec 5,8 Mo et 32Mi de mémoire, au lieu d'hériter de la taille de son plus
gros voisin.

**Le scheduler crée les runs ; la file les exécute.** Deux boucles
indépendantes, et cette séparation est délibérée : l'une peut tomber sans
interrompre l'autre, et retraiter un mois passé ne dépend pas de l'horloge.

**La validation n'a pas besoin de base de données.** `brevis validate` s'exécute
en CI avec les tests, de sorte qu'une erreur YAML fait échouer la pull request
et non le cluster.

## Trois composants, séparés à dessein

| composant | ce que c'est | version |
|---|---|---|
| **engine** | `brevis`, le binaire dont parle cette page : orchestre, planifie, exécute et opère | `0.15` |
| **SDK** | un module Go pour écrire le step qui extrait et charge | `0.66` |
| **gateway** | un endpoint HTTP qui fait atterrir les données qui *arrivent*, au lieu d'aller les chercher | `0.8` |

Les versions sont indépendantes parce que les maturités le sont. Numéroter le
gateway `0.15.3` parce que l'engine en est là reviendrait à revendiquer une
maturité qu'il n'a pas, et une version ne peut pas être dépubliée.

Ce qui relie les trois est un nombre : l'`ingestion_id`, un UUID v5 figé calculé
sur `provider|entity|source_key|record_ts`. Une ligne que le
[gateway](/fr/docs/ingestion/index.md) fait atterrir et une ligne qu'un fetcher du
[SDK](/fr/docs/sdk/index.md) fait atterrir pour le même enregistrement sont *une seule
ligne*, sans réconciliation entre elles.

## Quand brevis.sh n'est pas le bon choix

Documenter ce qu'un outil ne fait pas fait gagner plus de temps que documenter
ce qu'il fait :

- **Vous exploitez déjà Airflow ou Dagster et vous en êtes satisfait.** Migrer
  coûte plus cher que l'économie d'un binaire de moins à opérer.
- **Vos workflows ne concernent pas les données.** brevis.sh suppose des DAG de
  transformation et de chargement ; un orchestrateur généraliste vous servira
  mieux.
- **Vous avez besoin d'un catalogue d'opérateurs prêts à l'emploi.** Ici, un
  step est une image et une commande. C'est simple, et c'est limité — il n'y a
  pas des centaines d'intégrations à glisser-déposer.
- **Votre équipe n'utilise pas Kubernetes et n'en a pas l'intention.** Le mode
  local fonctionne, mais la conception suppose des pods.

## Code ouvert. Choisissez comment participer.

brevis.sh est 100 % open source. Pas de licences propriétaires, pas d'édition limitée, pas de verrous sur ce que vous construisez.

### 100 % ouvert

*code · licence open source*

Lisez, utilisez, adaptez et améliorez le runtime dans votre propre exploitation. Runtime complet · Code auditable · Contributions ouvertes.

[Voir le dépôt](https://github.com/AreteAcademy/brevis)

### Construire ensemble

*communauté · issues ouvertes* · **Voie principale**

Participez aux conversations qui décident de ce que devient le projet. Issues et propositions · Échanges entre pairs · Feuille de route publique.

[Suivre le projet](https://github.com/AreteAcademy/brevis/issues)

### Apprendre en profondeur

*aretê · la technologie avec un sens*

Découvrez l'école qui relie la pensée philosophique et l'ingénierie logicielle. Contenu original · Apprentissage continu · Canal ouvert.

[Visiter Aretê](https://areteacademy.com.br/)

## Où aller ensuite

| si vous voulez | allez à |
|---|---|
| installer et voir le produit fonctionner | [Installation](/fr/docs/installation/index.md) et [Démarrage rapide](/fr/docs/quickstart/index.md) |
| comprendre le format des workflows | [Workflows](/fr/docs/workflows/index.md) |
| comprendre l'architecture | [Scheduler et file](/fr/docs/scheduler-and-queue/index.md) |
| la liste des commandes et des options | [CLI](/fr/docs/cli/index.md) |
| écrire un fetcher en Go | [SDK](/fr/docs/sdk/index.md) |
| passer une valeur d'un step au suivant | [Contexte entre steps](/fr/docs/context/index.md) |
| écrire un step en Python | [Bibliothèques clientes](/fr/docs/libraries/index.md) |
| recevoir des données en HTTP au lieu de les récupérer | [Ingestion](/fr/docs/ingestion/index.md) |
| construire des tableaux de bord et des alertes | [Observabilité](/fr/docs/observability/index.md) |

:::note À propos du nom
*Brevis* est un mot latin qui signifie « court, bref » — la racine de *brièveté*.
Il vient du **De Brevitate Vitae** de Sénèque : ce n'est pas que nous avons peu
de temps, c'est que nous en perdons beaucoup.
:::
