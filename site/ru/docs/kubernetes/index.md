# Kubernetes

> Как развернуть engine, с какими правами и что проверить после этого.

*https://brevis.sh/ru/docs/kubernetes/ · brevis.sh docs (ru)*

---

Типичная установка включает **два Deployment** одного и того же бинарного
файла — API и планировщик — плюс Postgres.

## Установка через Helm

Чарт находится в репозитории, в `deployments/helm/brevis`. Четыре значения
обязательны, у всего остального есть рабочее значение по умолчанию:

```bash
helm install brevis ./deployments/helm/brevis \
  --namespace data --create-namespace \
  --set database.url="postgres://brevis:pw@postgres/brevis?sslmode=require" \
  --set auth.user=admin \
  --set auth.passwordHash="$(brevis hash)" \
  --set auth.secret="$(openssl rand -base64 48)"
```

Это вся установка: сначала как хук выполняются миграции, оба Deployment
поднимаются каждый со своим образом, а планировщик получает ServiceAccount,
которому разрешено создавать pod, тогда как pod задач получают такой, которому
не разрешено ничего.

Чтобы получить доступ к интерфейсу:

```bash
helm upgrade brevis ./deployments/helm/brevis --reuse-values \
  --set api.ingress.enabled=true \
  --set api.ingress.host=brevis.example.com \
  --set api.ingress.tls.enabled=true \
  --set api.ingress.tls.secretName=brevis-tls
```

В продакшене не включайте строку подключения в релиз: положите её в Secret и
передайте `--set database.existingSecret=brevis-db`. Если передать её прямо в
команде, она попадёт в сохранённый манифест релиза, который может прочитать
любой, кто имеет доступ к Secrets в этом пространстве имён.

`helm show values ./deployments/helm/brevis` перечисляет все значения с
пояснением рядом с каждым.

### Что чарт отклоняет

Всё, что он не может сделать безопасным, завершается ошибкой на этапе
рендеринга с сообщением о том, что сломалось бы, — а не в три часа ночи, в
запуске, породившем дублирующиеся строки:

| отклоняется | потому что |
|---|---|
| `scheduler.replicas` вообще | два планировщика материализовали бы одни и те же слоты. `FOR UPDATE SKIP LOCKED` не даёт им забрать один и тот же элемент очереди; ничто не мешает им создать один и тот же запланированный запуск, и `dbt build` выполнился бы дважды на одном окне, и при этом ничего бы не упало |
| отсутствие учётных данных вне `local` | engine отказывается стартовать без них, и чарт подсунул бы вам CrashLoopBackOff |
| `auth.secret` короче 32 байт | он подписывает cookie сессии, и engine требует тот же минимум |
| Ingress без `host` | он совпадал бы с каждым запросом, доходящим до контроллера, — в общем кластере отвечая за чужое имя хоста |
| `alerts.enabled` без webhook | pod разбирал бы outbox и никуда ничего не доставлял, что хуже, чем не работать: сбой тогда выглядит объявленным |
| значение, которого чарт не определяет | опечатка в `--set` в противном случае молча ничего не делает |

`.github/scripts/helm-check.sh` проверяет всё это на отрендеренном выводе, а
также перечисленные выше инварианты: один планировщик, наличие `pods/log`,
миграции первыми, порт метрик вне Service.

## Без Helm

Семь манифестов находятся в `deployments/kubernetes/` и готовы к
`kubectl apply -f` после правки пространства имён и тега образа:

```bash
kubectl apply -f deployments/kubernetes/rbac.yaml
kubectl apply -f deployments/kubernetes/api.yaml
kubectl apply -f deployments/kubernetes/scheduler.yaml
```

Они воплощают те же решения, что и чарт, с обоснованиями в комментариях, и
помимо этого содержат `alert.yaml`, `report.yaml` и `publish-job.yaml`.
Разделы ниже разбирают, что содержит каждый из них.

## Две роли

| роль | команда | образ | реплики |
|---|---|---|---|
| API + интерфейс | `serve` | `:0.11.2` (distroless) | сколько угодно |
| планировщик + очередь | `scheduler` | `:0.11.2-worker` (alpine) | **одна** |

API построен на distroless, потому что он только отдаёт HTTP: ничего не
исполняет, поэтому оболочка ему не нужна. Воркер построен на alpine, потому
что шагам `run:` оболочка нужна.

:::warning Одна реплика планировщика
Два процесса, материализующие одно расписание, создают дублирующиеся запуски.
Если нужна доступность, используйте `replicas: 1` со `strategy: Recreate`.
:::

## Миграции

Запускайте их как Job перед развёртыванием, но никогда при старте `serve`:

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

## Deployment API

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

:::note Почему две пробы различаются
`/health` **не** обращается к базе данных; `/ready` обращается и называет
зависимость, которая отказала. Если бы liveness зависела от Postgres, сбой
базы данных заставил бы Kubernetes **убивать** pod API, а не просто убирать
их из балансировщика нагрузки, — и восстановление замедлилось бы именно тогда,
когда система и так под нагрузкой.
:::

## Deployment планировщика

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

`BREVIS_PODS=on` **требует** кластер: если сервисный аккаунт не смонтирован,
старт завершается ошибкой, а не молча откатывается к локальному исполнению.

## Права

Планировщик создаёт, отслеживает и удаляет pod в пространстве имён
исполнения. Ничего сверх этого:

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

`pods/log` позволяет выводить вывод шага в интерфейс. Без него запуск
работает, а журнал остаётся пустым.

## После развёртывания

```bash
kubectl -n data get pods -l app=brevis-api
kubectl -n data exec deploy/brevis-api -- brevis version
curl -fsS https://brevis.example.com/ready
```

Запустите из интерфейса простой workflow и убедитесь, что pod шага появляется и
исчезает:

```bash
kubectl -n data get pods -w
```

## Когда шаг остаётся в Pending

Почти всегда причина — одна из этих трёх:

| причина | как подтвердить |
|---|---|
| taint без toleration | `kubectl describe pod` показывает `node(s) had untolerated taint` |
| `nodeSelector` без подходящего узла | `describe` показывает `didn't match Pod's node affinity` |
| недоступный ресурс | `describe` показывает `Insufficient cpu` или `memory` |

Чтобы изучить pod упавшего шага, а не смотреть, как он исчезает:

```bash
BREVIS_POD_KEEP_ON_FAILURE=true
```

В продакшене оставляйте выключенным — остановленные pod расходуют квоту.

## Дальнейшие шаги

- [Pod на шаг](/ru/docs/pod-per-step/index.md) — модель исполнения
- [Конфигурация](/ru/docs/configuration/index.md) — все переменные `BREVIS_POD_*`
- [Наблюдаемость](/ru/docs/observability/index.md) — что собирать и из какого процесса
