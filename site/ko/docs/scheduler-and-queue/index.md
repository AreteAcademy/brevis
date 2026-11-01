# 스케줄러와 큐

> 서로 독립된 두 루프입니다. 누가 run을 만들고, 누가 실행하는지, 그리고 왜 분리되어 있는지 설명합니다.

*https://brevis.sh/ko/docs/scheduler-and-queue/ · brevis.sh docs (ko)*

---

brevis.sh는 run을 **만드는 일**과 **실행하는 일**을 분리합니다. 두 루프가 함께
돌아가지만 서로에게 의존하지 않습니다.

```
scheduler                          queue
─────────                          ─────
reads the schedule
materialises the slot
creates the Run  ─────────────►  (pending in the database)
                                   claims it
                                   walks the graph
                                   executes each step
                                   records state and log
```

## 분리하는 이유

스케줄링과 실행을 모두 맡는 단일 루프에는 나쁜 성질이 하나 있습니다. 실행이 막히면
스케줄링도 함께 멈춘다는 것입니다. 이후 열두 시간 분량의 slot은 그냥 존재하지 않게
되고, 도착하지 않은 데이터를 누군가 찾으러 갈 때까지 아무도 알아채지 못합니다.

둘을 분리하면 다음과 같습니다.

- **스케줄러**가 멈춰도 큐는 이미 만들어진 것을 계속 처리합니다.
- **큐**가 멈춰도 slot은 계속 구체화되고, 큐가 돌아오면 실행됩니다.
- **재처리는 시계에 의존하지 않습니다.** `backfill`은 cron이 사용했을 경로와 같은
  경로로 과거 run을 만듭니다.

## 프로세스

두 루프는 같은 명령 안에 있습니다.

```bash
brevis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| 플래그 | 기본값 | |
|---|---|---|
| `--interval` | `10s` | 스케줄러 주기 사이의 간격 |
| `--concurrency` | `5` | 동시에 실행되는 **run** 수 |
| `--max-pods` | `5` | 동시에 실행되는 **step**의 전체 수 |

`--concurrency`와 `--max-pods`는 의도적으로 서로 다른 것을 셉니다. 한도가 run에만
있다면, step 세 개를 병렬로 돌리는 run 다섯 개는 pod 열다섯 개가 됩니다.

:::note `serve`는 스케줄을 구체화하지 않습니다
API 프로세스도 같은 스케줄러를 띄우지만 **루프는 돌리지 않습니다**. 인터페이스의 수동
트리거를 처리하기 위한 것일 뿐이며, 덕분에 "스케줄러가 run을 만든다"는 규칙은 소유자가
하나로 유지됩니다. 스케줄이 실행되려면 `brevis scheduler`가 함께 실행되고 있어야
합니다.
:::

## 정의 스냅샷

스케줄러는 run을 만들 때 **workflow 정의를 run 안에 저장**합니다. 실행은 디스크의
YAML이 아니라 이 스냅샷을 읽습니다.

이유는 구체적입니다. 트리거와 실행 사이에 누군가 파일을 수정하고 배포했을 수
있습니다. 스냅샷이 없으면 05:00에 한 정의로 만들어진 run이 05:02에 다른 정의로
실행되고, 로그에는 그 차이를 설명할 방법이 없습니다.

## 재시도

실패한 run은 **세 번** 시도되며 지연은 **두 배씩** 늘어납니다. 기본값에서는 **0초, 30초,
1분 30초**에 시도됩니다.

```bash
brevis scheduler --max-attempts 3 --retry-backoff 30s --retry-backoff-max 1h
```

| 플래그 | 기본값 | |
|---|---|---|
| `--max-attempts` | `3` | run당 시도 횟수(첫 시도 포함) |
| `--retry-backoff` | `30s` | 첫 번째 지연. 이후 시도마다 두 배가 됩니다 |
| `--retry-backoff-max` | `1h` | 지연의 상한 |

프로세스는 부팅 시 일정을 출력하므로, 두 숫자에서 직접 계산할 필요가 없습니다.

```
scheduler and dispatcher are up  max_attempts=3 retry_backoff=30s retry_at="0s, 30s, 1m30s"
```

:::tip
**요청 제한이 걸린 벤더는 더 긴 지연을 원합니다.** 이런 파이프라인은 무엇보다 한 가지
이유로 실패합니다. 일시적인 업스트림 문제, 즉 "요청 한도에 도달했습니다"라는 내용으로
200을 응답하는 API나 웨어하우스 쿼터의 일시적 변동입니다. 해소되는 데 1분쯤 걸리는데,
그 전에 끝나는 재시도는 하지 않은 것과 같습니다. `--retry-backoff 1m`은 시도를 0초, 1분,
3분에 배치합니다.
:::

재시도는 **run** 단위이며, 실패한 step만 다시 실행합니다. 실패한 `dbt_build` 때문에 그
위에서 성공한 fetch가 다시 실행되지는 않습니다.

시도 횟수는 프로세스 상태가 아니라 영속화된 값입니다. worker를 재시작해도 해당 run에
대해 이미 알려진 사실이 지워지지 않습니다.

**run의** 시도 번호는 pod 이름에 들어갑니다. 그렇지 않으면 재시도가 run을 처음부터 다시
시작하면서 같은 이름의 이전 시도 pod와 만나 `Pending`에서 영원히 멈추게 됩니다.

## Backfill

이미 게시된 workflow의 과거 slot을 구체화합니다.

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

```
  31 run(s) de backfill enfileirados para daily (2026-01-01 a 2026-01-31)
  rode `brevis scheduler` para executa-los
```

이 명령은 **큐에 넣을 뿐 실행하지 않습니다.** 실행은 `scheduler`의 일이며, 같은 분리
원칙입니다. `--to`는 그날 하루 전체를 포함합니다.

## 랜딩으로 시작하기

workflow는 시계가 그렇게 말해서가 아니라 데이터가 도착했기 때문에 실행될 수 있습니다.

```yaml
trigger:
  on_landed:
    - bigquery://acme-prod/bronze/orders
    - bigquery://acme-prod/bronze/*
  debounce: 5m
```

모든 step은 이미 자신이 쓴 것을 단계를 전달하는 파이프를 통해 선언하며, `/data`를
채우는 것이 바로 그 정보입니다. workflow는 그 이름 중 하나를 구독하고, 그 이름에
무언가 도착하면 시작됩니다.

끝에 `/*`를 붙이면 데이터셋 전체를 구독합니다. 이는 경로마다 테이블을 만드는
`auto_table`을 위해 존재합니다. 아무도 선언하지 않은 테이블에 대해 이름으로 지정할 수
있는 것은 데이터셋뿐이기 때문입니다. `bronze/orders`에는 일치하고
`bronze_raw/orders`에는 일치하지 않습니다.

### 시간 창, 그리고 랜딩 열 건이 run 하나인 이유

`debounce`는 몰려드는 랜딩을 하나로 합칩니다. 테이블 열 개가 5분 안에 도착하면 run이
열 개가 아니라 **하나** 시작되며, run은 어떤 테이블인지 알려 줍니다.

```
BREVIS_AUTO_LANDED=bigquery://acme-prod/bronze/orders,bigquery://acme-prod/bronze/items
```

정확한 목록은 JSON으로 `BREVIS_AUTO_PARAMS`에도 들어 있으며, 대상 이름에 쉼표가 들어
있을 수 있을 때 읽어야 하는 것은 이쪽입니다.

시간 창은 step이 일어났다고 말한 시각이 아니라 **엔진이 랜딩을 기록한 시각**을 기준으로
잘립니다. 쿼리가 시작된 순간을 보고하는 step은 사실이지만 오래된 정보를 보고하는
것이며, 시계 하나가 어긋나면 몰려든 랜딩이 두 run으로 갈라질 수 있습니다.

`debounce`를 생략하면 랜딩마다 run이 시작됩니다.

### 하지 않는 것

**workflow는 자신의 쓰기로 시작되지 않습니다.** 엔진은 작성자가 구독 중인
workflow 자신인 랜딩을 버리고 그 이유를 로그에 남깁니다. 그렇지 않으면 같은 테이블을
읽고 쓰는 workflow가 영원히 스스로를 트리거합니다.

이것은 파일을 검사할 때가 아니라 **트리거가 발생할 때** 거부되며, 이는 간과가 아니라
한계입니다. `brevis validate`는 파일을 읽는데, 어떤 파일도 step이 무엇을 쓰는지
선언하지 않습니다. 웨어하우스 대상은 YAML이 아니라 연결이 결정합니다. 그래서
`validate`는 런타임에 거부될 workflow를 매번 통과시키며, 이유는 로그에 남습니다.

**첫 주기에는 아무것도 시작되지 않습니다.** 트리거는 스케줄처럼 활성화된 순간부터
카운트를 시작합니다. 그렇지 않으면 트리거를 게시하는 순간 카탈로그 이력의 모든 랜딩에
대해 run이 발생할 것입니다.

## 알림

```bash
export BREVIS_SLACK_WEBHOOK='https://hooks.slack.com/services/...'
export BREVIS_UI_URL='https://brevis.example.com'
```

웹훅이 없으면 프로세스는 부팅 시 실패가 보고되지 않을 것이라고 경고합니다. 조용히
실패하는 설치 환경은 팀이 아니라 고객이 먼저 발견하기 쉽습니다.

`BREVIS_UI_URL`은 알림 안의 run 링크를 만듭니다. 이 변수가 없으면 알림은 무엇이
실패했는지는 알려 주지만 읽는 사람이 run을 직접 찾아야 합니다.

## 다음 단계

- [step당 pod 하나](/ko/docs/pod-per-step/index.md) — 각 step이 실제로 실행되는 곳
- [CLI](/ko/docs/cli/index.md) — `scheduler`와 `backfill`의 모든 플래그
