# step당 pod 하나

> 각 step이 자기만의 pod에서 실행되는 이유와, 그것이 비용과 격리에 가져오는 변화입니다.

*https://brevis.sh/ko/docs/pod-per-step/ · brevis.sh docs (ko)*

---

Kubernetes에서는 **workflow의 각 step이 pod가 되며**, 그 step에 선언된 이미지를
사용합니다. 작업을 기다리는 범용 worker는 없습니다. 작업이 자신의 런타임을 직접
가져옵니다.

![Diagram: the Core starts one pod per step — extract on Go, prepare on Python, load on Go and transform on dbt. Steps pass context to one another, and the SDK is what reads and writes it.](/assets/execution.svg)

```
scheduler                          cluster
─────────                          ───────
walks the graph
  for each ready step:
    builds the Pod  ───────────►  Pod: the step's image, the step's command
    follows status      ◄───────  Pending → Running → Succeeded/Failed
    follows the log     ◄───────  container stdout
    reads the exit code ◄───────  containerStatuses[0].terminated
    deletes the pod ───────────►  (gone)
```

## 무엇이 달라지는가

단일체 worker는 어떤 step이든 필요로 할 수 있는 모든 것을 이미지에 담도록 강제합니다.
실제로는 다음과 같습니다.

| | 단일 이미지 | step당 pod 하나 |
|---|---|---|
| dbt step | 1.9 GB · 1Gi RAM | 1.9 GB · 1Gi RAM |
| 그 옆의 Go fetcher | **1.9 GB · 1Gi** | **12 MB · 32Mi** |
| dbt 버전 올리기 | 전부 다시 빌드 | 해당 workflow의 YAML 한 줄 |
| 메모리가 새는 step | worker와 이웃 step까지 함께 죽음 | 혼자 죽음 |

세 번째 행은 가장 눈에 띄지 않지만 일상적으로는 가장 비용이 큽니다. 이미지가 하나뿐이면
한 파이프라인에서 dbt를 1.10에서 1.11로 올리는 순간 모든 파이프라인이 함께 올라가야
합니다.

## 역할별 이미지

이미지는 프로젝트별이 아니라 **역할**별입니다. 이미지에 남아 있는 것은 모두 그 step을
실행하는 모든 노드가 가져옵니다.

| 역할 | 크기 | 콜드 스타트 |
|---|---|---|
| **Go** | **5.8 MB** | 0.18 s |
| **Python** | **118 MB** | 0.54 s |
| **dbt** | **620 MB** | 3.3 s |

이전에는 모든 것을 담은 1.87 GB짜리 이미지 하나였습니다.

dbt는 빌드 시점에 `dbt parse`를 실행하고 `target/partial_parse.msgpack`을 이미지에
넣어 둡니다. 호출할 때마다 pod당 **2.68초**를 줄여 줍니다.

## 각 step이 실행되는 곳

결정은 부팅 시 한 번 내려지며, `BREVIS_PODS`와 클러스터 유무에 따라 달라집니다.

| `BREVIS_PODS` | 클러스터 있음 | 클러스터 없음 |
|---|---|---|
| `auto` (기본값) | `image:`가 있는 step은 pod가 됩니다 | 모두 로컬 프로세스로 실행하고 로그에 경고를 남깁니다 |
| `on` | `image:`가 있는 step은 pod가 됩니다 | **부팅 오류** |
| `off` | 모두 로컬 프로세스 | 모두 로컬 프로세스 |

**`host:`가 있는 step은 이 중 어느 것도 거치지 않습니다.** pod와 프로세스보다 먼저
검사되어 이미 실행 중인 머신으로 가며, `BREVIS_PODS`는 적용되지 않고 로컬 프로세스로
폴백하지도 않습니다. 의도된 동작입니다. `image:`가 경고와 함께 로컬 실행으로
저하되는 것은 컨테이너와 프로세스가 같은 명령을 두 가지로 감싼 것이기 때문이고, host는
그렇지 않습니다. 라이선스나 GPU, 데이터가 있는 곳이 host입니다.

`auto`가 기본값인 것은 같은 바이너리가 두 곳에서 실행되기 때문입니다. 노트북에는
마운트된 서비스 계정이 없으므로 로컬 프로세스로 폴백하고, 클러스터에는 있으므로 pod를
만들기 시작합니다.

`on`은 조용히 로컬 실행이 되어서는 **안 되는** 배포를 위해 존재합니다. 그런 배포에서는
클러스터가 없는 것이 부팅 오류여야 합니다.

:::warning `brevis run`은 pod를 만들지 않습니다
`run` 명령은 프로세스 실행기만 만듭니다. `image:`가 있는 step도 인스턴스 자체에서
실행되며, 그렇다고 알려 줍니다. 조용히 넘어가면 step이 선언된 이미지에서 실행된 것처럼
보일 텐데, 이는 결과가 이미 틀린 뒤에야 드러나는 종류의 실수입니다.
:::

## 아이덴티티와 자격 증명

pod가 어떤 아이덴티티로 올라오는지는 workflow가 아니라 **설치 환경**이 정하는
사항입니다. 파이프라인이 실행될 서비스 계정을 직접 선택할 수 있어서는 안 되기
때문입니다. 따라서 환경에서 가져옵니다.

```bash
BREVIS_POD_NAMESPACE=data
BREVIS_POD_SERVICE_ACCOUNT=brevis-runner
BREVIS_POD_PULL_SECRETS=registry-cred
BREVIS_POD_ENV_FROM_SECRETS=bigquery-cred,api-tokens
```

pod 모드에서 작업 환경은 `BREVIS_TASK_ENV`가 아니라 클러스터 Secret에서 가져옵니다.

## 노드 셀렉터와 톨러레이션

```bash
BREVIS_POD_NODE_SELECTOR='kubernetes.io/arch=arm64'
BREVIS_POD_TOLERATIONS='dedicated=data:NoSchedule'
```

톨러레이션에는 `Equal` 연산자만 허용됩니다. `Exists`는 해당 키를 가진 **모든** taint를
허용하게 되는데, 환경 변수에서 오는 결정으로는 너무 넓습니다.

형식이 잘못된 톨러레이션은 부팅 오류로 만들지 않고 **무시됩니다**. 그러면 pod가
`Pending`으로 남아 눈에 보이지만, 부팅을 거부하면 그 풀이 필요 없는 workflow까지 함께
멈추기 때문입니다.

## 실패 디버깅

```bash
BREVIS_POD_KEEP_ON_FAILURE=true
```

실패한 pod를 남겨 두어 `kubectl logs`와 `kubectl describe`를 쓸 수 있습니다. 운영
환경에서는 꺼 두세요. 중지된 pod가 클러스터 쿼터를 소모합니다.

## 다음 단계

- [Kubernetes](/ko/docs/kubernetes/index.md) — 전체 배포
- [구성](/ko/docs/configuration/index.md) — 모든 `BREVIS_POD_*` 변수
- [관측성](/ko/docs/observability/index.md) — 두 프로세스의 메트릭
