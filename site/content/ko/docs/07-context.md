---
title: step 간 컨텍스트
description: 한 step이 다음 step에 전달하는 것으로, 환경 변수 두 개와 파일 하나입니다.
group: 개념
order: 7
slug: context
---

step은 값을 게시하고, 그 step에 의존하는 step들이 그 값을 읽습니다. 이 기능이 하는
일은 그것이 전부입니다.

```yaml
steps:
  - id: extract
    run: python fetch.py

  - id: transform
    run: python transform.py
    depends_on: [extract]
```

```python
# extract
from brevis import context
context.set(bucket="s3://landing/2026-09-07", rows=48213)
```

```python
# transform
from brevis import context
bucket = context.get("extract.bucket")
```

키는 항상 `<step id>.<name>` 형식입니다. step은 **자신이 의존하는 step들**이 게시한
것만 읽을 수 있고, 그 외에는 아무것도 읽을 수 없습니다.

:::note 이 기능이 아닌 것
메시지 큐도, run 사이의 상태도, 데이터를 두는 곳도 아닙니다. 한 step이 다음 step에게
남기는 메모이며, 경로나 워터마크, 개수 같은 것입니다.
:::

## 어떤 언어든 라이브러리는 필요 없습니다

계약은 **환경 변수 두 개와 파일 하나**이므로, SDK가 전혀 없는 step도 참여할 수
있습니다.

```bash
window=$(jq -r '.extract.watermark' <<< "$BREVIS_INPUT")
./publish --until "$window"
jq -n --arg n "$(date -Is)" '{published_at: $n}' > "$BREVIS_OUTPUT"
```

| | |
|---|---|
| `BREVIS_INPUT` | `{"<step id>": {…}}` — 이 step이 의존하는 step들이 게시한 것 |
| `BREVIS_OUTPUT` | JSON 객체 **하나**를 쓸 경로 |

Kubernetes에서 이 경로는 `/dev/termination-log`이며, 엔진은 종료 코드를 얻기 위해 이미
이 파일을 읽습니다. 따라서 **API 호출도, 토큰도, 포트도 없고**, 이를 위한 라이브러리는
JSON 파싱과 파일 쓰기가 전부입니다.

| 언어 | |
|---|---|
| Python | [`pip install brevis`](/docs/python/) — [자동 매개변수](/docs/parameters/)를 제공하는 `brevis.run`도 포함합니다 |
| Go | `sdk/context`는 SDK 모듈에 속하며 다른 어떤 것에도 의존하지 않습니다 |
| 그 외 모든 언어 | 위의 두 변수 |

## 격리

어떤 step도 다른 step이 게시한 것을 덮어쓸 수 없습니다. 키에는 작성한 step의 id가
접두사로 붙고, 그 접두사는 step이 아니라 엔진이 결정합니다. 두 step이 `bucket`을
게시하면 `extract.bucket`과 `load.bucket`이 만들어지고, 어느 쪽도 사라지지 않습니다.

## 4096바이트 상한

step이 게시하는 내용은 **4096바이트** 안에 들어가야 합니다. 이 한계는 임의로 정한 것이
아닙니다. Kubernetes에서 전송 수단이 `/dev/termination-log`이고, kubelet이 허용하는
크기가 그것이기 때문입니다.

상한을 넘기면 **step 오류**가 되며, 메시지에 몇 바이트를 썼는지 표시됩니다. 대안은
잘라 내는 것인데, 잘린 JSON은 유효하지 않은 JSON이고 다음 step이 이를 발견하게 됩니다.
그 step은 결함이 자기 탓이 아니라는 것을 알 방법이 없습니다.

값이 들어가지 않는다면 그것은 컨텍스트가 아닙니다. 버킷에 쓰고 경로를 게시하세요.

## 재시도와 재개된 run

새 시도에서는 step이 다시 게시하며 이전 값이 대체됩니다. 이미 성공한 step은 다시
실행되지도 다시 게시하지도 않으며, 그 step이 말한 내용은 의존하는 step들에게 계속
유효합니다.

## step을 수동으로 실행하기

계약의 전부가 변수 두 개이므로, run을 재현하려면 그 변수를 내보내기만 하면 됩니다.

```bash
export BREVIS_INPUT='{"extract":{"bucket":"s3://landing/2026-09-07"}}'
export BREVIS_OUTPUT=/tmp/out.json
python transform.py
cat /tmp/out.json
```

## 다음 단계

- [매개변수](/docs/parameters/) — run 사이에 달라지는 것과 자동 매개변수
- [step 런타임](/docs/runtime/) — step이 실행 위치에 대해 선언하는 것
- [Python](/docs/python/) — 클라이언트 라이브러리와 다른 언어
