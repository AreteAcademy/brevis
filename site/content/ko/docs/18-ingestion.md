---
title: 수집
description: 데이터를 적재하는 HTTP 엔드포인트입니다. 검증하고, 식별하고, 배치로 묶고, 전달합니다. 자체 이미지와 자체 버전을 가집니다.
group: 수집
order: 18
slug: ingestion
---

수집 게이트웨이는 **데이터를 적재하는 HTTP 엔드포인트**입니다. 클라이언트가 `POST`하면
`202`를 받고, 도착한 내용은 토픽의 레코드, 테이블의 행, 또는 버킷의 파일이 됩니다.

![The Gateway flow: a POST to /v1/clicks is decoded, given its ingestion_id, answered with 202 straight away and appended to a batch; a worker delivers the batch to the sink, here files. The Gateway has no database.](/assets/flow-gateway.svg)

이것은 **다른 바이너리, 다른 Go 모듈, 다른 버전, 다른 이미지**입니다. 엔진은
오케스트레이션하며 고객 데이터를 절대 만지지 않고, 게이트웨이는 그 일만 합니다. 번호
체계는 의도적으로 독립적입니다. 엔진은 0.17이고 게이트웨이는 0.27이며, 그것이 사실에
맞는 표기입니다.

아래 섹션의 `gateway.yaml`과 `keys_from`에서 요구하는 키로 실행합니다.

```bash
docker run -p 8080:8080 -e BREVIS_INGEST_KEYS=dev-key \
  -v ./gateway.yaml:/etc/brevis/gateway.yaml:ro \
  brevissh/brevis-gateway:0.32.0
```

```text
2026/10/09 01:03:01 drain budget 30s; set terminationGracePeriodSeconds >= 40
2026/10/09 01:03:01 connections idle out after 1m30s and are recycled after 1m0s
2026/10/09 01:03:01 listening on :8080
2026/10/09 01:03:01 metrics on :9090/metrics
```

`POST`는 즉시 `202`로 돌아옵니다.

```bash
curl -X POST localhost:8080/v1/clicks -H 'Authorization: Bearer dev-key' \
  -d '{"event_id":"e-1","occurred_at":"2026-10-08T12:00:00Z","page":"/pricing"}'
```

```text
{"accepted":1,"rejected":null}
```

Google Cloud 자격 증명이 없으면 배치는 재시도되다가 dead letter로 갑니다.
[sink가 거부할 때](#when-the-sink-refuses)를 참고하세요.

`-slim` 이미지에는 `auto_table`, `files`, `postgres` sink만 들어 있습니다. 이 예제의
`pubsub`과 나머지는 전체 이미지에 있습니다. 포함하지 않은 sink를 쓰는 slim 이미지는
시작 시점에 이유와 함께 거부됩니다.

## 파일이 곧 계약입니다

hook 외에는 작성할 코드가 없습니다. stream, 경로, 형식, 아이덴티티, 재시도, 목적지,
dead letter가 모두 YAML에 있으며, 지킬 수 없는 필드는 모두 거부됩니다.

```yaml
name: events_gateway

listen:
  addr: :8080
  max_body: 1MiB
  idle_timeout: 90s                  # the default; it cannot be turned off
  max_conn_age: 60s                  # off unless set — see below
  auth:
    type: bearer
    keys_from: BREVIS_INGEST_KEYS   # the NAME of the variable, never the keys

metrics:
  addr: :9090                        # its own port, never the ingest one

streams:
  - name: clicks
    path: /v1/clicks
    format: json                     # json | array | ndjson — declared, never sniffed

    identity:
      provider: web
      entity: click
      source_key: event_id
      record_ts: occurred_at

    buffer:
      flush: {every: 1s, records: 500}

    sink:
      type: pubsub
      project: acme-prod
      topic: clicks

    dead_letter:
      type: files
      path: /var/dead-letter/
```

`format`은 선언하며 추측하지 않습니다. 추측은 천 건의 배치가 배열 하나를 담은 한 행이
되는 원인입니다.

## 핵심인 `ingestion_id`

모든 레코드는 `ingestion_id`와 함께 나갑니다. `provider|entity|source_key|record_ts`에
대한 **고정된 UUID v5**입니다.

같은 레코드에 대해 배치 fetcher가 계산하는 id와 **동일한 id**입니다. 게이트웨이가 적재한
행과 [SDK](/docs/sdk/) 파이프라인이 적재한 행은 *하나의 행*이며, 둘 사이에 조정
작업이 없습니다.

실용적인 결과가 두 가지 있습니다.

- **재시도된 `POST`는 같은 레코드입니다.** 클라이언트는 걱정 없이 다시 보낼 수 있으며,
  `merge`가 설정된 목적지는 두 번째 행을 쓰지 않고 재시도를 흡수합니다.
- **공식은 고정되어 있습니다.** 네 필드가 모두 필수이며, 하나를 빠뜨리면 더 약한 id가
  아니라 *다른* id가 만들어집니다. 파일은 불완전한 구성을 받아들이지 않고 거부합니다.

수집 도구는 바이트를 매우 잘 옮기지만 레코드가 *무엇인지*에 대해서는 의견이 없습니다.
게이트웨이가 더하는 것이 바로 그 의견입니다.

## 요청은 sink를 기다리지 않습니다

디코딩하고, hook을 실행하고, 아이덴티티를 계산하고, 버퍼에 추가한 뒤 반환합니다.
**그것이 전부입니다.** 가득 찬 배치는 worker 풀로 가며, 게시, `COPY`, 재시도, dead
letter는 모두 거기서 일어납니다.

이것은 겉치레가 아닙니다. 이 구조가 생기기 전에는 우연히 배치를 채운 요청이 전달을
인라인으로 실행했습니다. 그래서 `flush.records`마다 호출자 한 명이 왕복 전체 비용을
치렀고, sink가 내려가 있으면 재시도 윈도우 전체를 치렀습니다. 운이 나빴던 호출자가
누구냐에 따라 모양이 정해지는 p99는 누구도 근거로 삼아 행동할 수 없습니다.

```yaml
buffer:
  flush:
    every: 1s         # age
    records: 500      # count
    size: 8MiB        # bytes — whichever is crossed FIRST sends the batch
    max_age: 30s      # and this one sends it REGARDLESS
  workers: 4          # batches delivered at once
  queue: 64           # full batches that may wait for a worker
  max_records: 10000  # events held in memory before the gateway says no
  max_bytes: 256MiB   # the same ceiling in bytes
```

### flush 시계는 배포에 속합니다

`buffer.flush.every`는 **모든 레플리카**의 `time.AfterFunc`입니다. 그래서 테이블이
받는 load job 수는 트래픽이 아니라 레플리카 수의 함수입니다.

```
jobs / day / table  =  replicas × 86400 / every
```

BigQuery에서는 테이블당 하루 1,500건 기준으로, **60초 주기의 레플리카 두 개만으로도 이미
늘릴 수 없는 쿼터의 192%**입니다. HPA가 아무것도 하기 전의 이야기입니다. 그리고 방향이
직관과 반대입니다. 게이트웨이는 HTTP 동시성을 위해 스케일 아웃하지만 sink는 집중을
원합니다.

```yaml
buffer:
  flush:
    every: 60s
    max_age: 300s
    claim: true
```

`claim`을 쓰면 레플리카는 타이머로 flush하기 전에 stream과 윈도우에 대해 set-if-absent를
수행합니다. 타이머는 **윈도우 경계**에서 깨어나므로 모든 레플리카가 같은 키를 요청합니다.
레플리카 수와 관계없이 윈도우당 flush는 한 번입니다. 그리고 스스로 조정됩니다. 레플리카는
다른 레플리카가 실제로 경쟁할 때만 기다리므로, 스케일 인하면 아무도 구성을 수정하지
않아도 낮은 지연 시간이 복원됩니다.

레플리카 두 개와 Redis 하나, 5초 윈도우로 측정한 결과 **load job 8건이 6건이 되었고,
윈도우당 하나씩 3/3으로 번갈아** 나갔으며 두 건이 있는 윈도우는 없었습니다.

**공유 백엔드가 필요합니다.** `memory`에서는 모든 레플리카가 자기 클레임에서 이기고
아무것도 조정되지 않습니다. 또한 **`max_age`가 필요하며**, 없으면 구성이 거부됩니다.
클레임은 `every`를 목표값으로 바꾸는데, 상한 없는 목표값은 아무것도 제한하지 않는
지연이기 때문입니다.

**도달할 수 없는 metastore에서도 flush합니다.** 조정을 잃으면 load job이 늘어나는 비용을
치르지만, flush를 거부하면 데이터를 붙잡게 됩니다. 실행 도중 Redis를 죽여 확인했습니다.
이벤트 40건 중 40건이 적재되었고 붙잡힌 것은 없었습니다.

**`outcome="unreachable"`은 알림을 걸 가치가 있는 값입니다.** 백엔드에 질의할 수 없어
클레임이 fail-open으로 처리되었다는 뜻입니다. 조정이 꺼져 있고 load job이 레플리카 수만큼
다시 늘어나고 있으며, 모든 flush가 여전히 flush처럼 보이므로 조용히 진행됩니다.

그리고 클레임은 공정성이 아니라 **배타성**을 제공합니다. 먼저 요청하는 쪽이 이깁니다.
실제 레플리카 두 개 실행에서는 균등한 분배가 아니라 4/1이 측정되었습니다. 운이 나쁜
쪽이 굶지 않게 하는 것이 `max_age`입니다.

`claim: true`와 `metastore: memory`를 함께 쓰면 게이트웨이는 **부팅 시 한 번** 경고합니다.
모든 레플리카가 자기 클레임에서 이기므로 모든 것이 맞아 보이지만 아무것도 조정되지
않기 때문입니다.

### `every`는 목표이고, `max_age`는 약속입니다

위의 세 트리거는 배치가 언제 나가려고 **시도하는지**를 결정합니다. `max_age`는 배치가
언제 **무조건** 나가는지를 결정합니다.

현재는 이 차이가 의미 없습니다. 아무것도 목표를 미루지 않기 때문입니다. flush 시계가 더
이상 프로세스별이 아니게 되는 순간부터 중요해집니다. `buffer.flush.every`는 각 레플리카의
`time.AfterFunc`이므로, 테이블이 받는 load job 수는 트래픽이 아니라 **레플리카 수**의
함수입니다. `replicas × 86400 / every`입니다. BigQuery에서는 테이블당 하루 1,500건
기준으로, **60초 주기의 레플리카 두 개만으로도 이미 쿼터의 192%**입니다.

레플리카가 다른 레플리카에게 윈도우를 양보할 수 있게 되면 계속 채우게 되고, `max_age`가
순서를 기다리며 데이터를 무기한 쥐고 있는 일을 막습니다.

또한 드레인도 정해 줍니다. `SIGTERM` 시점에 버퍼가 **쥐고 있을** 수 있는 양이 이것으로
제한되며, 게이트웨이가 부팅 시 출력하는 `terminationGracePeriodSeconds`가 이 값에서
도출됩니다.

목표값보다 **낮은** 상한은 로드 시점에 거부됩니다. 목표값이 절대 적용되지 않는다는
뜻이 되기 때문입니다.

### 크기 트리거가 있는 이유

나머지 둘은 **개수**이며, 개수로는 2 KB짜리 이벤트 500건과 2 MB짜리 이벤트 500건을
구별할 수 없습니다. 파일에는 같은 숫자인데 1 MB와 1 GB입니다. `max_records: 10000`도
같은 사각지대를 가집니다. 20 MB인지 20 GB인지 YAML의 어떤 것도 말해 주지 않았습니다.

id 대신 문서 전체를 보내기 시작한 프로듀서는 pod를 죽이는데, 이 트리거가 생기기 전에는
**그 한계를 표현할 수 있는 필드가 하나도 없었습니다.**

크기는 **도착한** 것을 기준으로 측정합니다. 메모리에서 이벤트가 차지하는 크기(`map[string]any`는
JSON의 몇 배입니다)가 아니며, 크기를 부풀리는 hook 이후의 크기도 아닙니다. 근사치이며
올바른 근사치입니다. 디코딩 시 비용이 없고, 페이로드와 함께 움직이며, 막으려는 대상이
열 배로 커진 레코드이기 때문입니다.

**BigQuery에서 `size`는 목표가 아니라 상한입니다.** 해당 목적지는 테이블당 하루 1,500건의
load job을 허용하며, 그래서 `every`에 60초의 하한이 있습니다. 그러나 하한이 지배하는 것은
**타이머**입니다. 몇 초마다 발동하는 size는 그 하한을 그대로 지나쳐 같은 쿼터를 소모합니다.
도착 속도를 로드 시점에는 알 수 없으므로 이를 거부하는 것은 없습니다. 대신 가시성이
있습니다.

```
brevis_gateway_flushes_total{stream,trigger}   trigger = time | records | size
```

BigQuery stream에서 `trigger="size"`가 올라간다면 배치를 만드는 것이 윈도우가 아니라는
신호입니다.

이 카운터는 stream 단위인데 쿼터는 **테이블** 단위입니다. `auto_table`에서는 라우트
하나가 여러 테이블이 되고, 94개의 테이블이 하나의 숫자를 보고하면 그중 어느 것에 대해서도
알 수 없습니다. `BREVIS_INGESTION_METRICS` 뒤에는 이를 이름으로 구별하는 메트릭이 있습니다.

```
brevis_gateway_table_flushes_total{stream,table,trigger}
```

테이블의 flush 한 번은 그 테이블의 load job 한 건이므로, BigQuery가 허용하는 1,500건 중
1,200건에서 알림을 걸 시리즈가 이것입니다. 무조건 제공되는 쪽은 부팅 시의 한 줄로,
이벤트가 도착하기 전에 `flush.every`가 테이블당 하루에 얼마를 소모하는지 알려 줍니다.

**한도가 있으며, 던져 놓고 잊는 방식이 아닙니다.** 배치마다 고루틴을 만들면 sink 장애가
메모리 무한 증가로 이어집니다. 큐와 버퍼가 모두 가득 차면, 둘 곳이 없는 이벤트에 `202`를
주는 대신 **`Retry-After`가 포함된 `503`**으로 응답합니다. 수락했는데 전달되지 않는
이벤트는 이 서비스가 존재하며 막으려는 바로 그 결과입니다.

그리고 이 `503`은 거의 어떤 서비스도 주장할 수 없는 방식으로 **재시도해도 안전합니다**.
`ingestion_id`는 이벤트 자체의 고정된 함수이므로, 같은 본문을 다시 보내면 *같은 레코드*
입니다.

## 연결이 어느 레플리카가 부하를 받을지 결정합니다

Kubernetes `ClusterIP` Service 뒤에서 kube-proxy는 요청별이 아니라 **TCP 연결당 한 번**
백엔드를 고릅니다. 대부분의 클라이언트는 연결을 유지하고 재사용하므로, 연결이 몇 개
없는 프로듀서는 레플리카가 몇 개 실행 중이든 모든 것을 한두 레플리카에 보냅니다. HPA가
추가한 레플리카는 아무도 연결하지 않으므로 아무것도 받지 못합니다.

```yaml
listen:
  idle_timeout: 90s     # closes a connection nobody came back to
  max_conn_age: 60s     # after this, the next response asks the client to redial
```

**`max_conn_age`는 기본적으로 꺼져 있으며** 의도된 동작입니다. 클라이언트가 연결 하나를
얼마나 오래 유지할 수 있는지는 정책이고, 연결당 수명마다 클러스터 내부 핸드셰이크 한 번을
치르는 대가로 부하가 분산됩니다. 요청 자체는 항상 처리되며, 헤더가 응답에 붙을 뿐 아무것도
재시도되지 않고 배치도 손실되지 않습니다.

**`idle_timeout`에는 기본값이 있고 끌 수 없습니다.** 없이 서비스하면 버려진 클라이언트마다
프로세스가 재시작될 때까지 파일 디스크립터 하나가 남습니다. 길게 두고 싶다면 길게
설정하세요. 그러면 누군가 선택한 숫자가 됩니다.

둘은 독립적입니다. 연결은 먼저 만나는 한도에 걸리고, 연결이 수명에 도달하는 것은 사용되는
경우뿐입니다. 수명이 재분배하려는 대상이 바로 오래 유지되는 프로듀서입니다.

## sink가 거부할 때

배치는 재시도되고(기본적으로 약 7초에 걸쳐 4회, 지터와 함께 두 배씩 증가), 그다음에
**dead letter**로 가며 모든 레코드에 이유가 붙습니다.

```json
{
  "event_id": "dl-9",
  "ingestion_id": "84aaee4b-66af-5cc0-b97b-6856dec95b25",
  "_dead_letter_reason": "rpc error: code = NotFound desc = Topic not found",
  "_dead_letter_sink": "pubsub:acme-prod/clicks",
  "_dead_letter_at": "2026-09-25T02:02:32Z"
}
```

이유는 로그에만 남지 않고 **레코드에** 실려 이동합니다. 나중에 그 파일을 발견하는 사람이
가진 것은 로그가 아니라 이벤트이며, *이것이 왜 여기 있는가*가 첫 번째 질문이기
때문입니다.

**`dead_letter`가 없는 stream은 로드 시점에 거부됩니다.** 기본값을 침묵으로 두면 결정이
아무도 내리지 않는 곳에 놓이게 되고, 로그 한 줄뿐인 거부된 배치는 조용히 데이터를 잃는
것이기 때문입니다.

## hook은 컴파일된 Go입니다

```go
hooks := gateway.NewHooks()
hooks.MustRegister("enrich_clicks", enrichClicks)
gateway.Main(hooks, gateway.WithSinks(sinks))

func enrichClicks(e map[string]any) (map[string]any, error) {
    host, _ := e["host"].(string)
    e["tenant"] = strings.Split(host, ".")[0]
    return e, nil     // returning nil DROPS the event, on purpose
}
```

YAML은 hook의 이름을 지정할 뿐 hook을 담고 있지 않습니다. 선택하기 전에 측정했습니다.
Go 함수는 **93 ns/op**이고 Starlark는 959, yaegi는 1,281이며, 바이너리에 추가되는 것은
없습니다. 그리고 `plugin.Open`은 선택지가 아닙니다. 여기서 모든 산출물을 빌드하는 방식인
`CGO_ENABLED=0`에서는 `plugin: not implemented`를 반환하기 때문입니다.

누군가 읽을 곳에 밝혀 두는 비용은 이렇습니다. **hook을 추가하는 것은 구성 변경이 아니라
재빌드와 배포입니다.** hook을 바이너리를 배포하는 사람들이 작성하는 동안에는 맞는
트레이드오프이고, 고객이 릴리스 없이 hook을 바꿔야 하는 날에는 틀린 트레이드오프입니다.

hook의 오류는 *그* 이벤트를 dead letter 경로로 보내며, 옆의 배치를 실패시키지 않습니다.
패닉도 마찬가지입니다. 이벤트별로 복구되는데, 잘못된 레코드 하나가 다른 모든 stream을
서비스하는 프로세스를 죽여서는 안 되기 때문입니다.

## 이벤트가 너무 클 때

```yaml
oversize:
  larger_than: 256KiB
  archive: {type: files, path: gs://acme-oversize/clicks/}
  hook: strip_heavy_clicks
```

이벤트는 `archive`에 **통째로** 기록되고, 되돌아갈 포인터를 담은 축소 버전이 stream으로
계속 진행합니다.

```json
{
  "event_id": "big-1",
  "ingestion_id": "bd8ac083-9796-50f5-bab6-77a4e2e57ad0",
  "_oversize": true,
  "_oversize_archive": "files:gs://acme-oversize/clicks/",
  "_oversize_bytes": 2149
}
```

이것은 **Claim Check** 패턴이며 단순한 `413`보다 낫습니다. `413`은 이벤트를 잃게 하고,
너무 큰 페이로드는 대개 누군가 가진 가장 흥미로운 페이로드이기 때문입니다. 문서 전체가
첨부된 요청이며, 디버깅할 가치가 있는 경우입니다.

`larger_than`은 hook 이후의 **이벤트** 하나를 측정합니다. `listen.max_body`는 다른
것입니다. **요청** 전체를 제한하며, 바이트 하나를 파싱하기 전에 `413`으로 거부합니다.

## 무엇을 집계하나

**전용 포트**에서 Prometheus 시리즈 열한 개를 제공합니다. 수집(ingest) 포트가 아니며, 이
규칙은 엔진에서보다 여기서 더 중요합니다. 수집 포트는 구조상 공개되어 있고 클라이언트가
`POST`하는 곳이므로, 거기에 `/metrics`를 두면 경로를 찾아낸 누구에게나 모든 stream
이름, 경로, 목적지를 공개하게 됩니다. 둘에 같은 주소를 지정하면 로드 시점에 거부됩니다.

```
brevis_gateway_events_received_total{stream,format}     counter
brevis_gateway_events_rejected_total{stream,reason}     counter  per event, after decode
brevis_gateway_requests_refused_total{stream,reason}    counter  per request: unauthorized|body_too_large|malformed|empty|saturated
brevis_gateway_batches_total{stream,sink,outcome}       counter  delivered|retried|buried
brevis_gateway_reconciled_total{stream,sink}            counter  landed after the send window; also delivered
brevis_gateway_flushes_total{stream,trigger}            counter  time|records|size
brevis_gateway_flush_windows_total{stream,outcome}      counter  won|yielded|ceiling|unreachable
brevis_gateway_ingested_bytes_total{stream,table}        counter  opt-in
brevis_gateway_ingested_events_total{stream,table}      counter  opt-in
brevis_gateway_table_flushes_total{stream,table,trigger} counter  opt-in
process_start_time_seconds                              gauge    unprefixed, on purpose
brevis_gateway_saturated_total{stream}                  counter  the 503s
brevis_gateway_delivery_seconds{stream,sink}            histogram
brevis_gateway_buffer_records{stream}                   gauge
brevis_gateway_queue_batches{stream}                    gauge
```

`process_start_time_seconds`에는 `brevis_` 접두사가 붙지 **않으며** 의도된 동작입니다.
컬렉터가 정확히 그 이름을 찾기 때문입니다. 이 값은 프로세스가 시작된 시각을 알려 주며,
이 값이 없으면 시작 시각 보정을 수행하는 컬렉터(Google Managed Prometheus가 기반으로 하는
OpenTelemetry Prometheus receiver)는 모든 누적 시리즈를 **첫 번째 스크레이프**에 고정하고
그 스크레이프의 값을 기준선으로 소모합니다. 결과적으로 모든 카운터가 낮게 읽히며, 증분은
맞고 합계는 틀립니다.

마지막 세 개는 첫 번째 장애가 나기 전까지는 아무도 찾지 않는 것들입니다.
`saturated_total`은 클라이언트가 물러나라는 응답을 받았다는 사실을 알려 주는 유일한
숫자이고, `buffer.max_records`에 대한 `buffer_records`는 그것을 **예측**하는 유일한
숫자입니다.

`reason`은 닫힌 집합이며 오류 텍스트가 아닙니다. 오류 문자열에는 테이블 이름, 컬럼, 때로는
행이 들어 있고, 잘못된 클라이언트 하나가 요청마다 새 시리즈를 만들어 낼 것이기
때문입니다. 텍스트는 dead letter의 레코드에 남습니다.

## `200`이 아니라 `202`

게이트웨이는 이벤트를 **수락**했습니다. 구현된 유일한 티어인 `durability: memory`에서는
그것이 정직하게 주장할 수 있는 전부입니다. `200`을 받을 자격이 있는 티어는 이미 기록해
둔 티어이며, 그런 티어는 존재하지 않습니다.

`disk`는 메모리로 받아들여 처리하는 대신 **이름을 명시하며** 거부됩니다. `disk`라고 쓴
사람은 이벤트가 충돌에서 살아남는다고 믿는데, 전달하지 못하면서 동의하는 것은 그
필드가 막으려는 바로 그 실패이기 때문입니다.

종료 시점에 버퍼에 있는 것은 버려지지 않고 전달됩니다. 가득 찬 배치가 가는 경로와 같은
경로로, 재시도되고 묻힙니다.

## 하지 않는 것

- **Console에 나타나지 않습니다.** 엔진의 인터페이스와 데이터베이스를 공유하지 않으므로
  `/ingestion` 페이지가 없으며, `auto_table`이 수행하는 스키마 진화도 거기에 나타나지
  않습니다. 다음 단계이며 아직 작성되지 않았습니다.
- **`table`이 있는 stream은 스키마를 진화시키지 않습니다.** 테이블을 지정하면 계약은
  테이블의 것입니다. 테이블에 없는 필드는 누군가 리뷰한 테이블을 변경하는 대신, 해결
  방법이 담긴 메시지와 함께 거부됩니다. 스스로 컬럼을 늘리는 것은 `auto_table`이
  하는 일이며, 이것이 둘의 차이입니다.
- **`table`이 있는 stream은 테이블도 인덱스도 만들지 않습니다.** 테이블을 만드는 서비스는
  오타를 아무도 읽지 않는 두 번째 테이블로 만듭니다. 그래서 생성은 `auto_table`에서만
  일어나며, 거기서는 `naming`이 존재할 수 있는 이름을 제한하고 `listen.auth`가
  필수입니다.
- **게시된 이미지에는 hook이 없으며**, 컴파일된 hook 설계에서는 이것이 정직한 산출물입니다.
  `hook:`을 선언하지 않는 stream만 처리합니다.

## 하나의 라우트, N개의 테이블

`auto_table`은 각 이벤트를 **envelope**가 지정하는 테이블로 라우팅하고, 테이블이 없으면
만들고, 새 필드가 나타나면 컬럼을 늘립니다.

```json
{"table_name": "app_orders", "operation": "INSERT", "unique_key": "id",
 "data": {"id": "A-1", "total": 150, "customer": {"id": 7, "uf": "SP"}}}
```

제어 필드는 최상위에, 레코드는 `data` 안에 있고, 모든 테이블에 `brevis_*` 추적 컬럼 일곱
개가 붙습니다. 라우트 하나, 테이블 N개, 선언은 없습니다.

[수집 sink](/docs/ingestion-sinks/#one-route-n-tables-nothing-declared)에 있습니다.

## 다음으로 이동

| 원하는 것 | 이동할 곳 |
|---|---|
| 목적지와 각각이 쓰는 방식 | [수집 sink](/docs/ingestion-sinks/) |
| 반대편에서 본 `ingestion_id` | [Go SDK](/docs/sdk/) |
| 대시보드와 알림 | [관측성](/docs/observability/) |
