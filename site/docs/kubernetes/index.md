# Kubernetes

> Como implantar o engine, com que permissões, e o que verificar depois.

*https://brevis.sh/docs/kubernetes/ · brevis.sh docs (pt-BR)*

---

Uma instalação típica tem **dois Deployments** do mesmo binário — a API e o
scheduler — e um Postgres.

## Instalando com Helm

O chart está no repositório, em `deployments/helm/brevis`. Quatro valores são
obrigatórios e todo o resto tem default que funciona:

```bash
helm install brevis ./deployments/helm/brevis \
  --namespace data --create-namespace \
  --set database.url="postgres://brevis:pw@postgres/brevis?sslmode=require" \
  --set auth.user=admin \
  --set auth.passwordHash="$(brevis hash)" \
  --set auth.secret="$(openssl rand -base64 48)"
```

É a instalação inteira: as migrations rodam antes, como hook, os dois
Deployments sobem cada um com a sua imagem, e o scheduler recebe uma
ServiceAccount que pode criar pods enquanto os pods de task recebem uma que não
pode nada.

Para chegar na interface:

```bash
helm upgrade brevis ./deployments/helm/brevis --reuse-values \
  --set api.ingress.enabled=true \
  --set api.ingress.host=brevis.example.com \
  --set api.ingress.tls.enabled=true \
  --set api.ingress.tls.secretName=brevis-tls
```

Em produção, mantenha a string de conexão fora do release: coloque num Secret e
passe `--set database.existingSecret=brevis-db`. Passada inline, ela vai para o
manifesto guardado do release, que qualquer um com permissão de ler Secrets
naquele namespace lê.

`helm show values ./deployments/helm/brevis` lista cada valor com o motivo ao
lado.

### O que o chart recusa

O que ele não consegue tornar seguro falha na renderização, com uma mensagem
dizendo o que quebraria — e não às 3 da manhã, num run que gerou linhas
duplicadas:

| recusado | porque |
|---|---|
| `scheduler.replicas`, em qualquer valor | dois materializariam os mesmos slots. O `FOR UPDATE SKIP LOCKED` impede que peguem o mesmo item da fila; nada impede que criem o mesmo run agendado, e um `dbt build` rodaria duas vezes sobre a mesma janela sem nada falhar |
| credencial ausente fora de `local` | o engine recusa subir sem ela, então o chart estaria te entregando um CrashLoopBackOff |
| `auth.secret` com menos de 32 bytes | ele assina o cookie de sessão, e o engine exige o mesmo mínimo |
| Ingress sem `host` | casaria com toda requisição que chega ao controller — num cluster compartilhado, respondendo pelo hostname de outro |
| `alerts.enabled` sem webhook | o pod drenaria a outbox e entregaria em lugar nenhum, o que é pior que não rodar: a falha passa a parecer anunciada |
| um valor que o chart não define | um typo no `--set` de outra forma não faz nada, em silêncio |

O `.github/scripts/helm-check.sh` verifica tudo isso na saída renderizada, mais
os invariantes acima — um scheduler só, `pods/log` presente, migrations
primeiro, a porta de métricas fora do Service.

## Sem Helm

São sete manifests em `deployments/kubernetes/`, prontos para `kubectl apply -f`
depois de ajustar o namespace e a tag da imagem:

```bash
kubectl apply -f deployments/kubernetes/rbac.yaml
kubectl apply -f deployments/kubernetes/api.yaml
kubectl apply -f deployments/kubernetes/scheduler.yaml
```

Eles carregam as mesmas decisões do chart, com o raciocínio nos comentários, e
ainda `alert.yaml`, `report.yaml` e `publish-job.yaml`. As seções abaixo
percorrem o conteúdo de cada um.

## Os dois papéis

| papel | comando | imagem | réplicas |
|---|---|---|---|
| API + interface | `serve` | `:0.11.2` (distroless) | quantas quiser |
| scheduler + fila | `scheduler` | `:0.11.2-worker` (alpine) | **uma** |

A API é distroless porque só serve HTTP: não executa nada, então não precisa de
shell. O worker é alpine porque os passos `run:` precisam de um.

:::warning Uma réplica de scheduler
Dois processos materializando a mesma agenda criam runs duplicados. Se precisar
de disponibilidade, use `replicas: 1` com `strategy: Recreate`.
:::

## Migrations

Rode como Job antes do deploy, nunca no boot do `serve`:

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
          image: daniel3843/brevis:0.11.2
          args: ["migrate", "up"]
          envFrom:
            - secretRef: {name: brevis-db}
```

## Deployment da API

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
          image: daniel3843/brevis:0.11.2
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

:::note Por que as duas probes diferem
`/health` **não** consulta o banco; `/ready` consulta e nomeia a dependência que
falhou. Se a liveness dependesse do Postgres, uma oscilação do banco faria o
Kubernetes **matar** os pods da API em vez de apenas tirá-los do balanceador — e
a recuperação ficaria mais lenta justamente quando o sistema já está sob
estresse.
:::

## Deployment do scheduler

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
          image: daniel3843/brevis:0.11.2-worker
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
              value: dados
            - name: BREVIS_POD_SERVICE_ACCOUNT
              value: brevis-runner
            - name: BREVIS_POD_ENV_FROM_SECRETS
              value: bigquery-cred
```

`BREVIS_PODS=on` **exige** cluster: sem service account montada, o boot falha em
vez de cair silenciosamente para execução local.

## Permissões

O scheduler cria, observa e apaga pods no namespace de execução. Nada além
disso:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: brevis-runner
  namespace: dados
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["create", "get", "list", "watch", "delete"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
```

`pods/log` é o que permite seguir a saída do passo até a interface. Sem ele, o
run funciona e o log fica vazio.

## Depois do deploy

```bash
kubectl -n dados get pods -l app=brevis-api
kubectl -n dados exec deploy/brevis-api -- brevis version
curl -fsS https://brevis.exemplo.com/ready
```

Dispare um workflow simples pela interface e confirme que o pod do passo aparece
e desaparece:

```bash
kubectl -n dados get pods -w
```

## Quando um passo fica Pending

Quase sempre é uma destas três:

| causa | como confirmar |
|---|---|
| taint sem toleração | `kubectl describe pod` mostra `node(s) had untolerated taint` |
| `nodeSelector` sem nó | `describe` mostra `didn't match Pod's node affinity` |
| recurso indisponível | `describe` mostra `Insufficient cpu` ou `memory` |

Para inspecionar o pod de um passo que falhou, em vez de vê-lo desaparecer:

```bash
BREVIS_POD_KEEP_ON_FAILURE=true
```

Deixe desligado em produção — pods parados consomem cota.

## Próximos passos

- [Pod por passo](/docs/pod-per-step/index.md) — o modelo de execução
- [Configuração](/docs/configuration/index.md) — todas as variáveis `BREVIS_POD_*`
- [Observabilidade](/docs/observability/index.md) — o que raspar, e de qual processo
