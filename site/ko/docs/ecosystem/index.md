# 에코시스템

> brevis.sh의 네 가지 구성 요소가 각각 하는 일과 데이터가 지나가는 경로입니다.

*https://brevis.sh/ko/docs/ecosystem/ · brevis.sh docs (ko)*

---

Core는 스케줄링하고, 큐에 넣고, 감시하지만 데이터는 절대 만지지 않습니다. 데이터를 다루는 것은 가져오는 SDK, 받아들이는 Gateway, 그리고 이미 도착한 데이터를 모델링하는 SQL입니다.

## 네 가지 구성 요소

| 구성 요소 | 하는 일 | 받는 방법 | 가이드 |
|---|---|---|---|
| **Core** | step마다 pod 하나를 스케줄링하고, 큐에 넣고, 시작합니다. 데이터는 만지지 않습니다. | `areteacademy/brevis` | [Core](/ko/docs/core/index.md) |
| **SDK** | step 안에서 추출, 가공, 적재를 수행하는 Go 라이브러리입니다. | `go get github.com/AreteAcademy/brevis/sdk` | [Go SDK](/ko/docs/sdk/index.md) |
| **Gateway** | HTTP POST를 받아 202로 응답하고 배치로 기록합니다. 자체 데이터베이스는 없습니다. | `areteacademy/brevis-gateway` | [수집](/ko/docs/ingestion/index.md) |
| **SQL** | 일반 .sql 파일을 의존성 순서대로 테이블과 뷰로 만듭니다. | `areteacademy/brevis-sql` | [SQL](/ko/docs/sql/index.md) |

## 컨트롤 플레인과 데이터 플레인

**컨트롤 플레인**은 Core입니다. 각 step이 언제 실행될지 결정하고, 큐와 상태를
Postgres에 보관하며, step마다 pod 하나를 시작합니다. 데이터는 Core를 거치지
않습니다.

**데이터 플레인**은 나머지 세 구성 요소입니다. 데이터는 API, 데이터베이스,
파일에서 출발해 두 가지 경로 중 하나로 목적지에 도착합니다. 하나는 사용자의
step 안에서 **SDK가 가져오는** 것이고, 다른 하나는 웹훅과 앱이 HTTP로 **Gateway에
푸시하는** 것입니다. 목적지는 테이블, 토픽, 또는 버킷의 파일입니다. 데이터가
도착하면 **SQL**이 데이터가 도착한 그 데이터베이스 안에서 테이블을 모델링합니다.

### 주변 구성 요소

보조 구성 요소가 세 가지 있습니다.

- **[Console](/ko/docs/console/index.md)** — Core 위에서 동작하는 웹 인터페이스
- **[agent](/ko/docs/pod-per-step/#where-each-step-runs)** — 이미 가지고 있는 클러스터에서 step을 실행
- **[Python용 `brevis`](/ko/docs/python/index.md)** — Python step을 위한 실행 컨텍스트

## 한 가지 구성 요소만 사용하기

가능합니다. 각 구성 요소는 고유한 모듈, 버전, 이미지를 가집니다. SDK는 어떤 프로그램에서든 실행되는 Go 라이브러리이고, Gateway는 YAML 파일 하나만 있으면 데이터베이스 없이 단독으로 시작하며, brevis-sql은 컨테이너로 실행됩니다. 원할 때 Core가 나머지를 오케스트레이션하지만, 어느 구성 요소도 Core를 전제 조건으로 요구하지 않습니다.

## 동일한 행

SDK가 가져온 주문과 Gateway에 도착한 주문은 동일한 brevis_ingestion_id를 가진 채 같은 테이블에 도착합니다. 둘 중 무엇을 쓸지는 스키마의 문제가 아니라 배포의 문제가 됩니다.

SDK에서는 `sdk.Landing`을 사용합니다([랜딩 레이아웃](/ko/docs/sdk/#the-landing-layout) 참고).

```go
sdk.Execute(ctx, &sdk.Pipeline{
	Source:    sdk.Source{From: from.Files{Path: "order.json"}},
	Transform: []sdk.Transformer{sdk.Landing("app_orders", sdk.LandingKey("id"))},
	Target: sdk.Target{
		To:     postgres.Table{DSN: dsn, Name: "app_orders"},
		Schema: sdk.LandingSchema(sdk.LandingOptions{}),
	},
}, nil)
```

Gateway에서는 `/v1/tables`로 POST합니다([하나의 라우트, N개의 테이블](/ko/docs/ingestion/#one-route-n-tables) 참고).

```bash
curl -X POST localhost:8080/v1/tables -H 'Authorization: Bearer dev-key' \
    -d '{"table_name":"app_orders","unique_key":"id",
         "data":{"id":"A-1","customer":"acme.example.com","total":150}}'
```

응답은 다음과 같습니다.

```text
{"accepted":1,"rejected":null}  202
```

`app_orders`에 도착한 내용입니다.

| 컬럼 | SDK를 통한 경우 | Gateway를 통한 경우 |
|---|---|---|
| `brevis_ingestion_id` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` |
| `brevis_record_key` | `A-1` | `A-1` |
| `brevis_operation` | `INSERT` | `INSERT` |
| `brevis_gateway` | `NULL` | `acme_gateway` |
| `data` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` |

실제 출력입니다. 두 경로 모두 게시된 이미지와 SDK로 동일한 로컬 Postgres에 대해 실행했습니다. 유일한 차이는 누가 기록했는가이며, brevis_gateway는 SDK가 기록한 행에서 비어 있습니다.
