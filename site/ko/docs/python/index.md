# Python

> pip install brevis — step 간 컨텍스트, run의 시계, 파이프라인 고유의 메트릭을 의존성 없이 제공합니다.

*https://brevis.sh/ko/docs/python/ · brevis.sh docs (ko)*

---

```bash
pip install brevis
```

```python
from brevis import context, metrics, run

bucket = context.get("extract.bucket")
since, until = run.window()          # the window this run covers

context.set(rows=48213, watermark="2026-09-07T03:00:00Z")
metrics.set("rows_loaded", 48213)    # reaches the engine's /metrics
```

**의존성은 영원히 없습니다.** 환경 변수 하나를 읽고, 파일 하나를 쓰고, stdout에 한 줄을
출력합니다. Python 3.9 이상이 필요합니다.

Go SDK의 포팅이 아닙니다. 드라이버도, 페이지네이션도, ingestion id도 없습니다. 팀이
pandas, Polars, dbt를 그대로 쓰면서도 오케스트레이터가 존재하는 이유를 얻게 하려고
존재합니다.

## 컨텍스트 읽기

키는 항상 그것을 게시한 step으로 한정됩니다.

```python
context.get("extract.bucket")            # the value
context.get("extract.nope", default=0)   # absent is not an error
context.of("extract")                    # everything that step published
context.published()                      # what THIS step has published so far
```

step 없는 `context.get("bucket")`은 **거부되며**, 해당 키를 게시한 step들을 명시합니다.
step은 서로 격리되어 있어서 `extract`와 `transform`이 모두 `bucket`을 게시해도 어느
쪽도 잃지 않는데, 접두사 없는 키는 두 step이 게시하는 순간 이를 무너뜨립니다. 검색해서
하나를 고르는 것은 우연에 의한 우선순위이며, 조용히 틀어지기 전까지만 동작합니다.

이 step이 의존하지 않는 step을 읽으면 오류이며, 메시지가 해결 방법을 알려 줍니다.

## 쓰기

```python
context.set(name="Daniel")
context.set(label="Name")
# both survive: {"name": "Daniel", "label": "Name"}
```

호출은 **병합됩니다**. 같은 키를 두 번 쓰면 마지막 쓰기가 유지되고, 다른 키는 첫 번째
값을 건드리지 않습니다. **step 인자는 없습니다.** 프로세스가 쓸 수 있는 네임스페이스는
자기 것뿐이며, 그래서 격리가 규칙이 아니라 구조로 보장됩니다.

프로세스가 종료될 때까지 디스크에는 아무것도 기록되지 않으므로, 강제로 죽은 step은
아무것도 게시하지 않습니다. 이것이 올바른 동작입니다. 충돌한 step의 출력은 끝나지 않은
작업을 기술하기 때문입니다.

## 이 run이 읽어야 할 시계

모든 run은 **자동 매개변수**도 가집니다. 엔진이 스스로 계산하는 값입니다. 아무도
선언하지 않고, 모든 run에 존재하며, 파이프라인이 그렇지 않으면 `datetime.now()`로
답했을 질문에 답합니다.

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

:::warning `datetime.now()`를 쓰지 않는 이유
`run.now()`는 스케줄된 run에서 **slot**입니다. `datetime.now()`를 읽고 두 시간을 빼는
fetcher는 정시에 시작한 run에서는 맞고 큐 때문에 지연된 run에서는 40분이 틀리며, 그 40분은
**어떤 run에도** 속하지 않습니다. 다음 slot도 자기 `now()`를 읽기 때문입니다. 아무것도
실패하지 않고, 빈틈은 몇 주 뒤에야 발견됩니다.
:::

| | |
|---|---|
| `run.now()` | 시계: slot 또는 run이 시작된 시각 |
| `run.window()` | `(start, end)`, end는 미포함. 또는 `None` |
| `run.auto()` | 자동 매개변수. 아래를 참고하세요 |
| `run.context()` | id, 시도, 트리거, 매개변수, auto |
| `run.params()` · `run.param(name)` | workflow에 선언된 매개변수 |
| `run.map_index()` · `run.map_value()` | `for_each:` 아래의 인스턴스 |

`window()`는 이력이 아니라 **cron**에서 나옵니다. 3월 slot의 backfill은 3월에 있었던
윈도우를 만듭니다. `[start, end)`를 요청하면 run이 아무리 늦어도 아무리 자주
재시도해도 겹치거나 비는 구간이 없습니다.

workflow에 스케줄이 없으면 0 시각 쌍이 아니라 `None`을 반환합니다. 에포크부터의 쿼리는
모든 것을 선택하며, 그 실패는 조용해서는 안 되기 때문입니다. 위의 `or`가 폴백의
전부입니다.

### 자동 매개변수

| 필드 | |
|---|---|
| `date` | `"2026-09-08"` — 파티션, 폴더, `WHERE` |
| `adjusted_at` | `now()` 대신 읽을 시계 |
| `scheduled_at` | cron이 요청한 slot. 수동 run에서는 `None` |
| `started_at` | **이번 시도**가 시작된 시각. 재시도하면 달라집니다 |
| `delay_seconds` | 이번 시도가 얼마나 늦었는지. 음수가 되지 않습니다 |
| `interval_start` · `interval_end` | 윈도우, end는 미포함 |
| `previous_error` | 직전 run이 성공하지 못했는지 |
| `previous_success_at` | 마지막으로 **성공한** run의 slot. `previous_error`를 실행 가능한 정보로 만들어 줍니다 |

스크립트를 손으로 실행하면 `run.now()`는 **실제** 벽시계이고 `window()`는 `None`이므로
로컬 개발을 위해 작성할 분기가 없습니다.

## 매핑된 step의 인스턴스 하나

[`for_each:`](/ko/docs/workflows/index.md) 아래에서 엔진은 요소마다 프로세스 하나를 실행하고 각
프로세스에 어떤 요소를 받았는지 알려 줍니다.

```python
partition = run.map_value()
if partition is None:
    raise SystemExit("this step is meant to run under `for_each:`")

load(f"/data/{partition}.csv")
```

JSON 문자열은 **따옴표 없이** 전달됩니다. `["2026-01"]`에 대한 `for_each`는 step에
`"2026-01"`이 아니라 `2026-01`을 넘깁니다. 객체, 숫자, 목록 등 그 외의 값은 JSON 그대로
전달되므로 `json.loads(run.map_value())`를 사용하세요.

매핑되지 않은 step에서는 둘 다 `None`입니다. `-1`이나 `""`이 아니라 `None`인 이유는
sentinel은 결국 누군가 산술을 하게 될 숫자이고, 빈 문자열은 빈 문자열 *그 자체인* 요소와
구별되지 않기 때문입니다.

매핑된 step은 다운스트림에 **컨텍스트를 게시하지 않습니다**. 인스턴스 네 개가 키 하나를
공유할 수 없기 때문입니다. 무언가를 넘겨야 하는 step은 파일을 쓰거나, 그다음 step이
적재된 것을 셉니다.

## 파이프라인만 아는 메트릭

엔진은 볼 수 있는 것, 즉 소요 시간, 시도 횟수, 큐 깊이를 이미 측정합니다. 엔진이 알
방법이 없는 것은 적재된 행 수, 벤더가 거부한 행 수, 버려진 바이트 수입니다. 이것은
step이 알고 있고 라이브러리가 전달합니다.

```python
from brevis import metrics

metrics.set("rows_loaded", 48213)          # a gauge: the last value wins
metrics.inc("vendor_rejected_total")       # a counter: it adds up
metrics.inc("bytes_discarded_total", 4096)
```

메트릭은 workflow와 step이 레이블로 붙은 채 스케줄러의 포트에 있는 **엔진의**
`/metrics`로 나옵니다.

```
brevis_step_rows_loaded{workflow="daily_sales",step="load"} 48213
brevis_step_vendor_rejected_total{workflow="daily_sales",step="load"} 5
```

| | |
|---|---|
| `metrics.set(name, value)` | 게이지. step이 종료된 뒤에도 다음 run이 다른 값을 쓸 때까지 값이 유지됩니다 |
| `metrics.inc(name, by=1)` | 카운터. step과 run에 걸쳐 합산됩니다 |

### 포트를 열지 않는 이유

**step은 수집 대상이 될 수 없기** 때문입니다. `:9090`은 스케줄러와 API의 것이며, 안정적인
주소를 가진 오래 실행되는 프로세스로 15초에서 60초마다 수집됩니다. step은 자기만의
pod에서 시작해 40초 동안 실행되고 종료됩니다. 그 step이 연 포트는 한 번도 수집되지 않거나
운 좋게 한 번 수집될 것입니다.

알려진 해법은 세 가지입니다. Pushgateway(운영해야 하는 구성 요소이며, 누군가 삭제하기
전까지 시리즈가 남습니다), OTLP 컬렉터(구성 요소 **이면서** 이 라이브러리의 의존성이
되는데, 이 라이브러리는 의존성이 없고 앞으로도 갖지 않습니다), 또는 **오케스트레이터**가
전달하게 하는 것입니다. Brevis는 이미 모든 step의 stdout에서 `@brevis:` 라인을 읽고
있으며, SDK의 단계가 그래프에 도달하는 방식이 바로 이것이고, 미터와 Prometheus
엔드포인트도 이미 가지고 있습니다.

그래서 라이브러리는 **stdout에 한 줄**을 쓰고 나머지는 엔진이 합니다. 엔진은 자기가 무엇을
실행하고 있었는지 알기 때문에 메트릭은 workflow와 step 레이블이 붙은 채 공짜로 도착합니다.
Pushgateway를 거치면 이 라이브러리가 직접 레이블을 붙여야 하고 틀리게 됩니다.

쓰기는 `print(flush=True)`인데, 엔진이 그 파이프를 **실시간으로** 읽기 때문입니다. 버퍼링된
라인은 프로세스가 종료될 때 도착하며, 한 시간 동안 실행되는 step에서는 한 시간 늦고
강제 종료된 step에서는 영영 도착하지 않습니다.

### 메트릭이 거부하는 것

**유효하지 않은 이름은 즉시 거부됩니다.** Prometheus는 문자, 숫자, 밑줄을 허용하며
첫 글자는 숫자일 수 없습니다. 거부되는 이름은 깨진 메트릭을 만들지 않고, **파싱을
거부하는** 노출 결과를 만들어 **스크레이프 전체**와 다른 모든 메트릭을 잃게 합니다.
그래서 `rows-loaded`는 아무도 쓰지 않은 이름으로 도착하는 대신 실수가 일어난 이곳에서
`MetricError`를 발생시킵니다.

| 거부되는 것 | 이유 |
|---|---|
| `rows-loaded`, `rows loaded`, `2rows` | Prometheus가 파싱하지 못하며 스크레이프 전체가 사라집니다 |
| `metrics.set("ok", True)` | Python에서 `bool`은 `int`이고, `1`을 보고하는 것은 그 사실을 알아야 의미가 통하는 숫자입니다. 상태가 아니라 개수나 소요 시간을 보고하세요 |
| `metrics.inc("x", -1)` | 카운터는 증가만 합니다. 모든 `rate()`가 이를 가정하며, 문제는 몇 주 뒤 그래프에서 나타납니다 |

스크립트를 손으로 실행해도 라인은 stdout으로 나가며 본래의 모습으로 읽힙니다. 아무것도
수집하지 않고 아무것도 실패하지 않습니다. 노트북에서 `context.set()`을 호출했을 때와
같습니다.

## 거부하는 것

| | 이유 |
|---|---|
| 4096바이트 초과 | 플랫폼은 거부하지 않고 **잘라 내므로**, 너무 큰 객체는 반으로 잘려 도착하고 다운스트림에서 "아무것도 게시하지 않음"으로 읽힙니다 |
| JSON이 아닌 값 | `datetime`은 직렬화하는 쪽에 따라 `"..."`이 되거나 사라집니다. 오류는 키를 명시하며 `.isoformat()`을 제안합니다 |
| 문자열이 아닌 키 | `{1: "x"}`는 `{"1": "x"}`로 돌아와 조회가 실패합니다 |

상한은 우리가 아니라 플랫폼의 숫자입니다. "작업 간 컨텍스트" 기능은 모두 같은 방식으로
실패합니다. 누군가 **컨텍스트**만 들어맞는 곳에 **데이터**를 넣기 때문입니다. 4킬로바이트면
워터마크, 개수, 파티션 목록을 담을 수 있습니다. 참조를 게시하고 행은 있던 곳에 두세요.

:::danger 시크릿 저장소가 아닙니다
게시된 모든 것은 run을 볼 수 있는 누구에게나 보입니다. pod의 상태와 run 이력에
들어 있기 때문입니다. 서명된 URL이 아니라 경로를 게시하세요.
:::

## 손으로 실행하기

Brevis 밖에는 입력이 없고 출력을 읽는 곳도 없습니다. `get()`은 기본값을 반환하고,
`set()`은 받아서 버리며, 라이브러리는 이를 INFO 레벨로 한 번 알려 줍니다. **예외를
발생시키지 않습니다**. 손으로 실행할 수 없는 스크립트는 개발할 수 없기 때문입니다.
검증은 그대로 실행되므로, 노트북에서 동작하는 값은 운영 환경에서도 동작합니다.

## step 테스트하기

계약 전체가 환경 변수 두 개이므로 모킹할 것이 없습니다.

```python
def test_my_step(monkeypatch, tmp_path):
    monkeypatch.setenv("BREVIS_INPUT", '{"extract": {"rows": 42}}')
    monkeypatch.setenv("BREVIS_OUTPUT", str(tmp_path / "out"))
    assert context.get("extract.rows") == 42
```

## 버전

라이브러리는 엔진 및 Go SDK와 별개로 독립적인 버전을 가집니다.

:::warning `0.2.0`은 사용하지 마세요
PyPI에 wheel만 배포되었습니다. `0.2.1`은 같은 코드이며 완전합니다.
:::

`brevis.run`이 읽을 것이 있으려면 엔진이 **`v0.9.0` 이상**이어야 합니다. 이전 엔진에서는
모든 필드가 비어 있고 `run.now()`는 벽시계가 되며, 엔진 밖에서의 동작과 같습니다.

## 다음 단계

- [클라이언트 라이브러리](/ko/docs/libraries/index.md) — 계약과 다른 언어
- [step 간 컨텍스트](/ko/docs/context/index.md) — 개념
- [매개변수](/ko/docs/parameters/index.md) — 선언하는 매개변수
