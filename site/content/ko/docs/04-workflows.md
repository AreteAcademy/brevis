---
title: Workflows
description: 전체 YAML 형식입니다. step, 의존성, 이미지, 리소스, 스케줄을 다룹니다.
group: 개념
order: 4
slug: workflows
---

workflow는 YAML 파일입니다. **무엇을 실행할지**, **어떤 순서로** 실행할지,
**어떤 런타임으로** 실행할지만 선언하며, 그 외에는 아무것도 선언하지 않습니다.

## 최소 구조

```yaml
name: hello
steps:
  - id: only
    run: echo done
```

`name`은 데이터베이스와 인터페이스에서 workflow를 식별합니다. `steps`는 step의
목록이며, 각 step은 고유한 `id`와 `run`을 가집니다.

### `description` — 용도 설명

선택 사항이며 자유 형식이고, 엔진은 이 값을 읽지 않습니다.

```yaml
name: daily-orders
description: >
  Pulls yesterday's orders from the vendor and lands them in bronze.
  Runs at 04:00 because the vendor closes its books at 03:30.
steps:
  - id: extract
    run: python fetch.py
```

이 값은 workflow 화면 상단의 건수와 스케줄 위에 표시됩니다. 그래프는 flow가 **무엇을
하는지**는 이미 보여 주지만, 왜 존재하는지를 알려 주는 곳은 여기뿐입니다. 새벽 세
시에 처음 이 화면을 여는 사람에게 실제로 필요한 것이 바로 그 정보입니다.

설명이 없는 workflow는 그 자리에 아무것도 표시하지 않습니다. 빈 블록을 두면 불러오기에
실패한 것처럼 보이기 때문입니다.

## 순서: chain 또는 dag

```yaml
type: chain   # order is the file's order
type: dag     # order comes from depends_on
```

`chain`은 편의 문법입니다. 파서가 순서를 엣지로 바꾸며, **엔진은 DAG만 이해합니다**.
순서가 선형이 아닐 때는 `dag`를 사용하세요.

```yaml
type: dag

steps:
  - id: prepare
    run: ./prepare.sh

  # Siblings: they depend on the same step, so they run in parallel.
  - id: extract
    run: ./extract.sh
    depends_on: [prepare]

  - id: validate
    run: ./validate.sh
    depends_on: [prepare]

  - id: publish
    run: ./publish.sh
    depends_on: [extract, validate]
```

러너는 그래프를 **레벨 단위로** 진행합니다. 한 레벨 안의 모든 step은 병렬로 실행되고,
다음 레벨은 이전 레벨이 완전히 끝난 뒤에야 시작됩니다.

:::warning 실패하면 레벨 전체가 멈춥니다
러너는 한 레벨에서 **처음** 발생한 실패에서 멈추며 다음 레벨을 시작하지 않습니다.
오류 이후에도 계속 진행하면 완전해 보이는 부분 결과가 만들어지기 때문입니다.
:::

## 스케줄

```yaml
schedule: "0 5 * * *"   # five-field cron
concurrency: 1
```

`schedule`이 없으면 workflow는 수동입니다. 인터페이스의 트리거, `brevis run`,
`backfill`로만 실행됩니다.

`concurrency: 1`은 같은 workflow의 동시 run 수를 제한합니다. run 하나가 15분을
넘겼을 때 `*/15` 스케줄이 자기 자신과 겹치지 않게 막아 주는 것이 바로 이 설정입니다.

같은 테이블에 쓰는 **서로 다른** 두 workflow에는 **group**이 필요합니다. 같은 dbt
모델을 빌드하는 일간 workflow와 `*/15` workflow는 각각 다음과 같이 선언합니다.

```yaml
concurrency:
  group: jaffle_models   # the shared resource, named the same in both files
  limit: 1               # optional; a group without one means 1
```

group에 속한 모든 workflow의 run은 시작한 원인(스케줄, 랜딩, backfill, 클릭)에
관계없이 하나의 한도에 합산됩니다. group이 가득 찬 것을 발견한 run은 **큐에서
대기**하며, 건너뛰거나 실패하지 않습니다. 같은 테이블에 쓰는 세 번째 workflow도
group 이름을 지정하면 합류합니다. 대기하는 동안 run 페이지에는 어떤 group인지, 한도가
얼마인지, 어떤 run이 슬롯을 쥐고 있는지가 표시됩니다.

- 모든 구성원은 **같은 한도**를 선언해야 합니다. `validate`와 `publish`는 한 파일에서는
  1로, 다른 파일에서는 3으로 선언된 group을 거부하며, 이미 게시된 구성원과의 비교도
  포함됩니다.
- group은 **Brevis run**만 직렬화합니다. 누군가 자기 컴퓨터에서 직접 시작한 `dbt run`은
  이 큐에 속하지 않으며, 똑같이 실패합니다.

## 이미지와 리소스

```yaml
image: us-central1-docker.pkg.dev/example/apps/dbt:1.10.3
resources:
  cpu: 200m
  memory: 1Gi
  limits: {memory: 2Gi}
```

최상위에 선언하면 **모든 step의 기본값**이 됩니다. 각 step은 자신의 값을 재정의할 수
있습니다.

```yaml
steps:
  - id: bronze
    run: dbt build --select bronze+          # inherits the top-level image

  - id: notify
    image: ghcr.io/example/notify:0.3        # another runtime
    shell: false                             # distroless has no shell
    run: /notify --channel data
    resources: {cpu: 25m, memory: 32Mi, limits: {memory: 64Mi}}
    depends_on: [bronze]
```

덕분에 Go fetcher는 1.9 GB짜리 `dbt build` 옆에서도 12 MB와 32Mi만 차지하며, 둘 다
더 큰 쪽의 크기를 치르지 않습니다. [step당 pod 하나](/docs/pod-per-step/)를
참고하세요.

## step 필드

| 필드 | | |
|---|---|---|
| `id` | **필수** | workflow 안에서 고유합니다. 그래프와 로그에 표시되는 이름입니다 |
| `run` | | 명령 |
| `image` | | step의 이미지. 없으면 최상위 값을 상속합니다 |
| `host` | | 엔진이 관리하지 **않는** 머신으로 명령을 보냅니다. `image`의 대안이며 함께 쓰지 않습니다. [Kubernetes 가이드의 `host:`](https://github.com/AreteAcademy/brevis/blob/master/docs/KUBERNETES.md#a-third-place-a-step-can-run-host)를 참고하세요 |
| `shell` | `true` | `false`는 셸 없이 실행합니다. distroless에서는 필수입니다 |
| `depends_on` | | 먼저 끝나야 하는 `id`의 목록. 항목은 `{step, label}`일 수도 있습니다 |
| `marker` | `false` | `start`나 `end`처럼 명령이 없는 step |
| `resources` | | 해당 step의 `cpu`, `memory`, `limits` |
| `runtime`, `tools` | | step의 언어와 도구. [step 런타임](/docs/runtime/)을 참고하세요 |
| `when` | `all_success` | 의존 대상이 어떤 상태일 때 이 step을 실행할지. 아래를 참고하세요 |
| `unless_empty` | | 할 일이 있는지 결정하는 컨텍스트 키. 아래를 참고하세요 |
| `for_each` | | 목록을 담은 컨텍스트 키. step은 요소마다 한 번씩 실행됩니다. 아래를 참고하세요 |
| `group` | | 이 step을 이름이 있는 접이식 상자 안에 그립니다. 아래를 참고하세요 |
| `uses` | | 이 step 자리를 대신하는 steps를 가진 다른 workflow. 아래를 참고하세요 |
| `on_error` | | 이 step의 실패를 알립니다. 아래를 참고하세요 |

## 할 일이 있을 때만 step 실행하기

```python
# in extract
context.set(has_rows=len(rows) > 0)
```

```yaml
  - id: transform
    run: ./transform.sh
    depends_on: [extract]
    unless_empty: extract.has_rows
```

`unless_empty`는 표현식이 아니라 **키**를 지정합니다. 판단은 step이 내려 답을
게시하고, 엔진은 키 하나를 읽어 비어 있는지만 확인합니다.

| 비어 있는 것으로 간주 | 값이 있는 것으로 간주 |
|---|---|
| `false`, `0`, `""`, `null`, `[]`, `{}` | 그 외 모든 값 |

**문자열** `"false"`는 값이 있는 것입니다. 그 다섯 글자를 게시한 step은 무언가를
게시한 것이며, 그것이 불리언을 뜻한다고 추측하기 시작하면 규칙이 작성자가 볼 수 없는
의견을 갖게 됩니다. 실제 불리언을 게시하세요.

### 표현식을 쓰지 않는 이유

가장 먼저 떠오르는 설계는 `when: "{{ context.extract.rows > 0 }}"`입니다. 작은
표현식 언어는 큰 약속입니다. 파서, JSON `any` 전반에서 `>`가 무엇을 뜻하는지 정하는
타입 시스템, 표현식이 다른 사람이 작성한 YAML에서 나온다는 데서 오는 보안 문제, 문자열
안을 가리키는 오류 메시지가 모두 필요합니다. 표현식 언어를 가진 오케스트레이터마다
그에 대한 버그 트래커가 가득합니다.

여기서는 판단이 작성자가 이미 쓰고 있는 언어 안에 있으며, 작성자 자신의 테스트
프레임워크가 닿을 수 있습니다.

### 없는 키는 빈 값이 아니라 실패입니다

키가 없으면 step은 **실패**하고, 메시지에는 게시하는 쪽이 실제로 쓴 키가 함께
표시됩니다.

```
step "transform" reads `extract.has_row`, and "extract" published "has_rows" but not "has_row"
```

없는 키를 "비어 있으니 건너뜀"으로 읽으면 오타가 어디에도 이유가 남지 않은 채 조용히,
영원히 실행되지 않는 step이 됩니다. 이 기능에서 일어날 수 있는 가장 나쁜 결과입니다.

키는 항상 그것을 게시하는 step으로 한정되며, 그 step은 이 step이 의존하는 step이어야
합니다. 두 조건 모두 **publish** 시점에 거부됩니다. 절대 볼 수 없는 키로 게이팅된
step은 영원히 건너뛰어질 것이고, 야간 작업이 멈춘 뒤에야 알게 되면 너무 늦습니다.

## 요소마다 step 한 번씩 실행하기

```python
# in extract
context.set(partitions=["2026-01", "2026-02", "2026-03"])
```

```yaml
  - id: load
    run: ./load.sh "$BREVIS_MAP_VALUE"
    depends_on: [extract]
    for_each: extract.partitions
```

step은 요소마다 한 번씩 실행되며, 각각 고유한 행, 재시도, 종료 코드를 가집니다.
그래프에는 노드 세 개가 아니라 **`[3]`이 붙은 노드 하나**가 표시됩니다.

| 변수 | |
|---|---|
| `BREVIS_MAP_INDEX` | `0`, `1`, `2` … |
| `BREVIS_MAP_VALUE` | 요소 |

JSON 문자열은 **따옴표 없이** 전달되므로, `["2026-01"]`에 대한 `for_each`는 셸에
`"2026-01"`이 아니라 `2026-01`을 넘깁니다. 숫자, 객체, 목록 등 그 외의 값은 JSON
그대로 전달됩니다.

매핑되지 않은 step에는 두 변수 모두 존재하지 않습니다. 항상 존재하지만 항상 비어 있는
변수는 환경을 읽는 사람에게 그 변수를 무시하도록 가르치게 됩니다.

### DAG의 모양은 바뀌지 않습니다

매핑된 step도 여전히 엣지 집합이 하나인 노드 하나입니다. 그 아래의 행 수만 달라지므로
레이아웃은 늘 그대로이고, `[3]`은 읽는 시점에 그 행들로부터 계산됩니다. 저장되는 값이
없으므로 동기화할 것이 없고, 잘못된 개수는 쿼리를 고치면 더 이상 남지 않습니다.

인스턴스가 아직 진행 중일 때 카드에는 `[2/3]`로 표시됩니다.

### 인스턴스 하나가 실패하면 step이 실패합니다

파티션 세 개 중 하나가 적재되지 않았다면 그 step은 맡은 일을 하지 못한 것이고, 그
아래의 step들도 그 사실을 압니다. 대부분 성공했다는 이유로 초록색이 되는 노드는
거짓말하는 배지입니다.

**재시도는 실패한 인스턴스만 다시 실행합니다.** 정상적으로 끝난 열여덟 개 파티션은 두
개가 깨졌다는 이유로 다시 적재되지 않습니다.

### 빈 목록은 성공이 아니라 `skipped`입니다

할 일이 없어서 아무것도 하지 않은 step이 그 일을 성공한 것은 아닙니다. 인스턴스가
0개인데 초록색인 노드는 누군가 그 위에 대시보드를 만들기 딱 좋은 종류의 것입니다.

**목록이 아닌** 값은 실패하며, 키가 없는 경우도 마찬가지입니다. `unless_empty:`와 같은
정책이며 이유도 같습니다. 인스턴스를 조용히 0개 만들어 내는 오타는 step을 영원히
비활성화하기 때문입니다.

### 팬아웃에는 한계가 있고, 그 한계는 상속됩니다

목록은 컨텍스트에 실려 이동하는데, 컨텍스트에는 kubelet에서 비롯된 4096바이트의 상한이
있습니다. workflow는 4킬로바이트 안에서 표현할 방법을 찾지 못하는 한 pod 1만 개를
요청할 수 없습니다. 우회하기보다 지킬 가치가 있는 한계입니다.

### 매핑된 step이 게시하는 것

출력은 각 인스턴스의 행에 기록되며, 아래의 step들에는 **보이지 않습니다**. 인스턴스
네 개가 한 step 이름으로 게시하면 키 하나에 값이 네 개가 되고,
`context.String("load.bucket")`에 대해 추측이 아닌 답은 없습니다. step은 이를 조용히
버리지 않고 로그에 밝힙니다.

## 그래프에서 step 묶기

```yaml
steps:
  - id: extract_orders
    group: sales_data_reporting
    run: ./extract.sh
  - id: load_orders
    group: sales_data_reporting
    depends_on: [extract_orders]
    run: ./load.sh
```

step은 이름이 있는 접이식 상자 안에 그려집니다. DAG가 커져서 읽기 어려워질 때를 위한
Airflow의 TaskGroup과 같은 기능입니다.

**시각적인 용도뿐입니다.** Airflow의 TaskGroup은 내부 id에 접두사도 *붙여서*
`extract`가 `sales.extract`가 되지만, 여기서는 그렇지 않습니다. 접두사를 붙이면 기존
workflow의 모든 `depends_on`, 모든 컨텍스트 키, 기록된 모든 행이 바뀌는데, 이 기능의
가치는 큰 그래프를 더 잘 읽히게 하는 것뿐이기 때문입니다. 네임스페이스는 나중에
추가할 수 있으며, 이 기능에 대한 엄격한 추가가 됩니다.

group은 컨테이너가 아니라 **레이블**입니다. step은 전역 id를 유지하고, group은 여러
레벨에 걸칠 수 있으며, 실행에는 아무 변화가 없습니다.

group 이름을 클릭하면 접힙니다. 그 안의 step이 사라지고 경계를 넘나들던 화살표는
상자를 가리키게 됩니다.

## 다른 workflow 재사용하기

```yaml
# nightly.yaml
steps:
  - id: prepare
    run: ./prepare.sh

  - id: mlops
    uses: ml_training       # another workflow in the same publish
    depends_on: [prepare]

  - id: report
    run: ./report.sh
    depends_on: [mlops]
```

**publish** 시점에 `ml_training`의 step들이 해당 step 자리를 id 접두사와 함께
대신하고, `uses` 노드는 사라집니다.

```
prepare → mlops.train → mlops.evaluate → report
```

이들은 접을 수 있는 하나의 group으로 도착하므로, 그래프는 파일에 적힌 내용을 그대로
보여 줍니다.

**run 시점에 실행되는 것이 아니라 publish 시점에 펼쳐집니다.** step이 하위 *run*을
트리거하고 기다리는 설계는 Airflow를 교착 상태에 빠뜨린 설계이며, 이 엔진에도 같은
요인이 있습니다. pod 상한은 프로세스별 세마포어이므로, 부모가 슬롯을 쥔 채 같은 풀의
슬롯이 필요한 하위 run을 기다리면 멈춰 버립니다. 그것도 부하가 걸릴 때, 다시 말해
운영 환경에서만 발생합니다. run 하나, 그래프 하나, 풀 하나입니다.

대신 포기하는 것은 고유한 id와 고유한 이력을 가진 하위 run입니다.

### 규칙

| | |
|---|---|
| 하위 workflow는 **같은 publish**에 있어야 합니다 | 단순히 이미 게시된 것이어서는 안 됩니다 |
| 화살표 | step으로 들어오는 화살표는 하위 workflow의 각 **루트**로 들어가는 화살표가 되고, 나가는 화살표는 각 **리프**에서 나갑니다 |
| 하위 workflow의 `image`, `env`, `secrets`, `resources` | 각 step에 구체화되어, 자신의 파일에 적힌 환경에서 실행됩니다 |
| 하위 workflow의 `unless_empty` / `for_each` 키 | 접두사와 함께 이동합니다 |
| 중첩 | 깊이 우선으로 평탄화됩니다 |
| 순환 | 체인을 명시하며 publish 시점에 거부됩니다 |

**"이미 게시된 것"이 아니라 같은 publish여야 한다는 점이 중요합니다.** 데이터베이스를
읽는 확장은 데이터베이스를 건드리지 않도록 의도된 `brevis validate`가
`brevis publish`와 다른 질문에 답하게 만들고, CI를 통과한 파일이 배포에서 실패하는
파일이 됩니다.

하위 workflow는 여전히 단독으로 publish할 수 있습니다. 확장은 **복사**하며 소비하지
않습니다. step은 `uses:`와 `run:`, `action:`, `marker:`를 함께 선언할 수 없습니다.
파일이 두 가지를 말하는 셈이기 때문입니다.

## 화살표의 의미 표시하기

의존성에는 레이블을 붙일 수 있으며, 그래프의 화살표에 표시됩니다.

```yaml
steps:
  - id: determine_load_type
    run: ./decide.sh

  - id: load_full
    run: ./full.sh
    depends_on:
      - {step: determine_load_type, label: additional data}

  - id: load_delta
    run: ./delta.sh
    depends_on:
      - step: determine_load_type
        label: changed existing data
```

`depends_on: [extract]`처럼 레이블 없는 형식도 계속 동작하며 일반적인 형식으로
남습니다. 대부분의 의존성은 할 말이 없고, 모든 화살표에 레이블이 붙으면 잡음이
됩니다.

레이블이 제 역할을 하는 곳은 **분기**입니다. 같은 step에서 나가는 화살표 두 개에 아무
것도 적혀 있지 않으면 소스를 열어야 읽을 수 있는 다이어그램이 되는데, 그래프는 바로
그것을 피하려고 존재합니다.

## 아무것도 하지 않는 step

```yaml
steps:
  - id: start
    marker: true

  - id: end
    marker: true
    depends_on: [load_full, load_delta, report]
```

`marker: true`는 명령이 없는 step입니다. 아무것도 실행하지 않고 즉시 성공하며, 그래프에
나타납니다.

장식이 아닙니다. 모든 분기에 의존하는 `end`는 *전체가 끝났는가?*를 따라가야 할 화살표
여섯 개가 아니라 노드 하나로 바꿔 줍니다. 그리고 다른 step과 똑같이 동작하므로,
실패한 분기 아래의 `end`는 초록색이 아니라 **skipped**가 됩니다.

**`run:`도 `marker: true`도 없는 step은 여전히 거부됩니다.** 두 경우를 하나로 합치면
안 됩니다. 빈 명령은 거의 항상 실수이고, `marker: true`는 의도했다는 표시이기
때문입니다. `run:`까지 선언한 marker도 거부됩니다. 파일이 두 가지를 말하는 셈이기
때문입니다.

## 무언가 실패했을 때만 step 실행하기

기본적으로 step은 앞선 모든 것이 성공했을 때 실행됩니다. `when:`이 이를 바꿉니다.

```yaml
steps:
  - id: extract
    run: python fetch.py

  - id: notify_failure
    run: ./notify.sh
    depends_on: [extract]
    when: any_failed          # runs precisely when extract did not make it

  - id: cleanup
    run: ./cleanup.sh
    depends_on: [extract, transform]
    when: all_done            # runs either way
```

| 규칙 | |
|---|---|
| `all_success` | 기본값입니다. 이 step이 의존하는 모든 step이 성공했을 때이며, Airflow처럼 **자신의** 의존성만 봅니다 |
| `any_failed` | 이 step이 의존하는 step 중 하나 이상이 실패했을 때 |
| `all_done` | 이 step이 의존하는 모든 step이 어떤 결과로든 끝났을 때 |

알 수 없는 규칙은 publish 시점에 거부되며 유효한 값이 안내됩니다. 그래야만 합니다.
`when: on_failure`가 "기본값"으로 읽히면 이름과 정반대로 **성공** 시에 실행될 것이고,
그 사실은 가장 중요했던 밤에 발견됩니다.

### 실행되지 않은 step은 `skipped`입니다

별도의 상태이며 고유한 색으로 그려지고 사유를 함께 가집니다. *`extract`가 failed
상태였음*과 같은 식입니다. 이 상태가 생기기 전에는 실패 아래의 step에 기록이 전혀
없었고, 화면에는 영원히 pending으로 표시되었습니다.

`skipped`는 실패도 성공도 아닙니다. 상위 step이 깨진 step은 실패한 것이 아니라 기회를
얻지 못한 것이며, 이를 성공이라 부르면 run 자체의 상태까지 번지는 거짓말이 됩니다.

트리거 규칙은 어떤 step이 **실행되는지**를 결정할 뿐 run의 결과를 결정하지 않습니다.
메시지를 전달한 `notify_failure`가 있다고 해서 파이프라인이 정상 동작했다는 뜻은
아니며, run은 여전히 실패입니다.

### `all_success`는 Airflow와 마찬가지로 로컬입니다

이 규칙은 step **자신의** 의존성만 확인하고 그 외에는 아무것도 보지 않습니다. 관련
없는 분기의 실패는 이 분기를 멈추지 않습니다.

```
extract_orders ──✕                 (failed)
extract_users  ──✓── transform_users ──✓     keeps going
```

실패 **아래**의 분기는 여전히 멈춥니다. `all_success`가 로컬이라고 해서 그 효과가
없다는 뜻은 아닙니다.

이 엔진은 예전에 첫 실패에서 그래프 전체를 중단했습니다. 이유가 있었습니다. 오류 뒤에도
계속 진행하면 완전해 보이는 부분 결과가 만들어졌고, 한 파이프라인이 아무도 모르는 채
28일 늦게 실행된 일이 있었기 때문입니다.

**그 보호를 대신하는 것**은 결코 작지 않습니다.

- run은 같은 오류와 함께 여전히 **실패**합니다.
- 그래프는 실패한 step을 빨간색으로, 건너뛴 step을 각각 고유한 색으로 표시하며, 각
  step은 어떤 step이 자신을 멈췄는지 알려 줍니다.
- run이 포기하면 알림은 여전히 발송됩니다.

run이 완전하지 않다고 스스로 밝히므로, 부분 결과가 더 이상 완전해 보이지 않습니다.

**포기하는 것**은 솔직히 말해 이렇습니다. 관련 없는 분기가 이제는 다른 곳에서 실패한
run에서도 데이터를 씁니다.

### 재시도는 실패한 것만 다시 실행합니다

run의 재시도는 그래프 전체를 다시 실행하지 않습니다. **같은 run**의 이전 시도에서 이미
성공한 step은 결과를 유지하며 다시 실행되지 않습니다. Airflow에서 DAG run을 clear했을
때의 동작과 같습니다.

```
attempt 1   extract ✓    load ✕    report (skipped)
attempt 2   extract –    load ✓    report ✓            extract is not re-run
```

해당 step은 원래의 행을 유지합니다. 소요 시간, 로그, 게시한 값은 작업이 실제로 일어난
시점인 첫 번째 시도의 것입니다. **그 아래의 step은 여전히 게시된 값을 읽습니다.** run의
컨텍스트는 다시 만들어지지 않고 저장되어 있기 때문입니다.

중요한 구분이 있습니다. 이것은 **이 run의** 이전 시도에 관한 이야기입니다. *어제* run에서
성공한 step은 오늘 정상적으로 실행됩니다. (그 다른 질문도 존재하며, SDK에게 step이 처음
실행되는 것인지 알려 주는 것이 그것입니다. `BREVIS_RUN_FIRST`를 참고하세요.)

**skipped**였던 step은 확정된 것이 아닙니다. 실행된 적이 없으므로, 재시도는 새 시도의
결과를 가지고 그 step을 다시 판단합니다.

## step의 실패 알리기

모든 실패는 이미 알림이 갑니다. 웹훅이 구성되어 있으면 포기한 run이 메시지 하나를
보내며, 어떤 파일에도 블록을 반복해서 쓸 필요가 없습니다. `on_error`는 자기만의 알림이
필요한 step을 위한 것입니다.

```yaml
steps:
  - id: fetch_observations
    run: python fetch.py
    on_error:
      type: SLACK
```

| 필드 | | |
|---|---|---|
| `type` | **필수** | `SLACK`. 알 수 없는 값은 publish 시점에 거부되며 유효한 값이 안내됩니다 |
| `when` | `give_up` | `attempt`는 마지막 시도뿐 아니라 실패한 모든 시도를 알립니다 |

메시지는 하나가 아니라 두 개가 도착하며, 이것이 요점입니다. run 수준의 알림은
*`id_verification` 실패*라고 알리는데, 파이프라인 담당자에게 필요한 정보입니다. step
수준의 알림은 *`fetch_observations` 실패*라고 알리는데, 해당 통합의 담당자에게 필요한
정보입니다.

**`webhook`이나 `url` 필드는 없으며, 앞으로도 추가되지 않습니다.** 목적지는
자격 증명입니다. 이를 가진 사람은 플랫폼인 것처럼 채널에 글을 올립니다. 그리고 workflow
파일은 회사의 알림이 어디로 갈지 선택할 권한이 반드시 있다고 할 수 없는 사람이
작성합니다. 파일은 **여부**와 **방식**을 말하고, **어디로**는 설치 환경이
`BREVIS_SLACK_WEBHOOK`을 통해 정합니다.

**`when: give_up`이 기본값인 것은 누구의 밤인지를 고려했기 때문입니다.** 두 번 실패하고
세 번째에 성공하는 step은 다른 기본값이라면 메시지를 두 개 보내고, 두 번째 메시지는 문제가
사라진 뒤에 도착합니다.

**실패하지 않은 step은 절대 알림을 보내지 않습니다.** 한 분기가 깨진 workflow에서 다른
분기의 담당자는 잠에서 깨지 않습니다.

전달은 `brevis alert`의 역할입니다. 알림은 실패와 같은 트랜잭션에 기록되므로, Slack 장애는
알림을 잃게 하지 않고 지연시킬 뿐입니다.

## 태그

```yaml
tags: [analytics, dbt, daily]
```

인터페이스에서 필터링에 쓰입니다. 실행에는 영향을 주지 않습니다.

## 검증

검증에는 데이터베이스가 필요 없으므로 CI에서 테스트와 함께 실행할 수 있습니다.

```bash
brevis validate workflows/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencias  cron 0 2 * * *
```

파일 또는 디렉터리를 받습니다. 디렉터리에서는 `*.y*ml`에 일치하는 파일을 정렬해서
처리하므로, 두 번 실행해도 로그가 같고 두 배포 사이의 차이가 잡음이 되지 않습니다.

## 게시

```bash
brevis publish workflows/
brevis publish workflows/ --project acme --prune
```

`--prune`은 게시 목록에 없는 workflow를 이력은 보존한 채 프로젝트에서 제거합니다.
실용적인 이유로 이것은 기본값이 **아닙니다**. `publish one-file.yaml`이 명령줄에 이름이
없다는 이유만으로 나머지 마흔여덟 개를 삭제해서는 안 되기 때문입니다.

## 다음 단계

- [매개변수](/docs/parameters/) — 두 run 사이에 달라지는 것
- [스케줄러와 큐](/docs/scheduler-and-queue/) — workflow가 실행이 되는 과정
- [step 간 컨텍스트](/docs/context/) — 한 step이 다음 step에 알려 주는 것
