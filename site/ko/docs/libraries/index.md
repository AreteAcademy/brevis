# 클라이언트 라이브러리

> 어떤 언어의 step이든 컨텍스트와 run의 시계를 읽는 방법과, 이미 라이브러리가 있는 언어입니다.

*https://brevis.sh/ko/docs/libraries/ · brevis.sh docs (ko)*

---

step이 오케스트레이터에게 필요로 하는 것은 두 가지입니다. **이전 step이 게시한
것**과 **이 run이 읽어야 할 시계**입니다. 클라이언트 라이브러리는 이 두 가지만
건네주고 다른 것은 하지 않습니다.

## 라이브러리보다 계약이 먼저입니다

엔진이 정의하는 것은 **환경 변수 두 개와 파일 하나**입니다.

| | |
|---|---|
| `BREVIS_INPUT` | `{"<step id>": {…}}` — 이 step이 의존하는 step들이 게시한 것 |
| `BREVIS_OUTPUT` | JSON 객체 **하나**를 쓸 경로 |

여기에 run 변수와 자동 매개변수가 더해지며 모두 읽기 전용입니다. `BREVIS_RUN_*`와
`BREVIS_AUTO_*`입니다. Kubernetes에서 `BREVIS_OUTPUT`은 `/dev/termination-log`이며,
엔진은 종료 코드를 얻기 위해 이미 이 파일을 읽습니다.

따라서 **API 호출도, 토큰도, 포트도 없습니다**. 어떤 언어의 step이든 JSON 파싱과 파일
쓰기만으로 참여할 수 있습니다.

```bash
window=$(jq -r '.extract.watermark' <<< "$BREVIS_INPUT")
./publish --until "$window"
jq -n --arg n "$(date -Is)" '{published_at: $n}' > "$BREVIS_OUTPUT"
```

여기서 라이브러리는 새로운 기능을 열어 주지 않습니다. 다시 작성하지 않는 편이 나은
검증, 값이 없을 때의 처리, 오류 메시지를 대신해 줄 뿐입니다.

## 어떤 라이브러리가 있나

| 언어 | | |
|---|---|---|
| **Python** | `pip install brevis` | [제공 중](/ko/docs/python/index.md) — PyPI의 `0.2.1` |
| **Go** | `brevis/sdk/context` | 제공 중, SDK 모듈 안에 있습니다 |
| **Node.js** | — | 계획 중 |
| **Rust** | — | 계획 중 |
| 그 외 모든 언어 | 위의 변수 | 지금 바로 |

:::note Go는 `lib/`에 없습니다
Go의 컨텍스트 지원은 프로젝트가 이미 게시하는 `sdk/` 모듈 안에 하위 패키지로 있으며,
가져오지 않는 사람에게는 아무것도 남지 않도록 정리됩니다. `lib/go-context`를 만들면
패키지 하나를 위해 두 번째 Go 모듈이 필요해집니다.

규칙은 **이미 게시된 산출물이 없는 언어마다 디렉터리 하나**입니다.
:::

각 라이브러리는 자체 버전과 태그를 가진 별도의 산출물입니다. Python 클라이언트를
고친다고 엔진 릴리스가 필요하지는 않습니다.

## 라이브러리가 무엇이고 무엇이 아닌가

라이브러리는 계약 위의 **얇은 클라이언트**이며, Go SDK의 포팅이 아닙니다.

SDK의 ETL 기능, 즉 드라이버, 페이지네이션, 체크포인트, ingestion id를 포팅하는 것은
여기에 맞지 않으며 아마 어디에도 맞지 않습니다. 이 포팅이 겨냥하는 모든 언어에는 이미
그 일에 더 나은 도구가 있고, Brevis의 역할은 그것들을 실행하는 것입니다. Python
라이브러리가 존재하는 이유도 팀이 pandas, Polars, dbt를 그대로 쓰면서 오케스트레이터가
존재하는 핵심 기능 하나를 얻게 하기 위해서입니다.

## 메트릭

라이브러리는 메트릭을 **내보내고**, 노출하는 것은 엔진입니다.

```python
from brevis import metrics

metrics.set("rows_loaded", 48213)      # a gauge: the last value wins
metrics.inc("vendor_rejected_total")   # a counter: it adds up
```

메트릭은 workflow와 step이 레이블로 붙은 채 스케줄러의 포트에 있는 **엔진의**
`/metrics`로 나옵니다. [관측성](/ko/docs/observability/index.md)을 참고하세요.

**포트를 열지 않으며, 열 수도 없습니다.** step은 자기만의 pod에서 40초 동안 실행되고
종료됩니다. 그 step이 연 포트는 한 번도 수집되지 않거나 운 좋게 한 번 수집될
것입니다. 그래서 해당 라인은 stdout으로 가고, 이는 엔진이 파이프라인의 단계를 그리려고
이미 읽고 있는 바로 그 파이프이며 엔진이 이를 기록합니다. 설치할 것도 운영할 것도
없고, step은 자기 workflow slug를 알 수 없으므로 레이블은 엔진의 것입니다.

API 세부 사항은 [Python](/ko/docs/python/#파이프라인만-아는-메트릭)에 있습니다.

## 다른 언어용 클라이언트 작성하기

우리보다 먼저 Node.js나 Rust에 도달했다면, 클라이언트가 올바르게 처리해야 하는 것과
Python 구현이 이미 어렵게 배운 것은 다음과 같습니다.

1. **step으로 한정된 키.** step 없는 `get("bucket")`은 거부되어야 합니다. 두 step이
   `bucket`을 게시할 수 있으며, 검색으로 하나를 고르는 것은 우연에 의한 우선순위입니다.
2. **쓰기를 병합합니다.** 서로 다른 키로 두 번 호출하면 둘 다 유지되고, 같은 키로 두 번
   호출하면 마지막 것이 유지됩니다.
3. **프로세스 종료 시에만 flush합니다.** 강제로 죽은 step은 아무것도 게시하지 않으며,
   이것이 올바른 동작입니다. 충돌한 step의 출력은 끝나지 않은 작업을 기술하기
   때문입니다.
4. **4096바이트를 넘으면 거부합니다.** 플랫폼은 거부하지 않고 **잘라 내며**, 반으로 잘린
   객체는 다음 step에 유효하지 않은 JSON으로 도착합니다.
5. **Brevis 밖에서도 동작해야 합니다.** `BREVIS_INPUT`이 없으면 `get()`은 기본값을
   반환하고 `set()`은 예외를 일으키지 않고 받아서 버립니다. 손으로 실행할 수 없는
   스크립트는 개발할 수 없기 때문입니다.

## 다음 단계

- [Python](/ko/docs/python/index.md) — 전체 API
- [step 간 컨텍스트](/ko/docs/context/index.md) — 개념과 4096바이트 상한
- [매개변수](/ko/docs/parameters/index.md) — 선언하는 매개변수와 자동 매개변수
