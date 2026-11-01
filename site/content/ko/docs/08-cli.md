---
title: CLI
description: 엔진의 하위 명령 열두 개와 각각의 플래그, 인자, 요구 사항입니다.
group: 레퍼런스
order: 8
slug: cli
---

엔진은 여러 바이너리가 아니라 하위 명령을 가진 바이너리 하나입니다. API, 스케줄러,
worker는 같은 시스템의 역할이며, 바이너리가 하나이면 버전, 이미지, 빌드 경로도
하나로 유지됩니다.

```
brevis [command] [flags]
```

| 명령 | 데이터베이스 | `local` 외 환경의 자격 증명 | 역할 |
|---|---|---|---|
| [`serve`](#serve) | **필요** | **필수** | API + 인터페이스 |
| [`scheduler`](#scheduler) | **필요** | **필수** | 두 루프 모두 |
| [`migrate`](#migrate) | **필요** | **필수** | 스키마 |
| [`publish`](#publish) | **필요** | **필수** | workflow와 스케줄 저장 |
| [`backfill`](#backfill) | **필요** | **필수** | 구간 재처리 |
| [`cancel`](#cancel) | **필요** | **필수** | run 중지 |
| [`run`](#run) | 불필요 | — | 인스턴스 자체에서 즉시 실행 |
| [`validate`](#validate) | 불필요 | — | workflow YAML 검증 |
| [`brand`](#brand) | 불필요 | — | 브랜딩 YAML 검증 |
| [`hash`](#hash) | 불필요 | — | 비밀번호 해시 생성 |
| [`version`](#version) | 불필요 | — | 버전, 커밋, 빌드 |

앞의 여섯 개는 구성을 로드하며, `BREVIS_DATABASE_URL`이 없으면 **부팅 시 실패**합니다.
나머지 다섯 개는 그렇지 않으며, 그래서 Postgres가 없는 CI에서도 `validate`가 동작합니다.

같은 여섯 개는 자격 증명 규칙도 따릅니다. `BREVIS_ENV`가 `local`이 아닌 경우 인증
변수 세 개 없이 시작하면 **부팅 오류**입니다.

---

## serve

HTTP API와 인터페이스를 띄웁니다. 플래그는 없으며 모든 것이 환경에서 옵니다.

```bash
brevis serve
```

`SIGINT`/`SIGTERM`에서 정상 종료하므로, 배포가 처리 중인 요청을 끊지 않습니다. 이
프로세스는 **스케줄을 구체화하지 않습니다**. 그러려면 `brevis scheduler`가 함께
실행되어야 합니다.

`api` 이미지(distroless, 셸 없음)의 `CMD`입니다.

---

## scheduler

두 루프를 모두 실행합니다. 스케줄러는 스케줄을 run으로 구체화하고, 디스패처는 큐에서
가져와 실행합니다.

```bash
brevis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| 플래그 | 타입 | 기본값 | |
|---|---|---|---|
| `--interval` | duration | `10s` | 주기 사이의 간격 |
| `--concurrency` | int | `5` | 동시에 실행되는 **run** 수 |
| `--max-pods` | int | `5` | 동시에 실행되는 **step**의 전체 수 |

`worker` 이미지(`run:` step에 셸이 필요하므로 셸이 있는 alpine)의 `CMD`입니다.

---

## alert

스케줄러가 기록한 알림을 전달합니다. `serve`, `scheduler`와 나란히 동작하는 세 번째
프로세스입니다.

```bash
brevis alert --interval 1s --max-attempts 6
```

| 플래그 | 타입 | 기본값 | |
|---|---|---|---|
| `--interval` | duration | `1s` | 전달 주기 사이의 간격 |
| `--max-attempts` | int | `6` | 알림을 미전달로 기록하기 전까지의 시도 횟수 |

**이 프로세스가 존재하는 이유는 알림이 기록되는 위치 때문이지, 전달이 별도 프로세스를
가질 만해서가 아닙니다.** 스케줄러는 알림을 그 근거가 되는 실패와 같은 트랜잭션에
기록합니다. run의 시도 횟수가 소진되어 알림이 존재하거나, 둘 다 일어나지 않았거나
둘 중 하나입니다. 그 뒤에는 누군가 그 테이블을 비워야 하고, 그 주체는 Slack 장애와 자기
자신의 재시작을 모두 견뎌야 합니다. 이 프로세스가 생기기 전에는 웹훅이 실패하면 로그
한 줄만 남고 알림은 그냥 사라졌습니다.

채널(`BREVIS_SLACK_WEBHOOK`)이 구성되지 않으면 시작을 거부합니다. 전달할 곳이 없는 전달
프로세스는 아웃박스가 채워지는 속도만큼 빠르게 "미전달"로 비워 버리는데, 그 행은 Slack이
거부한 것과 똑같아 보입니다.

포기한 알림은 **보존됩니다**. "발생, 미전달, 4회 시도, Slack에서 403"이 중요한 행입니다.
누군가 오지 않을 메시지를 기다리고 있는 경우이기 때문입니다.

`worker` 이미지의 `CMD`입니다.

---

## report

실행된 작업의 주기적 요약을 보냅니다.

```bash
brevis report --window 168h              # sends it
brevis report --window 168h --dry-run    # prints it, sends nothing
```

| 플래그 | 타입 | 기본값 | |
|---|---|---|---|
| `--window` | duration | `168h` | 얼마나 거슬러 볼지 |
| `--dry-run` | bool | `false` | 보내지 않고 출력 |

루프가 아니라 **명령**이며, 이것이 리포트와 알림의 차이입니다. CronJob에서 실행되므로
새 루프도, 새 상태도, 리더 선출도 없습니다. 클러스터에는 이미 스케줄러가 있기
때문입니다. `--dry-run`은 주간 메시지가 말했을 내용을 읽어 보면서 그 메시지를 신뢰하게
되는 방법입니다.

알림 아웃박스를 거치지 **않습니다**. 둘은 전달 채널만 공유하고 그 외에는 아무것도
공유하지 않습니다. 알림을 잃는 것은 아무도 모르는 장애이고, 한 주의 요약을 놓치는
것은 다음 주의 요약이 대신합니다.

메시지에는 run 수, 성공, 실패, 가장 많이 실패한 파이프라인, 가장 오래 걸린 파이프라인,
SDK가 보고한 행 수와 바이트 수가 담깁니다. CPU나 메모리는 담지 **않으며** 0으로
출력하지도 않습니다. 엔진은 이를 수집하지 않으며, 메시지는 그 값이 실제로 어디에 있는지
알려 줍니다.

비어 있는 구간은 100%라고 보고하지 않고 비어 있다고 말합니다. 아무것도 없는 상태에서
나온 성공률은 리포트가 출력할 수 있는 가장 안심되는 숫자이면서 가장 사실과 먼
숫자입니다. 빈 한 주는 대개 스케줄러가 멈춰 있었다는 뜻입니다.

`deployments/kubernetes/report.yaml`이 매주 실행합니다.

---

## migrate

```bash
brevis migrate up      # applies what is missing
brevis migrate down    # reverts the last one
brevis migrate status  # shows the state
```

인자를 정확히 하나 받습니다. 마이그레이션은 바이너리에 내장되어 있고 별도의 하위
명령으로 적용됩니다. **`serve`는 스키마를 절대 변경하지 않습니다**.

---

## validate

workflow 파일을 하나 이상 검증합니다. 파일 **또는 디렉터리**를 받습니다.

```bash
brevis validate examples/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

데이터베이스를 건드리지 않고 서버도 시작하지 않으므로 에디터와 CI에서 실행됩니다. 0이
아닌 코드로 종료하며 실패 건수를 셉니다.

---

## run

workflow를 **인스턴스 자체에서 즉시** 실행합니다. 큐도, 데이터베이스도, 스케줄러도
없습니다.

```bash
brevis run examples/hello.yaml
brevis run wf.yaml --param load_full=true --retries 3 --timeout 5m
```

| 플래그 | 타입 | 기본값 | |
|---|---|---|---|
| `--param` | 반복 가능 | — | 선언된 매개변수에 대한 `key=value` |
| `--workdir` | string | 파일이 있는 디렉터리 | step의 작업 디렉터리 |
| `--retries` | int | `1` | step당 시도 횟수(`1` = 재시도 없음) |
| `--timeout` | duration | `0` | step당 타임아웃(`0` = 제한 없음) |

`=` 없는 `--param`은 경고가 아니라 **오류**입니다. 값을 빠뜨린 `--param load_full`이
기본값으로 실행되면 운영자는 값이 적용되었다고 믿게 될 것입니다.

:::warning 네 가지 제약
`run`은 `BREVIS_ENV=local`에서만 동작합니다. `image:`가 있는 step은 인스턴스 자체에서
실행되며 그렇다고 알려 줍니다. Go 작업 레지스트리가 없으므로 등록되지 않은 작업에 대한
`action:`은 사용 가능한 작업을 명시하며 실패합니다. 그리고 `run`은 `BREVIS_HOSTS`를
읽지 않으므로 `host:`가 있는 step은 여기서 전혀 실행할 수 없습니다. 변수를 설정해도
host를 명시하며 실패합니다.
:::

---

## publish

workflow와 그 스케줄을 데이터베이스에 저장합니다. 파일 또는 디렉터리를 받습니다.

```bash
brevis publish examples/hello.yaml
brevis publish workflows/ --project acme --prune
```

| 플래그 | 타입 | 기본값 | |
|---|---|---|---|
| `--project` | string | `default` | 프로젝트 slug |
| `--prune` | bool | `false` | 목록에 없는 workflow를 프로젝트에서 제거 |

`--prune`을 쓰더라도 제거된 workflow의 이력은 보존됩니다.

---

## backfill

이미 게시된 workflow의 과거 slot을 구체화합니다. **큐에 넣을 뿐 실행하지 않습니다.**

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

| 플래그 | | |
|---|---|---|
| `--from` | **필수** | 시작 날짜, `YYYY-MM-DD` |
| `--to` | **필수** | 종료 날짜, `YYYY-MM-DD` |
| `--param` | | 구간 내 **모든** slot에 적용됩니다 |

`--to`는 그날 하루 전체를 포함합니다. 내부적으로 종료 시각이 해당 날짜의 `23:59:59`가
됩니다.

---

## cancel

아직 대기 중이거나 실행 중인 run을 취소합니다. 이벤트 피드에는 취소한 주체로 `cli`가
표시됩니다.

```bash
brevis cancel 3f1c9a52-6d0e-4b7a-9c41-2b8e5d7f0a13
```

| 응답 | 일어난 일 |
|---|---|
| `canceled` | run이 큐에서 대기 중이었고, 큐를 떠났으며 실행되지 않습니다 |
| `cancel requested` | run이 실행 중이며, 디스패처가 `--cancel-poll`(2초) 안에 step을 중지합니다 |
| `already ended: <status>` | run이 이미 끝났으며 아무것도 바뀌지 않습니다 |

취소된 run은 재시도되지 않습니다. 존재하지 않는 id는 오류입니다.

---

## gateway

gateway가 `/data`에 나타나는 방식입니다. gateway의 구성이 의도적인 행위로 게시되며,
트래픽은 절대 게시되지 않고 런타임에도 게시되지 않습니다.

```bash
gateway describe gateway.yaml > manifest.json   # the gateway's own binary
brevis gateway publish manifest.json
brevis gateway unpublish events_gateway
```

`publish`는 아무것도 쓰기 전에 manifest 전체를 검사하여 첫 번째 문제의 stream과
역할을 명시하고, 해당 gateway의 목적지를 한 트랜잭션으로 교체합니다. `unpublish`는
폐기된 gateway를 제거하며, 그 이름으로 게시된 gateway가 없으면 오류입니다.

`brevis-sql-api`가 Data → Endpoints에 나타나는 방식입니다. 각 엔드포인트가 받고 읽는
것이 의도적인 행위로 게시되며, 트래픽은 절대 게시되지 않습니다.

```bash
brevis-sql-api describe --config api.yaml --out manifest.json   # the API's own binary
brevis endpoints publish manifest.json
brevis endpoints unpublish metrics-api
```

`publish`는 아무것도 쓰기 전에 manifest 전체를 검사하고 해당 API의 엔드포인트를 한
트랜잭션으로 교체합니다. 자격 증명처럼 보이는 이름, 기본값, 메모가 있으면 전체를
거부합니다.

`brevis publish`의 모드가 아니라 별도의 명령인 이유는 workflow와 gateway의 집합
의미론이 다르기 때문입니다. `publish --prune`이 gateway의 행에 닿는 일은 절대 있어서는
안 됩니다.

---

## prune

보존 정책을 적용합니다. Brevis에서 무언가를 삭제하는 **유일한** 기능이며, 스스로
실행되지 않고 운영자가 스케줄링합니다.

```bash
brevis prune --dry-run          # what would go, changing nothing
brevis prune
brevis prune --purge-after 0    # trim only, never delete a run
```

| 플래그 | 기본값 | |
|---|---|---|
| `--trim-after` | `720h` | 로그, 단계, 게시된 출력을 비웁니다 |
| `--purge-after` | `8760h` | run을 완전히 삭제합니다. `0`이면 삭제하지 않습니다 |

---

## brand

서버를 시작하지 않고 브랜딩 파일을 검증합니다.

```bash
brevis brand brand.yaml
```

```
  ok    Brevis · Orchestration
        logo      /assets/logo.svg  (built-in symbol)
        accent    (the product's own)
        Powered by Brevis
```

부팅 시와 동작이 다른 점이 하나 있습니다. **파일이 없으면 오류입니다.** `serve`에서는
파일이 없다는 것이 "기본 아이덴티티를 사용"한다는 뜻이지만, 경로를 검증해 달라고 요청한
사람은 그 경로가 없다는 사실을 알려 주기를 기대합니다.

---

## hash

`BREVIS_AUTH_PASSWORD_HASH` 값을 생성합니다.

```bash
brevis hash
```

비밀번호는 인자가 아니라 터미널에서 **에코 없이** 읽습니다. 인자는 어떤 프로세스의
`ps`에도 나타나고 셸 히스토리에 남기 때문입니다. 12자보다 짧은 비밀번호는 거부합니다.

해시만 **stdout**으로, 레이블은 **stderr**로 출력되므로 `brevis hash > hash.txt`는
중요한 것만 저장합니다.

---

## version

```bash
brevis version
```

```
brevis 0.11.2
  commit  bb832ff
  build   2026-09-05T12:00:00Z
  go      go1.25.7 darwin/arm64
```

`go build`로 직접 빌드하면 커밋과 날짜 없이 `brevis dev`를 출력합니다. 누군가 이상한
동작을 보고할 때 이것을 릴리스 산출물과 구별하는 일이 중요합니다.
