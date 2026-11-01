# Observabilité

> Les métriques Prometheus des deux processus, ce qu'il faut surveiller, et ce qui est délibérément absent.

*https://brevis.sh/fr/docs/observability/ · brevis.sh docs (fr)*

---

Brevis expose des métriques Prometheus sur un port dédié, depuis **les deux**
processus.

```
GET http://<pod>:9090/metrics
```

Le port vient de `BREVIS_METRICS_ADDR`. Il est séparé du port HTTP à dessein : les
métriques ne passent pas par l'authentification de l'interface, et les servir
depuis un seul endroit forcerait à choisir entre un scrape authentifié et un
tableau de bord ouvert.

## Deux processus, deux endpoints

C'est la partie que la plupart des installations ratent, en ne scrutant que l'API.

| | sert | rapporte |
|---|---|---|
| `brevis serve` | l'UI et l'API HTTP | profondeur de la file |
| `brevis scheduler` | rien en HTTP | profondeur de la file, latence de réclamation, slots, orphelins, runs, steps |

Le scheduler est ce qui exécute, donc presque tout vient de lui. Un tableau de bord
construit uniquement sur l'API montre la file qui grossit et **rien** de ce qui la
vide.

## Les métriques qu'un step publie

Au-delà de ce que le moteur mesure, un step peut publier les siennes — combien de
lignes ont atterri, combien le fournisseur en a rejeté :

```python
from brevis import metrics
metrics.set("rows_loaded", 48213)
```

Elles apparaissent sur le **même** `/metrics` du scheduler, préfixées et étiquetées
par le moteur :

```
brevis_step_rows_loaded{workflow="daily_sales",step="load"} 48213
```

Le step n'ouvre aucun port : il écrit une ligne sur stdout et le moteur
l'enregistre — un pod qui vit quarante secondes n'est pas scrutable. Voir
[Python](/fr/docs/python/index.md).

## Ce qu'il faut surveiller

| signal | pourquoi |
|---|---|
| profondeur de la file qui monte sans redescendre | le scheduler est mort, ou `--concurrency` est trop bas pour la charge |
| latence de réclamation qui augmente | contention sur la base de données, ou file trop grande pour l'intervalle |
| orphelins > 0 | runs réclamés par un processus mort avant d'avoir terminé |
| slots non matérialisés | la boucle du scheduler s'est arrêtée, et les planifications avec elle |

Les deux premiers seuls ne permettent pas de distinguer « forte charge » de
« processus mort » — c'est pourquoi les slots comptent. Une file qui monte **avec**
des slots créés, c'est de la charge ; une file qui monte **sans** slots, c'est une
boucle arrêtée.

## Kubernetes

```yaml
env:
  - name: BREVIS_METRICS_ADDR
    value: ":9090"
ports:
  - name: metrics
    containerPort: 9090
```

Avec le Prometheus Operator, un `PodMonitor` par rôle — et il en faut bien un par
rôle, parce que les deux Deployments portent des étiquettes différentes et
rapportent des ensembles différents.

## Ce qui est délibérément absent

**Les traces.** Il n'y a pas encore d'OpenTelemetry. Un step s'exécute comme son
propre pod, et ce qui compte à son sujet — début, fin, tentative, code de sortie,
log — est déjà dans la base et à l'écran. La trace utile serait celle à l'intérieur
du step, et celle-là relève du code du step, pas de l'orchestrateur.

Cela tient tant que le step est l'unité. Le jour où un step appelle un autre
service et où la question devient « où sont passées les 40 secondes », la trace
commence à valoir ce qu'elle coûte.

## Étapes suivantes

- [Kubernetes](/fr/docs/kubernetes/index.md) — déployer les deux processus
- [Configuration](/fr/docs/configuration/index.md) — `BREVIS_METRICS_ADDR` et le reste
