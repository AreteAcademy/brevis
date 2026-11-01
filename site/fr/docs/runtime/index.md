# Runtime d'un step

> Ce qu'un step déclare sur le langage et les outils qu'il pilote — et pourquoi il est rarement nécessaire de le faire.

*https://brevis.sh/fr/docs/runtime/ · brevis.sh docs (fr)*

---

Le graphe dessine sur chaque step une puce indiquant son langage et les outils
qu'il pilote :

```
┌──────────────────────────────────────┐
│ ● transform            SDK v0.57.0   │
│   dbt build --select gold            │
│   ⬤ Python  ▢ dbt                    │
│   2m14s · attempt 1                  │
└──────────────────────────────────────┘
```

**La plupart du temps, vous n'écrivez rien.** Le moteur lit `run:` et `image:` et
en déduit le reste.

## Quand le déclarer

Deux champs, tous deux facultatifs, tous deux par step :

```yaml
steps:
  - id: fetch
    run: /opt/brevis/bin/fetch-weather
    runtime: go            # a compiled binary; the path says nothing

  - id: transform
    run: dbt build --select gold+
    tools: [dbt]           # what it drives, not what it is
```

| champ | |
|---|---|
| `runtime` | le langage du step : `go`, `python`, `node`, `shell`… |
| `tools` | les outils que la commande pilote : `dbt`, `dlt`, `spark`… |

La règle empirique : **déclarez-le quand l'inférence se trompe.** Un `run:` qui
pointe vers un binaire compilé n'a aucun moyen de dire qu'il est en Go ; un
wrapper qui appelle `dbt` en interne n'a aucun moyen de dire qu'il pilote dbt.

## Pourquoi un vocabulaire fermé

Un id hors du vocabulaire est **refusé à la publication**, en nommant ce qui est
valide. L'alternative est une puce qui s'affiche vide sur un écran trois jours
plus tard, sans rien pour remonter à la cause.

```
workflow "daily": step "transform": `runtime: pyton` is not valid
  (valid: go, python, node, shell, …)
```

## À quoi sert la puce

Ce n'est pas de la décoration. Quand un run échoue à 3 h du matin, la première
question est « qu'exécute ce step ? », et la réponse se trouve généralement dans
un autre dépôt. La puce la met sur le graphe lui-même — à côté de la version du
SDK utilisée par le step, ce qui permet de distinguer « cassé aujourd'hui » de
« cassé depuis la montée de version de mardi ».

## Étapes suivantes

- [Workflows](/fr/docs/workflows/index.md) — tous les champs d'un step
- [Un pod par step](/fr/docs/pod-per-step/index.md) — où un step s'exécute réellement
- [Observabilité](/fr/docs/observability/index.md) — les métriques qui vont avec
