# 빠른 시작

> 아무것도 없는 상태에서 인터페이스의 실시간 DAG까지, 네 단계로 진행합니다.

*https://brevis.sh/ko/docs/quickstart/ · brevis.sh docs (ko)*

---

이 가이드는 brevis.sh를 띄우고, workflow를 게시하고, 실행합니다. 마지막에는 모든
step의 실시간 상태가 표시된 그래프를 볼 수 있습니다.

요구 사항: 접근 가능한 PostgreSQL과 `BREVIS_DATABASE_URL` 변수.

## 1. 스키마 적용

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
brevis migrate up
```

## 2. workflow 작성

`hello.yaml`을 만듭니다. 이 예제는 셸만 사용하므로 어디서든 실행됩니다.

```yaml
name: hello
type: dag
tags: [example]

steps:
  - id: prepare
    run: sh -c 'echo preparing; sleep 1'

  # The next two depend on the same step, so they run IN PARALLEL.
  - id: extract
    run: sh -c 'sleep 2; echo 42 extracted'
    depends_on: [prepare]

  - id: validate
    run: sh -c 'sleep 1; echo validated'
    depends_on: [prepare]

  - id: publish
    run: echo published
    depends_on: [extract, validate]
```

게시하기 전에 확인합니다. 이 명령은 데이터베이스를 건드리지 않습니다.

```bash
brevis validate hello.yaml
```

```text
  ok    hello                        dag  4 steps, 4 dependencies  (manual)
```

## 3. 큐 없이 바로 실행

그래프가 동작하는 모습을 가장 빨리 보는 방법입니다.

```bash
brevis run hello.yaml
```

```text
workflow hello (dag, 4 steps) in .
  ▶ prepare
    prepare | preparing
  ✓ prepare
  ▶ extract
  ▶ validate
    validate | validated
  ✓ validate
    extract | 42 extracted
  ✓ extract
  ▶ publish
    publish | published
  ✓ publish

workflow hello finished
```

`extract`와 `validate`는 같은 step에 의존하므로 함께 시작됩니다. 러너는 그래프를
**레벨 단위로** 진행합니다. 한 레벨 안의 모든 step은 병렬로 실행되고, 다음 레벨은
이전 레벨이 완전히 끝난 뒤에야 시작됩니다.

:::note `run`은 본질적으로 로컬입니다
`brevis run`은 `BREVIS_ENV=local`일 때만 동작하며(비어 있으면 local로 간주),
`image:`를 선언한 step까지 포함해 모든 step을 인스턴스 자체에서 프로세스로
실행합니다. pod는 `scheduler`가 만듭니다.
:::

## 4. 게시하고 운영하기

workflow가 데이터베이스에 존재하고, 인터페이스에 나타나며, 스케줄을 따르게 하려면
다음과 같이 합니다.

```bash
brevis publish hello.yaml
```

```text
  published  hello                      (manual)
```

터미널 두 개에서 인터페이스와 스케줄러를 띄웁니다.

```bash
brevis serve
```

```bash
brevis scheduler --interval 5s --concurrency 4
```

`http://localhost:8080`을 열고 workflow 목록에서 `hello`를 찾아 ▶를 클릭합니다.
그래프에서 모든 step의 상태가 실시간으로 바뀌는 것을 볼 수 있습니다.

## 무슨 일이 일어났나

| | |
|---|---|
| `migrate up` | 테이블을 만들었습니다 |
| `validate` | 데이터베이스를 건드리지 않고 YAML을 확인했습니다 |
| `run` | 큐 없이 인스턴스 자체에서 실행했습니다 |
| `publish` | workflow와 그 스케줄을 저장했습니다 |
| `serve` | API와 인터페이스를 띄웠습니다 |
| `scheduler` | 스케줄을 구체화하고 큐를 비웠습니다 |

## 다음 단계

- [Workflows](/ko/docs/workflows/index.md) — YAML 형식 전체
- [매개변수](/ko/docs/parameters/index.md) — 두 run 사이에 달라지는 것
- [step당 pod 하나](/ko/docs/pod-per-step/index.md) — 이것이 Kubernetes로 이어지는 방식
