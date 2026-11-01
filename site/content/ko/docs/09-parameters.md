---
title: 매개변수
description: 파일을 수정하지 않고도 같은 workflow의 두 run 사이에 달라지는 것입니다.
group: 레퍼런스
order: 9
slug: parameters
---

workflow는 **run 매개변수**를 선언할 수 있습니다. 파일은 그대로인 채 두 번의
트리거 사이에 달라지는 값입니다.

## 선언하기

```yaml
params:
  - name: load_full
    type: boolean
    default: "false"

  - name: start_date
    type: string
    pattern: '^\d{4}-\d{2}-\d{2}$'

steps:
  - id: run
    run: dbt build --vars '{"load_full":"{{ .load_full }}"}' --select bronze+
```

| 필드 | | |
|---|---|---|
| `name` | **필수** | 템플릿과 `--param`에서 사용하는 이름 |
| `type` | | `string`, `boolean`, `integer`, 또는 여러 값을 위한 `list|<type>` |
| `default` | | 트리거가 값을 제공하지 않을 때 사용됩니다 |
| `pattern` | | 값이 일치해야 하는 정규 표현식 |
| `title` | | 폼에 표시되는 레이블이며 최대 60자입니다. 없으면 이름이 표시됩니다 |
| `group` | | 폼에서 매개변수를 묶으며 최대 60자입니다 |
| `warn` | | 기본값이 아닌 값을 쓸 때의 비용으로, 필드 아래에 표시되며 최대 200자입니다 |
| `uses` | | 매개변수가 다루는 커넥터의 키(`bigquery`, `postgres`)이며, 로고 표시에 쓰입니다 |

**`default`가 없는** 매개변수는 필수입니다. 이를 생략한 트리거는 어떤 step도
실행하기 전에 실패합니다.

`title`, `group`, `warn`, `uses`는 폼이 매개변수를 보여 주는 방식이며, **어느 것도 값이
가질 수 있는 범위를 바꾸지 않습니다**. 각각 한 줄이어야 하고, `uses`는 소문자, 숫자,
`-`만 허용합니다. publish는 그 외의 값을 매개변수를 명시하며 거부합니다.

```yaml
params:
  - name: load_full
    type: boolean
    default: "false"
    title: Full load
    group: Load
    warn: reprocesses the whole table; it costs
    uses: bigquery
```

## 값의 목록

`list|<type>`은 여러 값을 담는 매개변수를 선언합니다. 적재할 테이블, 다시 빌드할
레이어, backfill할 구간 같은 것입니다.

```yaml
params:
  - name: tables
    type: list|string
    default: "users,orders"

  - name: layers
    type: list|string
    enum: [bronze, silver, gold]     # an enum restricts each ITEM
    default: "bronze"

  - name: days
    type: list|integer
    default: "1,7,30"
```

요소 타입은 `string`, `integer`, `boolean`이 가능합니다. 각 항목은 개별적으로 검증되므로
`list|integer`에 `1,x,30`을 주면 전체 값이 아니라 **2번째 항목**을 명시하며 실패합니다.

어디서나 **쉼표로 구분된 문자열 하나**입니다. 데이터베이스에서도, `BREVIS_RUN_PARAMS`에서도,
명령에서도 마찬가지입니다. 그래서 step에서 특별한 처리 없이 그대로 동작합니다.

```yaml
run: dbt build --select {{ .tables }}      # dbt build --select users,orders
```

그리고 그 때문에 항목 안에는 쉼표가 들어갈 수 없습니다. 빈 항목과 중복된 항목도
거부됩니다. 중복은 step이 항목마다 하는 일을 조용히 두 배로 만들기 때문입니다.

### step에서 배열로 사용하기

```go
for _, table := range p.Run.ParamList("tables") {
    load(table)
}
```

```python
from brevis import run

for table in run.param_list("tables"):
    load(table)
```

두 방식 모두 아무도 채우지 않은 매개변수에 대해 빈 문자열 하나가 든 목록이 아니라
`[]`를 반환합니다. "할 일 없음"을 순회하면 아무 일도 일어나지 않아야 하기
때문입니다. 항목은 `list|integer`인 경우에도 문자열로 반환됩니다. 숫자로 무엇을 할지는
라이브러리보다 step이 더 잘 알기 때문입니다.

### 인터페이스에서

`enum`이 있는 목록은 체크박스로 렌더링되며, JavaScript가 필요 없고 브라우저가 체크한
상자마다 값 하나씩 제출합니다. `enum`이 없으면 항목이 제거 가능한 칩이 되는 텍스트
필드이고, JavaScript를 끄면 `users,orders`를 담은 필드로 남는데, 서버가 기대하는 형식이
정확히 그것입니다.

### 명령줄에서

```bash
brevis run wf.yaml --param tables=events,sessions --param days=1,30
```

:::note 이전 엔진은 이를 읽지 못합니다
타입은 workflow와 함께 저장되며, 이 기능 이전의 엔진은 publish 시점에 `list|string`을
알 수 없는 타입으로 거부합니다. 목록 매개변수를 쓰는 workflow를 게시하기 전에 엔진을
업그레이드하세요.
:::

## 명령에서 사용하기

값은 점이 붙은 이중 중괄호 템플릿을 통해 `run`에 들어갑니다.

```yaml
run: ./load.sh --since {{ .start_date }}
```

## 명령줄로 전달하기

```bash
brevis run wf.yaml --param load_full=true
brevis run wf.yaml --param load_full=true --param start_date=2026-01-01
```

`--param`은 반복해서 쓸 수 있습니다. `=`가 없는 입력은 **오류**입니다.

```
erro: --param "load_full": use chave=valor
```

의도된 동작입니다. 값을 빠뜨린 `--param load_full`이 기본값으로 실행되면 운영자는
backfill이 의도한 매개변수로 수행되었다고 믿게 됩니다.

## backfill에서

값은 구간 내 **모든** slot에 적용됩니다. 핵심 사용 사례입니다.
"1월 전체를 `load_full=true`로 재처리".

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

## 인터페이스에서

`params`가 있는 workflow는 단순한 트리거 버튼 대신 **폼**을 가집니다. 필드는 선언에서
만들어지며, `pattern`은 제출 전에 검증됩니다.

## step 환경에서

run 매개변수는 접두사가 붙은 채 각 step의 **환경**에도 들어갑니다. [SDK](/docs/sdk/)로
작성한 fetcher가 인자로 아무것도 받지 않고도 값을 볼 수 있게 하기 위한 것입니다.

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    if p.Run.Params["load_full"] == "true" {
        p.Source.URL += "&full=1"
    }
    return nil
},
```

## 자동 매개변수

모든 run은 **자동 매개변수**도 가집니다. 엔진이 스스로 계산하는 값입니다. 아무도
선언하지 않고, 모든 run에 존재하며, run 화면에 표시됩니다.

가장 중요한 것은 `adjusted_at`이며, 이 값이 없애 주는 버그는 짚고 넘어갈 가치가
있습니다. fetcher가 `now()`를 읽고 윈도우를 빼서 벤더에게 최근 두 시간을 요청합니다.
정시에 시작한 run에서는 맞습니다. 큐 때문에 40분 지연된 run에서는 40분이 틀리며, 그
40분은 **어떤 run에도** 속하지 않습니다. 다음 slot도 자기 `now()`를 읽기 때문입니다.
아무것도 실패하지 않고 데이터만 없으며, 몇 주 뒤에야 발견됩니다.

그래서 엔진이 시계를 대신 건네줍니다. `now()`를 읽던 자리에서 `adjusted_at`을
읽으세요.

| 매개변수 | | |
|---|---|---|
| `adjusted_at` | 항상 | 이 run이 읽어야 할 시계: slot이 있으면 slot, 없으면 시작 시각 |
| `date` | 항상 | UTC 기준 `YYYY-MM-DD` 형식의 `adjusted_at` — 파티션, 폴더, `WHERE` |
| `delay_seconds` | 항상 | 이번 시도가 slot보다 얼마나 늦었는지. slot이 없는 수동 run에서는 `0` |
| `previous_error` | 항상 | 직전 run이 성공하지 못했는지 |
| `scheduled_at` | slot이 있을 때 | cron이 요청한 slot |
| `started_at` | | 이번 시도가 시작된 시각. 재시도하면 달라집니다 |
| `interval_start` | slot이 있을 때 | **이전** slot |
| `interval_end` | slot이 있을 때 | 이번 slot, **미포함** |
| `previous_success_at` | | 마지막으로 성공한 run의 slot |

run이 slot을 가지는 경우는 스케줄이 만들었을 때, backfill이 만들었을 때, 또는
**과거 slot에 대해** 수동으로 트리거했을 때입니다. 트리거의 `slot` 필드
(`2026-10-27T04:00:00Z`)는 해당 slot을 그 slot의 매개변수로 다시 실행합니다. cron이 절대
발생시키지 않는 slot은 거부되며, 그 이전에서 가장 가까운 slot이 안내됩니다.

**여기의 모든 타임스탬프는 UTC**이며 `Z`가 붙은 RFC 3339로 기록되고, run 화면도 UTC로
표시하므로 화면과 `$BREVIS_AUTO_ADJUSTED_AT`이 서로 어긋나지 않습니다. `date`는 **UTC
기준 날짜**이므로, slot이 UTC 자정을 넘는 workflow에서는 현지 날짜와 다릅니다.
`America/Sao_Paulo`의 `0 22 * * *`는 다음 날 `date`를 가집니다. Airflow의 `ds`도 같은
이유로 같은 방식으로 동작합니다. slot은 시점이고, 시점은 타임존을 정한 뒤에야 날짜가
하나로 정해지기 때문입니다.

`interval_start`와 `interval_end`는 이력이 아니라 **cron**에서 계산됩니다. 3월 slot의
backfill은 이 workflow의 run들이 오늘 기술하는 윈도우가 아니라 3월에 있었던 윈도우를
만듭니다. `[start, end)`를 요청하는 파이프라인은 아무리 늦게 실행되고 아무리 자주
재시도해도 겹치거나 비는 구간이 없습니다.

이 값들은 run이 시작될 때 계산되어 run에 저장되는 **스냅샷**입니다. `previous_error`는
이 run이 시작된 순간에 관한 사실입니다. 내일 다시 계산하면, 즉 직전 run이 재시도되어
통과한 뒤에 계산하면 같은 이름으로 다른 질문에 답하게 됩니다.

### 읽는 방법

셸 step에서는 값마다 변수가 하나씩 있어서 파싱할 필요가 없습니다.

```yaml
steps:
  - id: extract
    run: |
      curl "https://api.example.com/events?since=$BREVIS_AUTO_INTERVAL_START&until=$BREVIS_AUTO_INTERVAL_END" \
        > "/data/$BREVIS_AUTO_DATE.json"
```

한 번만 파싱하고 싶은 step을 위해 전체 집합이 JSON 객체 하나로 `$BREVIS_AUTO_PARAMS`에도
들어 있습니다.

Go에서는 [SDK](/docs/sdk/)가 대신 읽어 줍니다.

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    start, end, ok := p.Run.Auto.Window()
    if !ok { // no schedule: fall back to a fixed window
        start, end = p.Run.Auto.Now().Add(-24*time.Hour), p.Run.Auto.Now()
    }
    p.Source.URL += "&from=" + start.Format(time.RFC3339) +
        "&to=" + end.Format(time.RFC3339)
    return nil
},
```

`Auto.Now()`는 `time.Now()` 대신 읽어야 하는 시계입니다. 엔진 밖에서, 예를 들어 누군가
손으로 실행한 fetcher에서는 *실제* 벽시계이므로 로컬 개발을 위해 특별히 처리할 것이
없습니다.

Python에서는 `pip install brevis`가 같은 값을 읽습니다.

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

`window()`는 workflow에 스케줄이 없으면 0 시각 쌍이 아니라 `None`을 반환합니다. 에포크부터의
쿼리는 모든 것을 선택하며, 그 실패는 조용해서는 안 되기 때문입니다. 위의 `or`가
폴백의 전부입니다.

### dlt에서는 읽을 것이 전혀 없습니다

[dlt](https://dlthub.com)는 목적지에 기록하는 상태에 자기만의 커서를 보관합니다. 그
상태는 **앞으로만** 움직이므로 오래된 slot을 다시 실행하기 전까지는 문제가 없습니다.
커서가 여전히 오늘을 가리키므로 backfill은 오늘의 윈도우를 읽게 되고, 이에 대한 dlt
자신의 답은 상태를 버리는 것입니다.

Brevis가 건네주는 윈도우는 cron에서 나오므로 3월 run은 몇 번을 재시도하든 3월의
윈도우를 만듭니다. dlt는 이를 받아들입니다. 이름은 dlt 자신의 것이며, Airflow를 찾기
전에 먼저 읽는 값이고, 엔진이 모든 스케줄 run에 설정합니다.

```python
@dlt.resource(primary_key="id")
def events(
    updated_at=dlt.sources.incremental(
        "updated_at",
        initial_value=datetime(1970, 1, 1, tzinfo=timezone.utc),
        allow_external_schedulers=True,   # this line is the whole integration
    )
):
    yield from api.events(since=updated_at.start_value, until=updated_at.end_value)
```

`allow_external_schedulers=True`가 전부입니다. `$DLT_INTERVAL_START`와
`$DLT_INTERVAL_END`가 dlt의 `initial_value`와 `end_value`가 되며, 양쪽 모두 반개구간입니다.
시작 시각에 정확히 걸친 행은 포함되고 종료 시각에 정확히 걸친 행은 제외되므로, 연속된
slot 사이에 틈도 겹침도 생기지 않습니다.

`end_value`가 있는 incremental은 **영속된 상태를 건드리지 않습니다**. 이 점이
중요합니다. slot 하나를 다시 실행해도 멱등하고, 3월 backfill이 야간 run이 의존하는
커서를 움직이지 않습니다.

동작하기 전에 알아야 할 점이 두 가지 있습니다.

- **커서는 문자열이 아니라 datetime이어야 합니다.** dlt는 비교를 위해 `text` 커서를
  변환하지 않으며, 스케줄러 합류 자체를 거부하고 그 이유가 분명하지 않은
  `JoinSchedulerError`를 냅니다. 리소스에서 `add_map`으로 변환하거나 위와 같이
  `initial_value`의 타입을 지정하세요.
- **스케줄이 없는 workflow는 아무것도 건네주지 않으며**, 그러면 dlt는 조용히 자신의
  커서로 폴백하는 대신 `ExternalSchedulerNotAvailable`을 발생시킵니다. 파이프라인이 두
  방식 모두에서 동작해야 한다면 `allow_external_schedulers`를 끄고 `run.window()`에서
  윈도우를 직접 전달하세요.

step에 `tools: [dlt]`를 선언하는 것은 별개이며 그래프에 칩을 그릴 뿐입니다. 윈도우는
선언 여부와 관계없이 전달됩니다.

## 스냅샷

매개변수는 실행 시점에 workflow에서 읽지 않고 **run 안에** 저장됩니다. run은 트리거될 때의
입력을 가지고 있으므로, 기본값이 바뀐 뒤에도 1월 실행의 로그에는 계속 1월의 값이
표시됩니다.

## 다음 단계

- [구성](/docs/configuration/) — 프로세스 환경 변수
- [SDK](/docs/sdk/) — fetcher가 run 컨텍스트를 읽는 방법
- [step 간 컨텍스트](/docs/context/) — step이 다음 step을 위해 게시하는 것
