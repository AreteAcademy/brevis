# Kubernetes

> Comment déployer le moteur, avec quelles permissions, et ce qu'il faut vérifier ensuite.

*https://brevis.sh/fr/docs/kubernetes/ · brevis.sh docs (fr)*

---

Une installation type comprend **deux Deployments** du même binaire — l'API et le
scheduler — plus un Postgres.

## L'installer avec Helm

Le chart se trouve dans le dépôt, à `deployments/helm/brevis`. Quatre valeurs sont
requises et tout le reste a une valeur par défaut fonctionnelle :

```bash
helm install brevis ./deployments/helm/brevis \
  --namespace data --create-namespace \
  --set database.url="postgres://brevis:pw@postgres/brevis?sslmode=require" \
  --set auth.user=admin \
  --set auth.passwordHash="$(brevis hash)" \
  --set auth.secret="$(openssl rand -base64 48)"
```

C'est toute l'installation : les migrations s'exécutent d'abord comme hook, les
deux Deployments démarrent chacun avec la bonne image, et le scheduler reçoit un
ServiceAccount autorisé à créer des pods tandis que les pods de tâches en reçoivent
un qui ne peut rien faire.

Pour accéder à l'interface :

```bash
helm upgrade brevis ./deployments/helm/brevis --reuse-values \
  --set api.ingress.enabled=true \
  --set api.ingress.host=brevis.example.com \
  --set api.ingress.tls.enabled=true \
  --set api.ingress.tls.secretName=brevis-tls
```

En production, gardez la chaîne de connexion hors de la release : mettez-la dans un
Secret et passez `--set database.existingSecret=brevis-db`. Passée en ligne, elle
atterrit dans le manifeste stocké de la release, que peut lire quiconque peut lire
les Secrets de ce namespace.

`helm show values ./deployments/helm/brevis` liste chaque valeur avec sa raison à
côté.

### Ce que le chart refuse

Tout ce qu'il ne peut pas rendre sûr échoue au moment du rendu, avec un message qui
dit ce qui casserait — pas à 3 h du matin, dans un run qui a produit des lignes en
double :

| refusé | parce que |
|---|---|
| `scheduler.replicas`, sous quelque forme que ce soit | deux matérialiseraient les mêmes slots. `FOR UPDATE SKIP LOCKED` les empêche de réclamer le même élément de la file ; rien ne les empêche de créer le même run planifié, et un `dbt build` s'exécuterait deux fois sur une même fenêtre sans que rien n'échoue |
| un identifiant manquant hors de `local` | le moteur refuse de démarrer sans, donc le chart vous livrerait un CrashLoopBackOff |
| `auth.secret` de moins de 32 octets | il signe le cookie de session, et le moteur impose le même minimum |
| un Ingress sans `host` | il correspondrait à toutes les requêtes atteignant le contrôleur — dans un cluster partagé, répondant pour le nom d'hôte de quelqu'un d'autre |
| `alerts.enabled` sans webhook | le pod viderait l'outbox et ne livrerait nulle part, ce qui est pire que de ne pas tourner : l'échec paraît alors annoncé |
| une valeur que le chart ne définit pas | une faute de frappe dans `--set` ne fait sinon rien, en silence |

`.github/scripts/helm-check.sh` vérifie tout cela sur la sortie rendue, plus les
invariants ci-dessus — un seul scheduler, `pods/log` présent, les migrations en
premier, le port des métriques hors du Service.

## Sans Helm

Sept manifestes se trouvent dans `deployments/kubernetes/`, prêts pour
`kubectl apply -f` après avoir modifié le namespace et le tag de l'image :

```bash
kubectl apply -f deployments/kubernetes/rbac.yaml
kubectl apply -f deployments/kubernetes/api.yaml
kubectl apply -f deployments/kubernetes/scheduler.yaml
```

Ils portent les mêmes décisions que le chart, avec le raisonnement en
commentaires, et `alert.yaml`, `report.yaml` et `publish-job.yaml` en plus. Les
sections ci-dessous détaillent le contenu de chacun.

## Les deux rôles

| rôle | commande | image | réplicas |
|---|---|---|---|
| API + interface | `serve` | `:0.11.2` (distroless) | autant que vous voulez |
| scheduler + file | `scheduler` | `:0.11.2-worker` (alpine) | **un** |

L'API est distroless parce qu'elle ne fait que servir du HTTP : elle n'exécute
rien, donc n'a pas besoin de shell. Le worker est alpine parce que les steps
`run:` en ont besoin.

:::warning Un seul réplica du scheduler
Deux processus qui matérialisent la même planification créent des runs en double.
Si vous avez besoin de disponibilité, utilisez `replicas: 1` avec
`strategy: Recreate`.
:::

## Migrations

Exécutez-les comme un Job avant le déploiement, jamais au démarrage de `serve` :

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: brevis-migrate
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: migrate
          image: areteacademy/brevis:0.40.0
          args: ["migrate", "up"]
          envFrom:
            - secretRef: {name: brevis-db}
```

## Deployment de l'API

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: brevis-api
spec:
  replicas: 2
  selector:
    matchLabels: {app: brevis-api}
  template:
    metadata:
      labels: {app: brevis-api}
    spec:
      serviceAccountName: brevis
      containers:
        - name: api
          image: areteacademy/brevis:0.40.0
          args: ["serve"]
          ports: [{containerPort: 8080}]
          envFrom:
            - secretRef: {name: brevis-db}
            - secretRef: {name: brevis-auth}
          env:
            - name: BREVIS_ENV
              value: production
          livenessProbe:
            httpGet: {path: /health, port: 8080}
          readinessProbe:
            httpGet: {path: /ready, port: 8080}
          resources:
            requests: {cpu: 100m, memory: 128Mi}
            limits: {memory: 256Mi}
```

:::note Pourquoi les deux sondes diffèrent
`/health` n'interroge **pas** la base de données ; `/ready` le fait, et nomme la
dépendance qui a échoué. Si la liveness dépendait de Postgres, une vacillation de
la base ferait **tuer** les pods de l'API par Kubernetes au lieu de simplement les
retirer du load balancer — et la reprise serait plus lente précisément quand le
système est déjà sous tension.
:::

## Deployment du scheduler

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: brevis-scheduler
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector:
    matchLabels: {app: brevis-scheduler}
  template:
    metadata:
      labels: {app: brevis-scheduler}
    spec:
      serviceAccountName: brevis
      containers:
        - name: scheduler
          image: areteacademy/brevis:0.40.0-worker
          args: ["scheduler", "--interval", "10s", "--concurrency", "5", "--max-pods", "10"]
          envFrom:
            - secretRef: {name: brevis-db}
            - secretRef: {name: brevis-auth}
          env:
            - name: BREVIS_ENV
              value: production
            - name: BREVIS_PODS
              value: "on"
            - name: BREVIS_POD_NAMESPACE
              value: data
            - name: BREVIS_POD_SERVICE_ACCOUNT
              value: brevis-runner
            - name: BREVIS_POD_ENV_FROM_SECRETS
              value: bigquery-cred
```

`BREVIS_PODS=on` **exige** un cluster : sans compte de service monté, le
démarrage échoue au lieu de retomber silencieusement sur l'exécution locale.

## Permissions

Le scheduler crée, surveille et supprime des pods dans le namespace d'exécution.
Rien de plus :

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: brevis-runner
  namespace: data
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["create", "get", "list", "watch", "delete"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
```

`pods/log` est ce qui permet de suivre la sortie d'un step jusque dans
l'interface. Sans lui, le run fonctionne et le log reste vide.

## Après le déploiement

```bash
kubectl -n data get pods -l app=brevis-api
kubectl -n data exec deploy/brevis-api -- brevis version
curl -fsS https://brevis.example.com/ready
```

Déclenchez un workflow simple depuis l'interface et confirmez que le pod du step
apparaît puis disparaît :

```bash
kubectl -n data get pods -w
```

## Quand un step reste en Pending

C'est presque toujours l'une de ces trois causes :

| cause | comment la confirmer |
|---|---|
| taint sans toleration | `kubectl describe pod` affiche `node(s) had untolerated taint` |
| `nodeSelector` sans nœud | `describe` affiche `didn't match Pod's node affinity` |
| ressource indisponible | `describe` affiche `Insufficient cpu` ou `memory` |

Pour inspecter le pod d'un step en échec plutôt que le regarder disparaître :

```bash
BREVIS_POD_KEEP_ON_FAILURE=true
```

Laissez-le désactivé en production — les pods arrêtés consomment du quota.

## Étapes suivantes

- [Un pod par step](/fr/docs/pod-per-step/index.md) — le modèle d'exécution
- [Configuration](/fr/docs/configuration/index.md) — toutes les variables `BREVIS_POD_*`
- [Observabilité](/fr/docs/observability/index.md) — quoi scruter, et depuis quel processus
