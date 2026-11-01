# Go SDK

> fetcher를 작성하는 방법으로, 소스에서 추출하고 변환한 뒤 목적지에 적재합니다.

*https://brevis.sh/ko/docs/sdk/ · brevis.sh docs (ko)*

---

step의 역할이 **데이터를 가져와 어딘가에 적재하는 것**이라면, Go SDK로 fetcher 전체를
몇 줄에 작성할 수 있습니다. 엔진과는 별개의 모듈이며 버전도 독립적입니다.

```bash
go get github.com/AreteAcademy/brevis/sdk@latest
```

Go 1.23 이상이 필요합니다. SDK가 행을 `iter.Seq2`로 스트리밍하기 때문입니다.

:::warning `v0.1.0`은 사용하지 마세요
존재하지 않는 리비전을 고정한 `go.mod`가 포함된 채 배포되었고, Go 모듈 프록시는
불변입니다. `v0.1.1`부터 시작하세요.
:::

## 세 단계

```go
import (
	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to/bigquery"
)

rows, err := sdk.Extract(ctx, sdk.Source{
	From: from.HTTP{URL: "https://api.example.com/v1/events"},
})

rows = sdk.Transform(rows, sdk.Accept("id", "created_at", "amount"))

res, err := sdk.Load(ctx, rows, sdk.Target{
	To:      bigquery.Table{Dataset: "bronze", Name: "events"},
	Columns: []string{"id", "created_at", "amount"},
})
```

`Extract`는 읽고, `Transform`은 형태를 바꾸고, `Load`는 기록합니다. 각각 시퀀스를 받아
시퀀스를 반환하므로 메모리에 한꺼번에 구체화되는 것은 없습니다.

![The SDK flow: sdk.Extract reads from an HTTP API with from.HTTP, sdk.Transform keeps the accepted fields and sdk.Load writes the rows to a BigQuery table. The three calls run inside your own step.](/assets/flow-sdk.svg)

## 드라이버는 설정이 아니라 값입니다

`from.HTTP`는 HTTP 소스에 필요한 모든 것, 즉 URL, 헤더, 재시도, 페이지네이션, 응답이
의미하는 바를 담고 있습니다. `from.Files`는 경로와 형식을 담고 있습니다. 어느 쪽도 상대
필드를 위한 자리를 마련할 필요가 없으므로, 드라이버마다 여섯 개만 읽는 옵션 마흔 개를
모아 둔 소스 구조체가 없습니다.

또한 **무엇을 컴파일할지**도 이것이 결정합니다. Go는 사용한 필드가 아니라 가져온
패키지 기준으로 의존성을 정리합니다.

| 가져오는 것 | 패키지 수 | AWS | Google |
|---|---|---|---|
| `sdk` | 190 | 아니오 | 아니오 |
| `sdk` + `from` | 194 | 아니오 | 아니오 |
| `sdk` + `from` + `to` (files) | 195 | 아니오 | 아니오 |
| `sdk` + `to/bigquery` | 456 | 아니오 | **예** |
| `sdk` + `from` + `store/s3` | 265 | **예** | 아니오 |

읽기와 쓰기를 모두 하는 파일 파이프라인 전체가 패키지 195개이며 클라우드 SDK는 전혀
필요 없습니다. **벤더 SDK를 뒤에 둔 드라이버는 자기만의 패키지에 있으며**, BigQuery가
`to/bigquery`이고 오브젝트 스토어가 `store/s3`와 `store/gcs`인 이유가 이것입니다.

## 소스와 목적지

| 소스 | 패키지 |
|---|---|
| HTTP | `from.HTTP` |
| files (로컬, S3, GCS) | `from.Files` |
| Postgres | `from/postgres` |
| MySQL | `from/mysql` |
| 여러 소스 | `from.Many` |

| 목적지 | 패키지 |
|---|---|
| files | `to.Files` |
| BigQuery | `to/bigquery` |
| Postgres | `to/postgres` |
| MySQL | `to/mysql` |
| Redshift | `to/redshift` |
| Pub/Sub | `to/pubsub` |

경로의 스킴이 백엔드를 결정하며, `Store`는 드라이버 안에서 고르지 않고 **외부에서
전달**합니다.

```go
from.Files{Path: "s3://bucket/day=1/*.ndjson", Store: s3.New(client)}
to.Files{Path: "gs://bucket/landing/", Store: gcs.New(client)}
```

이 방식 덕분에 로컬 파일만 쓰는 프로그램은 AWS나 Google 코드를 한 줄도 컴파일하지
않습니다.

### 토픽에 게시하기

`to/pubsub`은 테이블이 아닌 유일한 목적지이며, 사용하기 전에 알아 둘 만한 방식으로
다르게 동작합니다. **전송하는 내용에 아무것도 추가하지 않습니다.**

```go
Target: sdk.Target{
    To: pubsub.Topic{
        Project: "acme-prod",
        Name:    "orders",

        // Nil is the default, and it means NO attributes at all.
        Attributes: func(e sdk.Envelope) map[string]string {
            row, ok := e.Payload.(map[string]any)
            if !ok {
                return nil
            }
            return map[string]string{"orderId": fmt.Sprint(row["order_id"])}
        },
    },
},
```

구독자는 페이로드를 바이트 단위로 그대로, 그리고 지정한 속성만 정확히 받습니다. 그
외에는 `ingestion_id`도, `provider`도, 어떤 종류의 envelope도 없습니다.

이것은 미니멀리즘이 아니라 규칙입니다. 테이블은 그것을 기록하는 파이프라인이 만들므로
SDK가 컬럼을 정해도 됩니다. **토픽은 그렇지 않습니다.** 토픽은 파이프라인보다 먼저
존재하고, 구독자와 필터도 먼저 작성되었습니다. 임의로 추가된 속성은 Brevis가 다른
사람의 계약을 수정하는 것이 되고, 구독자는 새벽 세 시에 그 사실을 알게 됩니다.

여기서 세 가지가 따라 나옵니다.

- **토픽을 절대 만들지 않습니다.** 토픽을 만들 수 있는 파이프라인은 잘못된 토픽도 만들
  수 있고, 잘못 입력한 테이블과 달리 아무도 알아채지 못합니다. 메시지는 어딘가로
  가고, 받았어야 할 구독자는 조용합니다.
- **`Schema`, `Dedup`, `PartitionBy`는 옵션 이름을 명시하며 거부됩니다.** 이들은
  테이블을 위한 것입니다. `Schema`는 목적지가 테이블을 만드는 방법이고, 중복 제거에는
  교체할 행이 필요하며, 파티션은 테이블의 개념입니다. 여기서 가장 가까운 것은
  `OrderingKey`인데, 이것은 *도착* 순서가 중요한 메시지를 묶는 다른 개념입니다.
- **실패는 부분적입니다.** 게시는 트랜잭션이 아니므로, 31,000건에서 실패한 48,000건의
  메시지는 31,000건이 *전달된* 것입니다. 결과는 실제로 나간 것을 보고하고, 오류는
  재실행이 재전달임을 알려 줍니다. 구독자는 멱등해야 하며, 어차피 Pub/Sub은
  at-least-once입니다.

에뮬레이터를 대상으로 실행 가능한 예제는
[`examples/13-pubsub`](https://github.com/AreteAcademy/brevis/tree/master/examples/13-pubsub)입니다.

## 완전한 fetcher

`sdk.Run`은 플래그, `-dry-run`, 로깅, 재시도, 페이지네이션, 출처 정보, 테이블 생성,
종료 코드를 처리합니다. 파일에 남는 것은 해당 소스에만 해당하는 내용뿐입니다.

```go
package main

import (
	"time"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
)

func main() {
	sdk.Run(sdk.Pipeline{
		Source: sdk.Source{
			From: from.HTTP{
				URL:     "https://api.example.com/v1/events",
				Timeout: 15 * time.Second,
			},
			Guard:  sdk.RejectIf("error"),
			Expand: sdk.ArrayAt("results"),
		},
		Target: sdk.Target{
			Provider: "example",
			Entity:   "events",
			Key:      sdk.Key("id"),
			When:     sdk.Field("created_at"),
		},
	})
}
```

```bash
go run ./fetcher -dry-run   # extracts, counts rows and errors, writes nothing
go run ./fetcher
```

## run 컨텍스트

fetcher가 **workflow step으로** 실행될 때, 엔진은 다른 방법으로는 알 수 없는 정보를
환경에 주입합니다. 첫 번째 run인지, 어떤 매개변수로 디스패치되었는지, 어떤 run인지
같은 것입니다. SDK는 이를 `Pipeline.Run`에서 읽습니다.

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	if p.Run.Params["load_full"] == "true" {
		p.Source.From = from.HTTP{URL: base + "?full=1"}
	}
	return nil
},
```

손으로 실행하면 `Run`은 0으로 초기화된 채 돌아옵니다. 읽는 것은 선택 사항이며 무시해도
비용이 없습니다.

이력이 없으면 "첫 번째 run인가?"에 대한 답은 항상 **아니오**입니다. 확신 없이 테이블을
만드는 것은 만들지 않는 것보다 나쁘기 때문입니다.

### 시계

`p.Run.Auto`는 엔진이 이 run에 대해 계산한 것으로, [자동 매개변수](/ko/docs/parameters/#자동-매개변수)입니다.
`Auto.Now()`는 `time.Now()` 대신 읽어야 하는 시계입니다. 스케줄된 run에서는 slot이므로,
run이 늦어도 움직이지 않고 세 시간 뒤에 재시도해도 움직이지 않습니다.

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	start, end, ok := p.Run.Auto.Window()
	if !ok { // no schedule: fall back to a fixed window
		start, end = p.Run.Auto.Now().Add(-24*time.Hour), p.Run.Auto.Now()
	}
	p.Source.From = from.HTTP{URL: base +
		"?from=" + start.Format(time.RFC3339) +
		"&to=" + end.Format(time.RFC3339)}
	return nil
},
```

손으로 실행하면 `Auto.Now()`는 *실제* 벽시계이고 `Window()`는 `false`를 반환하므로,
로컬 개발에 특별한 처리가 필요 없습니다.

## 클라이언트 라이브러리와 혼동하지 마세요

이 SDK는 Go로 된 ETL 기능으로, 드라이버, 페이지네이션, 출처 정보, 테이블 생성을
담당합니다. [클라이언트 라이브러리](/ko/docs/libraries/index.md)는 다른 것입니다. step에 run
컨텍스트와 시계를 건네주는 얇은 클라이언트이며, 현재는 Python이고 이후에 Node.js와
Rust가 추가될 예정입니다. pandas나 dbt를 쓰는 step에 필요한 것은 이 SDK가 아니라
클라이언트 라이브러리입니다.

## 파이프라인을 만드는 매개변수

`p.Run.Params`는 파이프라인 안에서 읽을 수 있는데, 파이프라인이 **무엇인지**를 결정하는
값에는 너무 늦습니다. 상태마다 소스 하나, 테넌트마다 테이블 하나 같은 경우입니다.
`Pipeline.Flags`는 `Run` 안에서 파싱되므로 같은 이유로 너무 늦습니다.

```go
func main() {
	sdk.Run(pipeline(sdk.ParamList("ufs")))
}
```

`sdk.Param`과 `sdk.ParamList`는 엔진의 환경을 먼저 읽고 `-param name=value`를 그다음에
읽습니다.

```bash
./fetch-stations -param ufs=SP,RJ
```

환경이 우선합니다. 엔진 아래에서는 환경이 곧 값이며, manifest에 남아 있는 플래그가
운영자가 입력한 값을 덮어써서는 안 됩니다. 엔진이 없을 때는 플래그가 유일한 값이며,
매 실행마다 `BREVIS_RUN_PARAMS`를 JSON으로 쓰는 것보다 낫습니다.

## 랜딩 레이아웃

파이프라인은 **게이트웨이가 적재하는 것과 같은 테이블**, 즉 같은 컬럼과 같은
`brevis_ingestion_id`를 적재할 수 있습니다. 그래서 둘 중 무엇을 쓸지는 스키마가 아니라
배포의 결정입니다. 팀은 둘을 함께 읽을 수 있고, 마이그레이션 없이 한쪽에서 다른 쪽으로
옮길 수 있습니다.

```go
const table = "landing.orders"

sdk.Run(sdk.Pipeline{
    Source:    sdk.Source{From: from.HTTP(/* ... */)},
    Transform: []sdk.Transformer{sdk.Landing(table, sdk.LandingKey("id"))},
    Target: sdk.Target{
        To:     postgres.Table{DSN: dsn, Name: table, CreateTable: true},
        Schema: sdk.LandingSchema(sdk.LandingOptions{}),
    },
})
```

`sdk.LandingSchema`는 아홉 개의 컬럼입니다. 레이아웃이 소유하는 여덟 개와 프로듀서가
소유하는 하나입니다. `sdk.Landing`이 이를 채우고 레코드를 통째로 `data`에 넣습니다.
원하면 스키마에 자신의 컬럼을 덧붙이거나, `sdk.LandingControlColumns`를 가져와 레코드의
필드에 각각 컬럼을 부여하세요.

### 두 가지 형태: 레코드 통째로, 또는 필드당 컬럼 하나

위의 것은 **document** 형태입니다. 레코드가 JSON으로 `data`에 들어가며, 레코드의 내용과
무관하게 테이블은 같습니다. `sdk.LandingColumns()`를 추가하면 레코드의 각 필드가 자기만의
컬럼을 갖습니다.

```go
sdk.Landing(table, sdk.LandingKey("source_key"), sdk.LandingColumns())
```

이때 테이블은 `sdk.LandingControlColumns`에 자신의 컬럼을 더한 것이며, `data`를 포함하는
`sdk.LandingSchema`가 **아닙니다**.

```go
Target{
    To: postgres.Table{DSN: dsn, Name: table, CreateTable: true},
    Schema: append(sdk.LandingControlColumns(sdk.LandingOptions{Keyed: true}),
        sdk.Column{Name: "source_key", Type: sdk.TypeString},
        sdk.Column{Name: "series", Type: sdk.TypeString},
        sdk.Column{Name: "data", Type: sdk.TypeString},
        sdk.Column{Name: "valor", Type: sdk.TypeString},
    ),
}
```

**컬럼은 직접 선언합니다.** SDK는 스키마를 절대 추론하지 않으며, 이 옵션도 그것을
바꾸지 않습니다. 이 옵션은 *행*을 만드는 방식을 결정할 뿐 테이블이 무엇인지는 결정하지
않습니다. 게이트웨이가 추론하는 것은 프로듀서가 가진 것을 무엇이든 게시하는 낯선
사람이기 때문입니다. 여기서는 파이프라인 작성자가 형태를 알고 있고, 누군가 리뷰하는
diff에 그것을 적어 둡니다.

게이트웨이가 [`shape: columns`](/ko/docs/ingestion-sinks/#두-가지-형태-하나의-계약)로 적재하는 것과
같은 형태이며, 같은 코드를 통하므로 컬럼도, 값도, `brevis_ingestion_id`도 같습니다.

**필드 이름은 컬럼 이름이 될 수 있어야 합니다.** 문자 또는 밑줄로 시작하고 그 뒤에
문자, 숫자, 밑줄이 옵니다. 네 가지 목적지 중 가장 좁은 BigQuery의 규칙이므로, 여기서
통과하는 이름은 어디서나 동작합니다. `my-field`를 가진 레코드는 아무것도 쓰기 전에 이름과
함께 거부됩니다. document 형태에는 그런 규칙이 없습니다. 하이픈은 JSON 키로 아무 문제가
없습니다.

**기본적으로 평탄화하지 않습니다.** 중첩된 객체는 그것을 담고 있던 키 아래의 `JSON`
컬럼 **하나**가 됩니다. `customer`는 `customer_id`와 `customer_uf`가 아니라
`{"id": 7, "uf": "SP"}`를 담습니다. 배열도 `JSON`입니다. 배열 요소마다 행 하나를 원하는
레코드는 소스의 `Expand`에 `sdk.ArrayAt`을 써야 합니다. 이것은 `Landing`보다 먼저
실행되며 이름도 다른 별개의 연산입니다.
[`BREVIS_NORMALIZE_DATA`](#중첩-필드당-컬럼-하나)는 배포 전체에 대해 앞부분을
바꾸며, 배열은 어느 쪽이든 `JSON`으로 남습니다.

```go
Source: sdk.Source{From: from.HTTP(/* ... */), Expand: sdk.ArrayAt("results")},
```

**`null`은 `NULL`로 남으며** `""`가 되지 않습니다. null로 보낸 필드와 비어 있게 보낸
필드는 서로 다른 사실이며, 컬럼은 나중에 이 둘을 구별할 수 없습니다.

**`data`는 사용자의 것이 됩니다.** 이 형태에서 레이아웃은 그 이름의 컬럼을 갖지 않으므로,
`data`라는 필드를 가진 프로듀서(Bacen 시리즈가 정확히 그렇습니다)는 자기 값이 들어 있는
자기만의 컬럼을 얻습니다. `document`에서는 그 필드가 레이아웃의 JSON 안의 키 하나일
뿐입니다.

### `brevis_` 접두사는 바꿀 수 있습니다

```
BREVIS_LANDING_PREFIX=acme
```

그러면 레이아웃의 컬럼 여덟 개는 `acme_ingestion_id`, `acme_record_key` 등이 됩니다.
설정하지 않으면 지금까지와 같습니다.

**테이블을 만들 때 선택하세요. 나중에 바꾸는 것은 이름 변경이 아니기 때문입니다.** 이
SDK는 어떤 컬럼도 삭제하지 않으므로, 접두사를 바꾼 stream은 여덟 개의 이름을 바꾸는 것이
아니라 여덟 개를 추가하고 여덟 개를 버립니다. 그 이후의 모든 행은 이전 집합에 NULL을
갖게 되며,

```sql
qualify row_number() over (partition by brevis_record_key
                           order by brevis_received_at desc) = 1
```

는 더 이상 아무것도 쓰지 않는 컬럼으로 파티션을 나눕니다. 행은 도착하는데 대시보드는
평평해지고, 아무것도 로그에 남기지 않습니다.

그래서 한 접두사로 레이아웃을 이미 가진 테이블은 다른 접두사를 선언한 적재를 추출
전에, 두 접두사를 명시하며 **거부합니다**. 새 테이블을 지정하거나 먼저 여덟 개 컬럼의
이름을 직접 바꾸세요.

**입력한 값은 정규화되므로** 틀릴 수 있는 표기가 네 가지가 아니라 하나가 됩니다.

| 설정한 값 | 결과 |
|---|---|
| 없음 또는 공백 | `brevis_` |
| `_NAME` | `name_` |
| `_NAME_` | `name_` |
| `NAME` | `name_` |

양쪽 끝의 `_`를 제거하고, 소문자로 바꾸고, 정확히 하나를 붙입니다. 두 가지 값은
추측하지 않고 거부합니다. 밑줄만 있는 값은 `ingestion_id`가 남는데, 이는 모든 일반
파이프라인에서 `sdk.IngestionID()`가 쓰는 바로 그 컬럼이기 때문입니다. 또 컬럼 이름의
시작이 될 수 없는 값도 거부합니다. **사용할 수 없는 값을 가진 프로세스는 시작하지
않으며**, 이것이 유일하게 정직한 결과입니다. 대안은 교체하려던 접두사 이름의 테이블이
생기는 것입니다.

**프로세스 단위이며, 한계가 아니라 의도입니다.** 한 게이트웨이의 모든 stream이 이를
공유하므로 두 sink가 서로 다를 수 없으며, 테이블을 선언하는 스키마와 그것을 채우는
변환기도 서로 다를 수 없습니다.

**예약 규칙도 함께 이동합니다.** `acme_` 아래에서는 `acme_region`을 보내는 프로듀서가
거부됩니다. 제어 컬럼을 위조할 수 있는 사람은 진짜처럼 보이는 것을 위조하기 때문입니다.
그리고 `brevis_region`은 평범한 필드가 됩니다.

`data`에는 접두사가 절대 붙지 않습니다. 레이아웃의 컬럼이 아니라 프로듀서의 컬럼이기
때문입니다.

### 중첩 필드당 컬럼 하나

```
BREVIS_NORMALIZE_DATA=true
```

이렇게 하면 중첩 객체의 필드가 pandas의 `json_normalize`처럼 자기만의 컬럼을 얻습니다.
한 단계까지입니다.

```python
data = [
    {"id": 1, "name": {"first": "Coleen", "last": "Volk"}},
    {"name": {"given": "Mark", "family": "Regner"}},
    {"id": 2, "name": "Faye Raker"},
]
```

```
id  name_first  name_last  name_given  name_family  name
```

**첫 번째 테이블 전에 선택하세요. 컬럼을 추가하기만 하는 것이 아니기 때문입니다.**
이름도 바꾸는데 이 SDK는 컬럼을 삭제하지 않으므로, 이전 이름은 이미 들어 있는 행들과
함께 남고 이후의 모든 것은 새 이름에 적재됩니다. 누군가 이전 이름에 대해 작성한 쿼리는
계속 실행되지만 새 행을 더 이상 보지 못합니다. 이름 변경은 두 가지가 일어납니다.

- `userName`은 중첩 여부와 관계없이 `username`이 됩니다.
- 객체를 담고 있던 `name`은 `name_first`와 `name_last`로 대체됩니다.

두 형태 중 하나라도 이미 가진 테이블은 이 옵션이 켜진 적재를 추출 전에 두 컬럼을
명시하며 **거부합니다**. 새 테이블을 지정하거나 먼저 컬럼 이름을 직접 바꾸세요.

**`name`은 평탄화된 형태와 통째의 형태로 모두 나타날 수 있으며**, 위 예제가 보여 줍니다.
세 번째 레코드는 `name`을 문자열로 가지므로 테이블에는 `name_first`, `name_last`,
그리고 `name`이 모두 있습니다. 한 레코드에서는 객체이고 다른 레코드에서는 스칼라인
필드는 둘 다 만듭니다.

**한 단계까지이며, 배열은 값입니다.** `{"a": {"b": {"c": 1}}}`은 `a_b`가
`{"c": 1}`을 담는 `JSON` 컬럼이지 `a_b_c`가 아닙니다. `{"a": [1, 2]}`는 `a`이며 역시
`JSON`입니다. 배열 요소마다 행 하나를 원하는 레코드는 여전히 `sdk.ArrayAt`이
필요합니다.

**연결하고 소문자로 바꾸며, camelCase에서 나누지 않습니다.** `userName`은 `user_name`이
아니라 `username`이 됩니다. 다른 해석은 `HTTPStatus`, `userID` 같은 약어에 대해 합의된
답이 없고 라이브러리마다 서로 다르기 때문입니다.

**충돌은 해결하지 않고 거부합니다.** `{"name_first": 1, "name": {"first": 2}}`는
필드 두 개로 컬럼 하나를 만들게 되는데 Go의 맵 순회는 무작위이므로, 한쪽이 이기게 두면
같은 배치가 실행마다 다른 테이블을 만들게 됩니다. 거부 메시지는 둘 다 명시합니다.

**예약된 접두사는 필드가 아니라 컬럼에서 검사합니다.** `{"brevis": {"stream": "x"}}`를
보내는 프로듀서는 그렇지 않으면 필드 검사가 지키지 않는 문을 통해 `brevis_stream`을
만들게 됩니다. 필드를 찾을 수 있도록 `brevis.stream`을 명시하며 거부됩니다.

**접두사와 마찬가지로 프로세스 단위이며**, 이유도 같습니다. 한 게이트웨이의 모든 stream이
이를 공유하므로 둘이 함께 쓸 수 있는 테이블에 대해 두 sink가 서로 다를 수 없습니다.
`columns` 형태에만 적용됩니다. `document`에서는 레코드가 통째로 `JSON` 컬럼 하나에
들어가므로 평탄화할 대상이 없습니다.

### 이미 JSON인 텍스트

`sdk.TypeJSON`으로 선언된 컬럼은 JSON **값**, 즉 객체, 배열, 숫자를 받습니다. BigQuery에서는
행 전체가 인코딩되므로 Go `string`이 JSON 문자열 리터럴이 되고, 컬럼은 객체가 아니라
객체의 텍스트를 담게 됩니다.

```sql
SELECT JSON_TYPE(payload) ...;            -- "string", not "object"
SELECT JSON_VALUE(payload, '$.a') ...;    -- NULL
```

컬럼의 타입은 맞지만 그 안의 데이터에는 접근할 수 없으며, 아무것도 실패하지 않으므로
세 가지 결과 중 최악입니다. **BigQuery 목적지는 `v0.74.0`부터 이를 거부하며**
컬럼을 명시합니다.

객체가 있으면 객체 자체를 전달하세요. 이미 JSON 텍스트를 가지고 있다면, 예를 들어 다른
데이터베이스에서 읽은 컬럼이나 다시 파싱하고 싶지 않은 응답 본문이라면 그렇다고
알려 주세요.

```go
row["payload"] = sdk.JSONText(body)
```

`encoding/json`의 `json.RawMessage`도 같은 이유로 동작합니다. 둘 다 따옴표로 감싼
문자열이 아니라 가지고 있는 JSON을 그대로 씁니다.

**Postgres와 MySQL은 영향을 받지 않으며 거부되지도 않습니다.** 두 서버는 문자열을 JSON
컬럼으로 파싱하며, 예전부터 그랬습니다.

### 테이블에 없는 필드가 나타났을 때

프로듀서가 필드를 추가하면 적재가 해당 필드를 명시하며 멈춥니다. 행이 어떤 선언에도 없는
컬럼을 담고 있고, 그것을 언급한 적 없는 목적지에 쓰면 조용히 버려지기 때문입니다. 이
거부는 기본값으로는 옳지만, 랜딩 테이블에서는 독자를 깨뜨릴 수 없는 유일한 변경이므로
목적지가 이를 허용할 수 있습니다.

```go
postgres.Table{DSN: dsn, Name: table, Evolve: sdk.EvolveAdditiveFromPayload}
```

그러면 배치가 선언을 **완성합니다**. 배치가 가지고 있지만 선언에 없는 필드는 컬럼이
되며, 필드가 객체나 배열이 아니면 `STRING`이고 그렇다면 `JSON`입니다. 게이트웨이가
`shape: columns`를 적재할 때와 같은 규칙, 같은 코드이므로 둘은 같은 테이블을 만듭니다.

다른 값이 두 가지 더 있으며, 이들의 차이는 컬럼이 어디에서 오는가입니다.

| `Evolve` | 추가하는 것 |
|---|---|
| `sdk.EvolveNone` (기본값) | 없음. 모든 차이를 양쪽을 명시하며 거부합니다 |
| `sdk.EvolveAdditive` | **선언**에는 있고 테이블에는 없는 컬럼 |
| `sdk.EvolveAdditiveFromPayload` | 위의 것에 더해 **배치**가 가진 컬럼 |

**`ADD COLUMN`만 합니다.** 소스에서 사라진 컬럼은 쓰기가 멈추지만 테이블에는 남습니다.
이름 변경도, 삭제도 없고, 타입이 좁혀지는 일도 없습니다. 이것은 빈틈이 아닙니다.
랜딩 테이블은 이력이며, 읽는 사람을 깨뜨릴 수 없는 유일한 변경은 null을 허용하는 새
컬럼입니다.

**선언을 완성할 뿐 대체하지 않습니다.** 선언된 것이 없으면 추출 전에 테이블을 명시하며
거부됩니다. 페이로드로 테이블 전체를 만드는 것은 다른 결정이며 SDK는 내리지 않습니다.
랜딩 레이아웃에서는 해악이 구체적입니다. `brevis_received_at`이 타임스탬프가 아닌
텍스트로 나오고, `brevis_loaded_at`은 행에 실려 이동하지 않으므로 종단 간 지연 시간
측정이 그냥 사라집니다.

**SDK는 여전히 추론하지 않습니다.** 타입은 필드의 값이 아니라 **형태**에서 나옵니다.
`21129`와 `8.89`는 둘 다 `STRING`을 선언하므로, 시리즈가 정수를 게시하는 날에도 아무것도
바뀌지 않습니다. 첫 번째 배치에서 읽은 타입은 이 규칙이 막으려는 바로 그 실패입니다.

**그리고 `null`은 아무것도 결정하지 않지만 행에는 남습니다.** 이는 서로 다른 두 질문이며,
하나의 답으로 처리하려다 v0.77.0이 대가를 치렀습니다.

null에는 형태가 없으므로 **스키마**가 읽을 것이 없습니다. `null`로 도착한 필드는 컬럼을
선언하지 않으며, 컬럼은 값을 가진 첫 번째 레코드에서 그 값의 형태로 나타납니다. 반대로
하면 null이 답이 없다는 이유로 `STRING`이 되고, 그다음에 오는 첫 번째 배열 때문에
목적지가 배치 전체를 거부하게 되며, 이를 되돌릴 `STRING`에서 `JSON`으로의 마이그레이션도
없습니다.

**행**은 프로듀서가 보낸 것을 null까지 포함해 담습니다. 그래야 합니다.
`Target.Schema`에 *선언한* 컬럼이 빈 채로 도착했다면 행에 있어야 하는데, 그렇지 않으면
선언과 행을 비교하는 검사가 더 이상 그것을 만들지 않는 체인을 읽고 적재를 거부합니다.
v0.77.0이 그렇게 동작했고 0.78.0이 되돌렸습니다.

나머지는 목적지가 정리합니다. 배치의 모든 레코드에서 null이고 테이블에 컬럼이 없는
필드는 어디에도 쓰이지 않으며 아무것도 거부하지 않습니다. null과 없는 필드는 똑같이
`NULL`로 적재되므로 잃는 것은 없습니다.

**형태가 바뀌는 필드는 흡수되지 않고 거부됩니다.** `valor`가 스칼라로 도착해 텍스트
컬럼을 만들었다면, 이후에 도착하는 객체는 종류의 변경이며 배치가 멈춥니다. 메시지에는
컬럼 자신의 주석이 포함되어, 아무도 선언하지 않았음을 확인할 수 있습니다. 컬럼 이름을
바꾸거나 직접 마이그레이션하세요.

**BigQuery에서는 한 이름의 두 표기가 컬럼 하나입니다.** BigQuery는 컬럼 대소문자를
접습니다. 한 레코드에 `nationalID`, 다른 레코드에 `nationalId`가 있는 배치는 컬럼을 둘이
아니라 **하나** 선언하며, 이기는 표기는 정렬 순서상 첫 번째인 `nationalID`입니다. `D`가
`d`보다 앞에 정렬되기 때문입니다. 먼저 도착한 것이 아닙니다. 같은 배치를 재생해도
표기가 다른 컬럼이 만들어지면 안 되기 때문입니다. 컬럼이 `createdAt`인 테이블에 대해
`createdat`을 선언하는 것도 같은 컬럼이므로, 아무것도 추가되지 않고 아무것도 거부되지
않습니다.

**한 레코드에 두 표기가 모두 있으면 둘을 명시하며 거부됩니다.** 목적지에는 컬럼이 하나인데
두 값 중 하나가 조용히 이기게 되므로 적재가 멈춥니다. `Transform`에서 하나의 이름을
바꾸거나 둘을 하나의 필드로 매핑하세요. 각각 한 표기를 가진 *두 레코드*는 문제없습니다.
일반적인 CDC 트래픽이며, 이 규칙이 적재하려는 대상이 바로 그것입니다.

**마이그레이션되는 것은 없습니다.** 대소문자만 다른 컬럼 두 개를 이미 가진 테이블은 둘 다
유지하며, 이름이 바뀌거나 삭제되는 컬럼은 없습니다.

**Postgres와 MySQL은 접지 않습니다.** 따옴표로 감싼 식별자는 거기서 구별되므로 두 표기는
컬럼 두 개입니다. 의도된 동작이며, 같은 fetcher를 BigQuery로 향하게 하면 하나로
적재됩니다.

**배치가 만든 모든 컬럼은 테이블 안에 그 사실이 적힙니다.** Postgres와 MySQL에서는
주석, BigQuery에서는 필드 설명이며 날짜를 담습니다.

```
brevis: added from a batch on 2026-09-29; the type is the landing rule
(scalar text, object and array json), not a decision
```

여섯 달 뒤에는 "이 컬럼이 언제 나타났고 누가 결정했는가"에 답해 주는 유일한 것이
이것입니다. 로그 라인은 그때쯤 로테이션되어 사라집니다.

**모드가 아닙니다.** `Target.Schema`는 여전히 테이블이 무엇인지 여러분이 선언하는
것입니다. 이 기능은 이미 존재하는 레이아웃을 직접 입력하는 대신 건네줄 뿐입니다. 이를
사용했다고 해서 SDK의 어떤 것도 다르게 동작하지 않으며, 자기만의 스키마를 선언하는
파이프라인은 영향을 받지 않습니다.

**테이블 이름은 한 번만 쓰세요.** 이름은 `Landing`에 전달되는데 id의 두 번째 슬롯이기
때문이고, 목적지에도 전달되는데 행이 가는 곳이기 때문입니다. **둘이 일치하는지는 아무것도
검사하지 않습니다.** `Writer`는 자기 테이블을 알지만 로그와 오류용 이름인 `Describe`를
통해서만 노출하며, 그것으로 id를 만들면 이미 기록된 모든 id가 로그 문자열에 묶이게
됩니다. 둘이 어긋나면 행은 올바르게 적재되지만 id는 아무도 쓰지 않은 테이블 기준으로
만들어집니다. 멀쩡해 보이겠지만 게이트웨이의 것과는 일치하지 않습니다.

**세 컬럼은 `NULL`이며, 그것이 정직한 답입니다.** `brevis_stream`과 `brevis_gateway`는
파이프라인에는 없는 것을 가리킵니다. `brevis_gateway IS NULL`이 파이프라인의 행과
게이트웨이의 행을 구별하는 방법입니다. `brevis_received_bytes`도 `NULL`입니다.
게이트웨이의 값은 envelope를 포함해 와이어로 도착한 양이고, 레코드 자체의 JSON은 약 30%
작습니다. 두 가지 의미를 가진 컬럼 하나는 두 경로의 행에 걸친 합계를 어느 쪽에서 얼마가
왔는지에 따라 틀리게 만들 것입니다. `length(data)`가 파이프라인 쪽 값을 정확히 답합니다.

**병합에는 `DedupKey`가 필요하며, 없으면 병합이 전혀 일어나지 않습니다.** 별도로
지정하지 않으면 모든 드라이버는 `ingestion_id`로 매칭하는데, 이 레이아웃의 식별 컬럼은
`brevis_ingestion_id`입니다.

```go
Target{
    To:       postgres.Table{DSN: dsn, Name: table, CreateTable: true},
    Schema:   sdk.LandingSchema(sdk.LandingOptions{UniqueID: true, Keyed: true}),
    Dedup:    sdk.DedupMerge,
    DedupKey: sdk.LandingColumnID,
}
```

`UniqueID`가 id에 UNIQUE 제약을 거는 것이며, Postgres와 MySQL은 제약 없이는 병합을
거부합니다. `ON CONFLICT`가 매칭할 대상이 없기 때문입니다. `Schema`가 선언하지 않은
`DedupKey`는 아무것도 실행되기 전에 거부됩니다. 테이블에 없는 컬럼에 대한 병합은
아무것도 매칭하지 않으며, 아무것도 매칭하지 않는 병합은 매칭해야 할 것을 모두 매칭하는
병합과 똑같아 보이기 때문입니다.

**목적지마다 다르며, 현재 MySQL은 할 수 없습니다.** BigQuery에는 고유 제약이 없으므로
선언 자체를 거부합니다. MySQL의 `string`은 `LONGTEXT`이고, MySQL은 길이 없이는
`LONGTEXT`를 키로 지정할 수 없으므로 병합하는 랜딩 테이블은 거기서 아예 *생성*할 수
없습니다.

```
Error 1170 (42000): BLOB/TEXT column 'brevis_ingestion_id' used in key
specification without a key length
```

Postgres는 `string`이 `TEXT`이므로 동작합니다. MySQL에서는 `DedupNone`으로 적재하고
현재 버전은 다운스트림에서 결정하거나, `CreateSQL`로 크기를 지정한 id 컬럼과 함께
테이블을 직접 만드세요.

**id는 콘텐츠 기반 주소 지정입니다.** 같은 레코드는 어느 경로에서든 같은 id를 만들고,
변경된 레코드는 새 id를 만듭니다. 그래서 재실행이 중복이 아닌 no-op이 될 수 있습니다.

## 참고

- [pkg.go.dev](https://pkg.go.dev/github.com/AreteAcademy/brevis/sdk) — 전체 API
- [`examples/`](https://github.com/AreteAcademy/brevis/tree/master/examples) — 실행 가능한 예제 열두 개
- [`CHANGELOG.md`](https://github.com/AreteAcademy/brevis/blob/master/CHANGELOG.md) — 버전별 변경 이력
