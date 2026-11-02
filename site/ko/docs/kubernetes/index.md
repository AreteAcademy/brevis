# Kubernetes

> 엔진을 배포하는 방법, 필요한 권한, 배포 후 확인할 사항입니다.

*https://brevis.sh/ko/docs/kubernetes/ · brevis.sh docs (ko)*

---

일반적인 설치는 같은 바이너리의 **Deployment 두 개**(API와 스케줄러)와 Postgres
하나로 구성됩니다.

## Helm으로 설치하기

차트는 저장소의 `deployments/helm/brevis`에 있습니다. 값 네 개는 필수이고 나머지는 모두
동작하는 기본값이 있습니다.

```bash
helm install brevis ./deployments/helm/brevis \
  --namespace data --create-namespace \
  --set database.url="postgres://brevis:pw@postgres/brevis?sslmode=require" \
  --set auth.user=admin \
  --set auth.passwordHash="$(brevis hash)" \
  --set auth.secret="$(openssl rand -base64 48)"
```

이것이 설치의 전부입니다. 마이그레이션이 훅으로 먼저 실행되고, 두 Deployment는 각각
올바른 이미지로 올라오며, 스케줄러는 pod를 만들 수 있는 ServiceAccount를, 작업 pod는
아무것도 할 수 없는 ServiceAccount를 받습니다.

인터페이스에 접근하려면 다음과 같이 합니다.

```bash
helm upgrade brevis ./deployments/helm/brevis --reuse-values \
  --set api.ingress.enabled=true \
  --set api.ingress.host=brevis.example.com \
  --set api.ingress.tls.enabled=true \
  --set api.ingress.tls.secretName=brevis-tls
```

운영 환경에서는 연결 문자열을 릴리스에 넣지 마세요. Secret에 넣고
`--set database.existingSecret=brevis-db`를 전달하세요. 인라인으로 전달하면 릴리스의
저장된 manifest에 들어가고, 해당 네임스페이스의 Secret을 읽을 수 있는 누구나 읽을 수
있습니다.

`helm show values ./deployments/helm/brevis`는 모든 값을 이유와 함께 나열합니다.

### 차트가 거부하는 것

안전하게 만들 수 없는 것은 렌더링 시점에, 무엇이 깨질지 알려 주는 메시지와 함께
실패합니다. 새벽 3시에 중복 행을 만든 run에서 알게 되는 것이 아닙니다.

| 거부되는 것 | 이유 |
|---|---|
| `scheduler.replicas` 자체 | 두 개면 같은 slot을 구체화합니다. `FOR UPDATE SKIP LOCKED`는 같은 큐 항목을 가져가는 것을 막지만, 같은 스케줄된 run을 만드는 것은 아무것도 막지 못하며, `dbt build`가 아무것도 실패하지 않은 채 한 윈도우에 대해 두 번 실행됩니다 |
| `local` 외 환경에서 자격 증명 누락 | 엔진은 자격 증명 없이 부팅을 거부하므로, 차트가 CrashLoopBackOff를 건네주는 셈입니다 |
| 32바이트 미만의 `auth.secret` | 세션 쿠키에 서명하는 값이며, 엔진도 같은 최솟값을 강제합니다 |
| `host` 없는 Ingress | 컨트롤러에 도달하는 모든 요청에 일치하게 됩니다. 공유 클러스터에서는 다른 사람의 호스트 이름에 대해 응답하게 됩니다 |
| 웹훅 없는 `alerts.enabled` | pod가 아웃박스를 비우면서 어디에도 전달하지 않게 되는데, 실행하지 않는 것보다 나쁩니다. 실패가 알려진 것처럼 보이기 때문입니다 |
| 차트가 정의하지 않은 값 | `--set`의 오타는 그렇지 않으면 아무 일도 하지 않고 조용히 지나갑니다 |

`.github/scripts/helm-check.sh`는 렌더링된 출력에 대해 이 모든 것에 더해 위의 불변
조건도 검증합니다. 스케줄러 하나, `pods/log` 존재, 마이그레이션 우선, 메트릭 포트가
Service에서 빠져 있음입니다.

## Helm 없이

`deployments/kubernetes/`에 manifest 일곱 개가 있으며, 네임스페이스와 이미지 태그를
수정한 뒤 `kubectl apply -f`로 바로 적용할 수 있습니다.

```bash
kubectl apply -f deployments/kubernetes/rbac.yaml
kubectl apply -f deployments/kubernetes/api.yaml
kubectl apply -f deployments/kubernetes/scheduler.yaml
```

차트와 같은 결정을 담고 있으며 이유는 주석에 있고, `alert.yaml`, `report.yaml`,
`publish-job.yaml`도 함께 있습니다. 아래 섹션은 각 파일에 무엇이 들어 있는지 차례로
설명합니다.

## 두 가지 역할

| 역할 | 명령 | 이미지 | 레플리카 |
|---|---|---|---|
| API + 인터페이스 | `serve` | `:0.11.2` (distroless) | 원하는 만큼 |
| 스케줄러 + 큐 | `scheduler` | `:0.11.2-worker` (alpine) | **하나** |

API는 HTTP만 제공하고 아무것도 실행하지 않아 셸이 필요 없으므로 distroless입니다.
worker는 `run:` step에 셸이 필요하므로 alpine입니다.

:::warning 스케줄러 레플리카는 하나
같은 스케줄을 구체화하는 프로세스가 둘이면 중복 run이 만들어집니다. 가용성이
필요하다면 `strategy: Recreate`와 함께 `replicas: 1`을 사용하세요.
:::

## 마이그레이션

`serve` 부팅 시가 아니라 배포 전에 Job으로 실행하세요.

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
          image: brevissh/brevis:0.40.0
          args: ["migrate", "up"]
          envFrom:
            - secretRef: {name: brevis-db}
```

## API Deployment

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
          image: brevissh/brevis:0.40.0
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

:::note 두 프로브가 다른 이유
`/health`는 데이터베이스를 조회하지 **않으며**, `/ready`는 조회하고 실패한 의존 대상을
명시합니다. liveness가 Postgres에 의존하면 데이터베이스가 흔들릴 때 Kubernetes가 API
pod를 로드 밸런서에서 빼기만 하는 것이 아니라 **죽이게** 되고, 시스템이 이미 부하를
받고 있을 때 오히려 복구가 느려집니다.
:::

## 스케줄러 Deployment

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
          image: brevissh/brevis:0.40.0-worker
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

`BREVIS_PODS=on`은 클러스터를 **요구합니다**. 서비스 계정이 마운트되지 않았다면 조용히
로컬 실행으로 폴백하는 대신 부팅이 실패합니다.

## 권한

스케줄러는 실행 네임스페이스에서 pod를 만들고, 감시하고, 삭제합니다. 그 이상은
하지 않습니다.

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

`pods/log`는 step의 출력을 인터페이스로 따라갈 수 있게 해 줍니다. 이 권한이 없으면 run은
동작하지만 로그는 비어 있습니다.

## 배포 후

```bash
kubectl -n data get pods -l app=brevis-api
kubectl -n data exec deploy/brevis-api -- brevis version
curl -fsS https://brevis.example.com/ready
```

인터페이스에서 간단한 workflow를 트리거하고 step의 pod가 나타났다가 사라지는지
확인하세요.

```bash
kubectl -n data get pods -w
```

## step이 Pending에 머물 때

거의 항상 다음 세 가지 중 하나입니다.

| 원인 | 확인 방법 |
|---|---|
| 톨러레이션 없는 taint | `kubectl describe pod`에 `node(s) had untolerated taint`가 표시됩니다 |
| 노드가 없는 `nodeSelector` | `describe`에 `didn't match Pod's node affinity`가 표시됩니다 |
| 리소스 부족 | `describe`에 `Insufficient cpu` 또는 `memory`가 표시됩니다 |

실패한 step의 pod가 사라지는 것을 지켜보는 대신 점검하려면 다음과 같이 합니다.

```bash
BREVIS_POD_KEEP_ON_FAILURE=true
```

운영 환경에서는 꺼 두세요. 중지된 pod가 쿼터를 소모합니다.

## 다음 단계

- [step당 pod 하나](/ko/docs/pod-per-step/index.md) — 실행 모델
- [구성](/ko/docs/configuration/index.md) — 모든 `BREVIS_POD_*` 변수
- [관측성](/ko/docs/observability/index.md) — 무엇을 어느 프로세스에서 수집할지
