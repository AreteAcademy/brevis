# 수집 sink

> 여섯 가지 목적지, 각 데이터베이스가 레코드를 쓰는 방식, 그리고 이미지에 사용하는 것만 담기는 이유입니다.

*https://brevis.sh/ko/docs/ingestion-sinks/ · brevis.sh docs (ko)*

---

목적지는 여섯 가지이며 목록은 의도적으로 명시적입니다. *계획 중인* 커넥터와 *실행되는*
커넥터는 이를 선택하는 사람에게 서로 다른 것이기 때문입니다.

| sink | 상태 | 쓰기의 의미 | 검증 대상 |
|---|---|---|---|
| `pubsub` | **실행됨** | 배치당 게시 한 번, 속성과 정렬 키는 이벤트 자체의 필드에서 가져옵니다 | 에뮬레이터 |
| `postgres` | **실행됨** | `COPY FROM STDIN`, 또는 한 트랜잭션 안의 스테이징 `INSERT` | 실제 Postgres |
| `mysql` | **실행됨** | 트랜잭션 안의 다중 행 `INSERT`, 또는 `INSERT IGNORE` | 실제 MySQL |
| `files` | **실행됨** | 디렉터리, `gs://`, `s3://`에 쓰는 NDJSON 오브젝트 | 파일 시스템과 S3 |
| `bigquery` | **실행됨** | 행을 로드하거나, 스테이징한 뒤 `ingestion_id`로 `MERGE` | **구성만 검증 — 에뮬레이터가 없음** |
| `redshift` | **실행됨** | 배치를 S3에 쓴 뒤 `COPY`, 그다음 `MERGE` | **구성만 검증 — 에뮬레이터가 없음** |

**여섯 개 중 둘은 종단 간 검증이 되지 않았으며**, "실행됨"이라는 말이 아무도 확인하지
않은 주장을 담지 않도록 표에 그렇게 적었습니다. BigQuery와 Redshift는 로컬 에뮬레이터가
없으므로 테스트되는 것은 구성, 거부 동작, 그 아래의 드라이버이며, 드라이버는 SDK 자체
스위트가 이미 다룹니다. 둘 중 어느 쪽이든 운영 환경에서의 첫 사용이 실제 검증이므로,
위험이 낮은 stream으로 시작해야 합니다.

알 수 없는 `type:`은 **시작 시점에 거부되며**, *이 바이너리*가 가진 것을 명시합니다.
없는 sink로 시작하는 게이트웨이는 아무도 알 수 없는 이유로 이벤트를 버리는 게이트웨이이기
때문입니다.

`gs://`와 `s3://`는 별도의 커넥터가 아닙니다. `files`가 세 가지 경로 형태를 모두 읽으므로,
dead letter는 노트북에서는 폴더이고 운영 환경에서는 버킷이며 여기서 바꿀 것이 없습니다.

## 각각이 쓰는 방식

`write`는 테이블에 쓰는 네 목적지에서 **필수이며 기본값이 없습니다**. 두 모드는 실제로
다르며, 어느 쪽인지는 테이블의 소유자만 알기 때문입니다.

| | `append` | `merge` |
|---|---|---|
| **postgres** | `COPY FROM STDIN` | `INSERT … ON CONFLICT (ingestion_id) DO NOTHING` |
| **mysql** | 블록당 다중 행 `INSERT` | `INSERT IGNORE` |
| **bigquery** | 행 로드(인라인 한도를 넘으면 GCS 경유) | `MERGE … WHEN NOT MATCHED THEN INSERT` |
| **redshift** | S3에서 `COPY` | 스테이징 테이블로 `COPY`한 뒤 `MERGE` |

`merge`는 **네 곳 모두에서 같은 의미**입니다. 멱등한 삽입이며 **첫 번째 전달이 이깁니다**.
재전달은 적용되지 않고 무시됩니다. 한 번 일어나고 바뀌지 않는 이벤트에는 맞고, 변경 가능한
상태를 담은 행에는 틀립니다.

Postgres와 MySQL은 `merge`를 위해 **`ingestion_id`에 `UNIQUE` 인덱스를 요구하며**, 없으면
조용히 append하지 않고 거부합니다.

```sql
CREATE UNIQUE INDEX CONCURRENTLY ON landing.orders (ingestion_id);
```

인덱스가 없으면 `ON CONFLICT`가 매칭할 대상이 없고, 모든 재전달이 소유자가 정반대를 요청한
테이블에 중복됩니다. 거부는 배치를 잃는 것이 아닙니다. 재시도된 뒤 이유와 `CREATE INDEX`가
레코드에 붙은 채 dead letter에 묻히므로, 누군가 인덱스를 만드는 동안 이벤트를 잃는 사람은
없습니다.

**`upsert`는 이름을 명시하며 거부됩니다.** *마지막* 전달이 이기는 `ON CONFLICT DO
UPDATE`는 실제 모드이지만 **구현되어 있지 않습니다**. 그 단어를 받아들이면서 `merge`처럼
동작하는 것은 그 필드가 막으려는 바로 그 실패이므로, 구성에서 그렇다고 말합니다.

## 각 sink에만 해당하는 사항

```yaml
# Postgres: schema-qualified name, and the table has to exist.
sink: {type: postgres, dsn_from: BREVIS_DSN, table: landing.clicks, write: merge}

# MySQL: no COPY, so throughput is an order below Postgres on the same
# hardware. Flush wider here.
sink: {type: mysql, dsn_from: BREVIS_DSN, table: landing.clicks, write: merge}

# BigQuery: no dsn_from. It authenticates with the pod's own credentials, which
# is what a workload identity is for. The name has no dots — the project and
# the dataset are their own fields.
sink: {type: bigquery, project: acme-prod, dataset: landing, table: clicks, write: merge}

# Redshift: two hops, not one. It is columnar, a row-by-row INSERT pays the cost
# of a block, and the only workable load is COPY from S3. So every batch becomes
# an object in the staging prefix and then a COPY — a stream flushing every
# second writes 86,400 objects a day. Flush much wider here.
sink:
  type: redshift
  dsn_from: BREVIS_RS_DSN
  table: landing.clicks
  staging: s3://acme-staging/gateway/
  iam_role: arn:aws:iam::123456789012:role/redshift-copy
  write: merge
```

`dsn_from`은 연결 문자열이 아니라 연결 문자열을 담은 **환경 변수**의 이름입니다. DSN에는
비밀번호가 들어 있고 그 파일은 git에 있기 때문입니다. workflow에서 `secrets:`가 하는 것과
같은 분리입니다.

`iam_role`은 역할이며 드라이버는 **키를 받지 않습니다**. `COPY`의 URL에 들어간 키는
클러스터의 쿼리 로그에 남고, 그 로그는 많은 사람이 읽기 때문입니다.

## 컬럼 선언 없음

게이트웨이는 컬럼 목록을 보내지 않습니다. 파이프라인은 행이 자신이 통제하는 단일한 형태를
가지므로 자기 컬럼을 선언합니다. 게이트웨이의 배치는 하나의 flush 윈도우 동안 *N*개의
클라이언트가 게시한 것이 무엇이든 담고 있고, 형태가 서로 다를 수 있습니다.

드라이버는 서버를 건드리기 **전에** **테이블 자체**에서 목록을 가져와 배치가 담고 있는
것과 교집합을 구합니다. 테이블에 없는 필드는 `COPY` 도중 `column "x" of relation "y"
does not exist`로 실패하는 대신 해결 방법이 담긴 메시지와 함께 거부되고, 한 이벤트가
생략한 필드는 `NULL`로 기록됩니다.

## import 목록이 곧 선택입니다

sink는 hook과 마찬가지로 **컴파일되어 포함되며**, 바이너리는 가진 것을 등록합니다.

```go
func main() {
    sinks := gateway.NewSinks()
    sinks.MustRegister(postgres.Sink, postgres.New)
    sinks.MustRegister(files.Sink, files.New)
    gateway.Main(nil, gateway.WithSinks(sinks))
}
```

Go 링커는 아무것도 참조하지 않는 것을 정리하므로, BigQuery 드라이버를 import하지 않는
바이너리는 BigQuery도, Arrow도, Storage Write API도 담지 않습니다. `database/sql`이
항상 동작해 온 방식입니다.

들리는 만큼의 가치가 있습니다.

| 빌드 | 패키지 | 바이너리 |
|---|---|---|
| sink 여섯 개 전부, 오브젝트 스토어 둘, metastore 둘 | 약 900 | 55.0 MB |
| `postgres` + 로컬 `files` | 약 240 | **10.2 MB** |

오브젝트 스토어는 **두 번째** 레지스트리입니다. 스킴은 목적지가 아니기 때문입니다. `files`는
디렉터리, `gs://`, `s3://`에 쓰는 하나의 sink이며, 로컬에만 쓰는 빌드가 그 때문에 AWS
SDK를 포함해서는 안 됩니다.

### 게시된 이미지 두 가지

| 이미지 | 포함하는 것 | pull 크기 (amd64) |
|---|---|---|
| `areteacademy/brevis-gateway:0.32.0` | sink 여섯 개, S3, GCS, Redis, memcached | 19.1 MB |
| `areteacademy/brevis-gateway:0.32.0-slim` | `postgres`, 로컬 `files`, `auto_table` | **5.2 MB** |

하나의 트리를 한 번 빌드해서 만들므로 두 태그는 항상 같은 커밋입니다. `latest-slim`은
없습니다. `latest`는 이미 아무도 배포해서는 안 되는 태그이기 때문입니다.

**다른 조합이 필요한가요?** `cmd/gateway-slim`은 열다섯 줄이며 import 블록이 구성의
전부입니다. 복사해서 import를 바꾸고 빌드하세요. hook을 원하는 사람은 이미 자기 바이너리를
컴파일하고 있으므로, sink를 고르는 데 추가 비용이 들지 않습니다.

### 거부 메시지는 지금 어떤 빌드를 쓰고 있는지 알려 줍니다

```
sink type "bigquery" is not one this binary carries (it has: files, postgres).
Sinks are compiled in, so this is a build that left it out rather than a
destination that does not exist.
```

고정된 목록이라면 그 사람은 자신이 하지 않은 구성 실수를 찾아 헤맸을 것입니다. 오브젝트
스토어도 마찬가지입니다. S3 백엔드가 없는 바이너리의 `s3://` dead letter는 묻어야 하는
첫 번째 배치가 아니라 **시작 시점에** 스킴을 명시하며 거부됩니다.

## 하나의 라우트, N개의 테이블, 선언은 없음

```yaml
sink:
  type: auto_table
  shape: columns
  naming: {pattern: '^(app|svc)_[a-z0-9_]{2,40}$', allow: [app_, svc_], max_new_per_hour: 20}
  metastore: {type: redis, addr_from: BREVIS_METASTORE_ADDR, ttl: 60s}
  into: {type: bigquery, project: acme-prod, dataset: landing, write: merge}
```

`auto_table`은 **라우팅만 하고 쓰지 않습니다**. 쓰는 것은 `into`이며 `table`을 갖지
않습니다. 테이블은 각 이벤트에서 옵니다. `table`이 있는 `into`는 시작 시점에 거부됩니다.
라우터 아래의 고정 테이블은 조용히 이겨서 모든 이벤트가 거기에 적재되기 때문입니다.

### envelope

본문은 **envelope**입니다. 제어 필드는 최상위에, 레코드는 `data` 안에 있습니다.

```json
{
  "table_name": "app_orders",
  "description": "App orders",
  "operation": "INSERT",
  "unique_key": "id",
  "data": {
    "id": "A-1",
    "total": 150,
    "customer": {"id": 7, "uf": "SP"},
    "items": [{"sku": "X", "qty": 2}]
  }
}
```

| 필드 | | 의미 |
|---|---|---|
| `table_name` | **필수** | 적재될 위치 |
| `data` | **필수** | 레코드이며 레코드만 담습니다 |
| `unique_key` | 기본값 `id` | `data`의 어떤 필드가 레코드를 식별하는지. **`merge`에서는 필수** |
| `operation` | 기본값 `INSERT` | `INSERT`, `UPDATE`, `DELETE` |
| `description` | 선택 | **받아들이지만 아직 사용하지 않음**. 아래를 참고하세요 |

`total`, `customer` 옆에 있던 `table_name`은 **레코드**의 필드인 척하는 **전송**의
필드였습니다. 둘을 분리하는 것은 모든 CDC 형식이 구성되는 방식이며, 접두사를 예약할 수
있게 해 주는 것도 이것입니다. 이제 프로듀서만 쓰는 곳이 한 군데이기 때문입니다.

### 키는 envelope가 아니라 `merge`에 속합니다

`unique_key`는 선택 사항이며 **필수인지**는 쓰기 모드에 달려 있습니다. 두 모드가 원하는
것이 다르기 때문입니다.

| | |
|---|---|
| `write: merge` | **필수.** 이 모드는 레코드당 행 하나를 유지하기 위해 존재하고, "레코드당"의 의미가 `brevis_record_key`입니다. 현재 버전을 결정하는 `qualify`가 이것으로 파티션을 나눕니다. 어떤 레코드인지 밝히지 않는 행으로 가득한 merge 테이블은 아무도 해석할 수 없습니다. |
| `write: append` | **선택.** append 테이블은 로그이고, 로그 항목이 반드시 레코드에 *관한* 것일 필요는 없습니다. 감사 라인, 웹훅, 메트릭 샘플이 그렇습니다. 여기서 `id`를 요구하면 프로듀서가 하나를 지어내게 되고, 진짜처럼 보이므로 `NULL`보다 나쁩니다. |

`append`에서는 컬럼이 null 허용으로 만들어지고 키 없는 행은 `NULL`을 갖습니다. `""`가
아니라 `NULL`인데, 서로 다른 사실이기 때문입니다. 그리고 `brevis_ingestion_id`는 여전히
존재합니다. 키가 없으면 **콘텐츠가 곧 키**이므로 같은 문서를 두 번 보내면 같은 id입니다.

**`data`에 없는 필드를 지정하는 것은 두 모드 모두에서 오류입니다.** "말하지 않았다"와
"거기 없는 것을 말했다"는 서로 다른 실수이며, 첫 번째만 통과합니다.

```json
{"accepted":0,"rejected":["event 0: \"unique_key\" names \"pedido_id\" as this
 record's identity and \"data\".\"pedido_id\" is missing or empty. Drop
 \"unique_key\" to fall back to \"id\", or send the field"]}
```

**`description`은 읽히지만 현재는 어디에도 쓰이지 않습니다.** 아직 작성되지 않은 Console의
수집 페이지를 위해 계약에 들어 있습니다. 조용히 받아들인 필드는 누군가 저장되고 있다고
믿는 필드이기 때문에 여기에 밝혀 둡니다.

### 고정 컬럼 여덟 개, 그리고 레코드가 담고 있는 것

| 컬럼 | | |
|---|---|---|
| `brevis_ingestion_id` | `STRING` | 아이덴티티이자 merge 키 |
| `brevis_record_key` | `STRING` | `data[unique_key]` — 어떤 **레코드**에 관한 것인지. `append`에서는 `NULL` |
| `brevis_operation` | `STRING` | `INSERT`, `UPDATE`, `DELETE` |
| `brevis_received_at` | `TIMESTAMP` | 도착 시각, **우리** 시계 기준 — 파티션 컬럼 |
| `brevis_loaded_at` | `TIMESTAMP` | 쓰기 시각, **목적지**가 기록 |
| `brevis_stream` | `STRING` | 어떤 라우트가 썼는지 |
| `brevis_gateway` | `STRING` | 어떤 배포인지 |
| `brevis_received_bytes` | `INT64` | 이벤트가 envelope를 포함해 **도착했을 때**의 크기 |

이 여덟 개는 이 sink의 것이 아니라 **SDK의 것**입니다. `sdk.LandingSchema`가 선언하고
`sdk.LandingID`가 아이덴티티를 만들므로, 파이프라인도 같은 id로 같은 테이블을 적재할 수
있습니다. 정의는 하나입니다. 둘이면 어긋나기 때문입니다.
[SDK의 랜딩 레이아웃](/ko/docs/sdk/#랜딩-레이아웃)을 참고하세요.

**`brevis_`는 예약되어 있습니다.** 해당 접두사의 키를 가진 `data`는 **이벤트별로**
거부됩니다. 그렇지 않으면 프로듀서가 제어 필드를 위조하게 되는데, 위조된
`brevis_received_at`은 진짜처럼 보이므로 없는 것보다 나쁩니다.

**`brevis_loaded_at`은 데이터베이스의 `DEFAULT`**이며 게이트웨이가 보내는 값이 아닙니다.
게이트웨이는 **디스패치** 시각을 알고 목적지는 **쓰기** 시각을 알며, 두 컬럼 사이의 간격이
아무것도 계측하지 않고도 행별로 얻는 실제 종단 간 지연입니다.

**`brevis_received_at`은 파티션 컬럼이며 클라이언트의 시계가 아닙니다.** 프로듀서의 시계는
틀리거나 없을 수 있고, 클라이언트가 통제하는 파티션 컬럼은 클라이언트가 2035년에 쓸 수
있다는 뜻입니다. 파티션이 없는 랜딩 테이블은 늘어나기만 하는 쿼리 비용입니다. 클러스터링은
`brevis_record_key`로 하는데, 이런 테이블로 사람들이 하는 일이 레코드 하나의 이력을 읽는
것이기 때문입니다.

하나의 선언이 모든 목적지에 쓰입니다. SDK의 DDL 생성기가 각 타입을 변환하기
때문입니다. `STRING`은 Postgres에서 `TEXT`, MySQL에서 `LONGTEXT`, BigQuery에서 `STRING`이
되고, `JSON`은 `JSONB`, `JSON`, `JSON`이 됩니다.

### 볼륨을 측정할 인프라 없이 볼륨 보기

`brevis_received_bytes`는 **공짜**입니다. 게이트웨이는 `buffer.flush.size`를 구동하려고
디코딩 시 이 바이트를 이미 세고 있으며, 지금까지는 그 숫자가 거기서 사라졌습니다. 레코드만
따로 측정하려면 이벤트마다 `json.Marshal`이 필요한데, 파싱이 이미 쓰는 3.1 µs 대비 2.0 µs로
**핫 패스가 67% 더 걸리고**, 그 목적은 추세와 귀속을 보는 숫자를 다듬는 것입니다.

이 컬럼은 메트릭이 **구조적으로 답할 수 없는 것**에 답합니다. 모든 `brevis_gateway_*`
시리즈는 *stream* 단위이며 `table` 레이블이 없는데, 의도된 것입니다. `auto_table`에서는
라우트 하나가 테이블 N개가 되고 그 레이블은 프로듀서가 정하기 때문입니다. 그래서 "어떤
테이블이 늘어나고 있고 언제부터인가"에는 어디에도 답이 없었습니다.

```sql
select date(brevis_received_at) day,
       count(*) events,
       sum(brevis_received_bytes)/1e9 gb
from app_orders
group by 1 order by 1 desc
```

이 컬럼이 없으면 같은 질문에 JSON 컬럼 전체를 스캔해야 하며,
`sum(length(to_json_string(data)))`는 누군가 물을 때마다 스캔하는 바이트가 대략
**쉰 배**입니다. 컬럼은 첫 번째 쿼리에서 본전을 뽑습니다.

**스토리지가 아니라 인그레스 기준입니다.** 목적지는 행을 타입을 지정하고 압축해서
보관합니다. JSON 400바이트가 디스크에서는 80바이트일 수 있습니다. 이 값을 합산하면 *도착한*
양이 나오며, 보관하는 데 청구되는 양이 아닙니다.

그리고 **프로듀서는 이 값을 쓸 수 없습니다**. 게이트웨이는 hook 이후에 값을 기록하고
envelope에 있던 값은 덮어씁니다. 필드를 보낸 클라이언트는 자기 볼륨을 보고하는 것이고,
그것을 합산하면 아무 의미가 없어집니다.

### 볼륨 시리즈는 옵트인입니다

```bash
BREVIS_INGESTION_METRICS=true
```

```
brevis_gateway_ingested_bytes_total{stream,table}
brevis_gateway_ingested_events_total{stream,table}
```

**기본값은 꺼짐**이며 기본값이 핵심입니다. `table`은 *프로듀서*가 정하는 레이블입니다. 이
문서의 나머지는 모두 운영자가 YAML에 쓴 것으로 레이블이 붙으므로 프로세스가 시작되기 전에
시리즈 수를 알 수 있습니다. 업그레이드했다는 이유로 메트릭 청구서를 발견하는 사람이
없어야 합니다.

파싱할 수 없는 값은 실패가 아니라 **꺼짐**으로 읽습니다. 이것은 관측성이며, 여기서의 오타가
수집 엔드포인트가 시작되지 않는 이유가 되어서는 안 됩니다.

컬럼은 어느 쪽이든 있습니다. 메트릭은 *지금*을 주고, 컬럼은 *언제부터*와 *누가*를 줍니다.

### 두 가지 형태, 하나의 계약

`shape`는 **레코드**가 행에 기여하는 방식을 결정합니다. 프로듀서의 계약은 어느 쪽이든
같고 envelope는 바뀌지 않습니다. 바뀌는 것은 테이블의 모습이며, 이는 프로듀서가 아니라
운영자의 결정입니다.

```yaml
shape: document   # the default
shape: columns
```

**`document`**에서는 `data`가 통째로 `JSON` 컬럼 하나에 들어갑니다.

```
 brevis_ingestion_id | brevis_record_key | … | data
 d7179bfe-…          | A-1               |   | {"id": "A-1", "total": 150, "customer": {…}}
```

생성 이후에는 DDL이 전혀 없습니다. 새 필드는 새 키일 뿐이므로 스키마 변경 쿼터도, write
stream 재오픈도, 레플리카 간 경합도 없으며, 도착한 날부터 쿼리할 수 있습니다. 시장이
수렴한 곳입니다. Fivetran, Airbyte, Snowpipe는 모두 실패할 수 없는 원시 레이어에 적재하고
다운스트림에서 모델링합니다.

**`columns`**에서는 레코드의 각 필드가 자기만의 컬럼을 갖습니다.

```
 id   | total | customer              | items
 A-1  | 150   | {"id": 7, "uf": "SP"} | [{"qty": 2, "sku": "X"}]
```

**스칼라는 `STRING`이 되고, 객체와 배열은 `JSON`이 되며, 어디에도 추론은 없습니다.**
`150`은 숫자로 도착했지만 문자열 `150`이 되었습니다. 한계가 아니라 선택입니다. 오늘은
정수로 도착하고 내일은 소수로 도착하는 필드는 아무도 아무것도 쓰지 않았는데 컬럼의 타입을
바꾸게 되고, 더 이상 맞지 않는 행은 dead letter로 갑니다. 타입을 지정하는 것은
**승격** 경로이며, YAML에 쓰고 diff로 리뷰합니다.

**`null`은 컬럼을 전혀 만들지 않습니다.** 형태가 없으므로 읽을 것이 없습니다. 필드는 값을
가진 첫 번째 이벤트에서 그 값의 형태로 테이블에 나타납니다. `null`을 보낸 이벤트는 그것이
의미한 대로 똑같이 `NULL`로 적재되며, 컬럼이 이미 존재하는 경우(직접 선언했거나 다른
이벤트가 만들었거나)에는 `null`이 빠지지 않고 그 컬럼에 기록됩니다.

**그리고 `JSON` 컬럼은 JSON 값을 받으며 JSON 값의 문자열을 받지 않습니다.** 사소해
보이지만 쿼리할 수 있는 테이블과 맞아 보이기만 하는 테이블의 차이입니다.

```sql
SELECT JSON_TYPE(customer) FROM bronze.orders;     -- object
SELECT JSON_VALUE(customer, '$.uf') ...;           -- SP
```

`JSON_TYPE`이 `string`을 반환하고 `JSON_VALUE`가 NULL을 반환한다면 객체가 두 번
인코딩된 것이며, 컬럼은 객체가 아니라 객체의 텍스트를 담고 있습니다. **0.15.0**까지의
게이트웨이 버전은 BigQuery에서 **두** 형태 모두에 대해 그렇게 동작했습니다. 한 줄짜리
확인 방법과 복구 방법은 [게이트웨이 changelog](https://github.com/AreteAcademy/brevis/blob/master/gateway/CHANGELOG.md)를
참고하세요. Postgres와 MySQL은 서버가 텍스트를 파싱하므로 영향을 받지 않았습니다.

필드 이름은 네 가지 중 가장 좁은 BigQuery의 규칙에 맞아야 합니다. Postgres는 따옴표를
붙이면 거의 무엇이든 받아들이는데, 그것이 함정입니다. 테이블은 거기서 만들어지고, 누군가
stream을 BigQuery로 향하게 하는 날 깨집니다.

두 형태 모두 SDK에 대응하는 기능이 있으며, 일치하는 코드가 아니라 같은 코드입니다.
`sdk.Landing(table, sdk.LandingKey("id"))`는 `document`를 적재하고,
[`sdk.LandingColumns()`](/ko/docs/sdk/#두-가지-형태-레코드-통째로-또는-필드당-컬럼-하나)를
추가하면 `columns`를 적재합니다. 같은 컬럼, 같은 값, 같은 `brevis_ingestion_id`이므로
마이그레이션 없이 stream을 게이트웨이와 파이프라인 사이에서 옮길 수 있고, 둘을 하나의
테이블로 읽을 수 있습니다.

파이프라인도 같은 방식으로 같은 컬럼을 늘릴 수 있습니다. 목적지에
[`Evolve: sdk.EvolveAdditiveFromPayload`](/ko/docs/sdk/#테이블에-없는-필드가-나타났을-때)를
설정하면 배치가 가진 필드가 이 규칙에 따라 이 코드를 통해 컬럼이 됩니다.


**접두사는 설정할 수 있으며, 프로세스 전체에 대한 하나의 결정입니다.**
`BREVIS_LANDING_PREFIX=acme`로 설정하면 `acme_ingestion_id` 등이 됩니다. 이 게이트웨이의
모든 stream에 적용되므로, 두 sink가 함께 쓸 수 있는 테이블에 대해 서로 다를 수 없습니다.
위의 예약 규칙도 함께 이동합니다. `acme_` 아래에서는 `acme_region`을 보내는 프로듀서가
거부되고 `brevis_region`은 평범한 필드입니다.

**첫 번째 테이블을 만들기 전에 선택하세요.** 나중에 바꾸면 컬럼 여덟 개가 추가되고 여덟 개가
버려집니다. 어떤 것도 컬럼을 삭제하지 않기 때문입니다. 그래서 한 접두사로 레이아웃을 이미
가진 테이블은 다른 접두사를 선언한 적재를 거부합니다. 정규화와 해결 방법은
[SDK 페이지](/ko/docs/sdk/#brevis-접두사는-바꿀-수-있습니다)를 참고하세요.

**중첩 객체의 필드에 자기만의 컬럼을 줄 수 있습니다.** 게이트웨이 프로세스에서
`BREVIS_NORMALIZE_DATA=true`로 설정하면 한 단계를 평탄화하여 `{"name": {"first": "x"}}`가
`name_first`로 적재되며, 모든 stream이 이를 공유하므로 두 sink가 함께 쓸 수 있는
테이블에 대해 서로 다를 수 없습니다. 켜기 전에 읽어야 할 부분인, 무엇의 이름이 바뀌는지는
[SDK 페이지](/ko/docs/sdk/#중첩-필드당-컬럼-하나)를 참고하세요.

**BigQuery에서 `nationalID`와 `nationalId`는 하나의 컬럼입니다.** BigQuery는 컬럼 대소문자를
접으므로, 한 flush에서 `DELETE`에는 한 표기가, `INSERT`에는 다른 표기가 있으면
`CREATE`를 실패시키지 않고 정렬 순서상 첫 번째인 **하나의** 컬럼을 만듭니다. 이벤트 하나가
둘을 모두 가지고 있으면 두 필드를 명시하며 거부됩니다. Postgres와 MySQL은 둘을 별개의
컬럼으로 유지하며, 그쪽에서는 그것이 맞습니다. 어느 표기가 이기는지와 마이그레이션되지
않는 것은 [SDK 페이지](/ko/docs/sdk/index.md)를 참고하세요.

### `UPDATE`와 `DELETE`는 적용되지 않고 기록됩니다

`brevis_operation`은 컬럼입니다. 랜딩 테이블은 **이력**입니다. 제자리에서 적용한
`UPDATE`는 이전 버전을 잃게 하고, 그 버전이 필요한 날은 무언가 깨진 날입니다. "각
레코드의 현재 버전"을 결정하는 것은 다운스트림 모델의 일입니다.

```sql
qualify row_number() over (partition by brevis_record_key
                           order by brevis_received_at desc) = 1
   and brevis_operation <> 'DELETE'
```

Debezium, Fivetran, Airbyte가 하는 방식이며, CDC가 쓰기 경로에 **아무 비용도** 들이지
않는다는 뜻입니다. upsert 모드도, 락도, 삭제도 없습니다.

### 아이덴티티에는 시계가 없습니다

```
brevis_ingestion_id = uuid5(auto_table | table | data[unique_key] | sha256(canonical(data)))
```

공식에 `occurred_at`이 없으며 의도된 것입니다. 도착이 **우리** 책임이 되었고 우리의
시계는 `time.Now()`이므로 전달할 때마다 다릅니다. 시계가 들어간 아이덴티티는 모든 재전달을
새 이벤트로 만들고 `merge`는 아무것도 흡수하지 못하게 됩니다.

```
same record, same content, twice  →  same id, merge absorbs
same record, content changed      →  a different id, both versions land
```

주의 사항은 문서화된 그대로입니다. 바이트 단위로 동일한 `data`를 가진 진짜로 별개인 두
이벤트는 하나로 합쳐집니다. CDC에서는 그것이 **맞습니다**. 같은 값으로 한 레코드에 대한
동일한 두 업데이트는 같은 사실입니다.

지문은 **정규** 형태의 문서에 대해 계산합니다. 모든 레벨에서 키를 정렬하고, 배열은 순서를
유지하고, 모든 것에 따옴표를 붙입니다. Go는 맵 순회를 무작위화하므로 단순한 해시는 같은
이벤트의 두 전달 사이에서 달라질 텐데, 그것이 해시가 막으려는 바로 그것입니다.

### 테이블은 스스로 컬럼을 늘립니다

테이블에 없는 필드를 가진 `data`는 컬럼이 나타나게 합니다. **추가만 하며, 추가만 합니다.**
삭제되는 것도, 좁혀지는 것도 없습니다.

BigQuery에서는 **0.13.0**부터 사실이 되었습니다. 그 전에는 `auto_table`이 모든 목적지에
`Evolve: additive`를 선언했지만 Postgres와 MySQL 드라이버만 그 선언을 읽었습니다. 그래서
거기서는 형태가 생성 시점에 고정되었고 아무도 그렇다고 알려 주지 않았습니다.

> **0.11.0 또는 0.12.0에서 올라오는 경우**, `0.11.0` 이전에 만든 테이블은 일회성
> 마이그레이션이 필요합니다. 여덟 번째 고정 컬럼이 테이블이 아니라 선언에 들어갔기
> 때문입니다.
>
> ```sql
> ALTER TABLE `<dataset>.<table>` ADD COLUMN IF NOT EXISTS brevis_received_bytes INT64
> ```
>
> Postgres와 MySQL은 영향을 받은 적이 없습니다.

`ALTER` 경쟁에서 진 배치는 실패하고, 파이프의 일반적인 재시도가 해결합니다. DDL을 기다리는
배치와 worker를 기다리는 배치는 같은 것이므로 두 번째 버퍼는 없습니다. 레플리카 간에는
metastore가 테이블과 형태마다 1초의 *디바운스*를 유지하여, 같은 새 필드를 본 레플리카
열 개가 BigQuery의 테이블당 10초에 메타데이터 작업 다섯 건이라는 쿼터를 상대로 `ALTER`
열 건이 되지 않게 합니다.

**락이 아니라 디바운스입니다.** 해제되는 것도 갱신되는 리스도 없습니다. 지는 쪽은
막히는 것이 아니라 *재시도*되며, 디바운스를 쥔 채 죽은 레플리카가 다른 레플리카에게 입히는
비용은 1초입니다. 시장이 하는 방식입니다. Delta Lake와 Iceberg는 낙관적으로 커밋하고
재시도하고, Kafka Connect는 파티션 순서에서 직렬화를 얻고, Fivetran은 테이블당 writer를
하나만 둡니다.

### 이름이 공격 표면입니다

`auto_table`은 페이로드의 문자열을 DDL로 바꾸므로, `naming` 아래의 모든 필드는 거부
규칙입니다.

- **`pattern`**의 기본값은 `^[a-z][a-z0-9_]{2,48}$`로, Postgres, MySQL, BigQuery,
  Redshift가 따옴표 없이 받아들이는 것의 교집합입니다. 통과하는 이름은 어디서도 따옴표가
  필요 없고 인젝션을 담을 수 없습니다.
- **`allow`**는 접두사로 더 좁힙니다.
- **`max_new_per_hour`**(기본값 20)는 롤링 1시간 동안의 **생성**을 제한하며 쓰기는
  제한하지 않습니다. 이미 존재하는 테이블은 속도 제한을 받지 않습니다. `metastore:
  memory`에서는 예산이 프로세스에 있으므로 레플리카 네 개는 이 값의 네 배를 허용하고,
  `redis`나 `memcached`에서는 카운터 하나를 공유합니다.

규칙을 벗어나는 이름은 응답에서 **이벤트별로** 거부되며, 같은 요청의 올바른 형식의 이벤트는
모두 그대로 적재됩니다.

```json
{"accepted": 0, "rejected": ["event 0: the table name \"orders\" does not match ^(app|svc)_[a-z0-9_]{2,40}$"]}
```

의도적으로 배치별이 아니라 이벤트별입니다. 쓰기 시점에 거부하면 배치 전체가 실패하여, 한
프로듀서의 잘못된 이벤트 하나가 같은 flush 윈도우의 다른 모든 프로듀서의 이벤트를 묻어
버리고 누구에게도 알려지지 않습니다.

**`listen.auth`가 없는 엔드포인트에서 `auto_table`은 거부됩니다.** `BREVIS_ENV=local`에서도
마찬가지입니다. 테이블 이름을 지정할 수 있는 프로듀서는 테이블을 만들 수 있으므로, 일반
stream이라면 필요 없을 곳에서도 인증이 필요합니다. NetworkPolicy는 이를 대신하지
못합니다. 포트에 누가 접근하는지를 제한할 뿐 어떤 테이블 이름을 요청하는지는 제한하지
않기 때문입니다.

### metastore는 캐시입니다

이벤트별 쓰기가 이벤트별 조회가 되지 않게 하려고 존재하며, 세 가지를 보관합니다. 어떤
테이블이 존재하는지, DDL *디바운스*, `max_new_per_hour` 뒤의 카운터입니다.

캐시이며 진실의 원천이 **아닙니다**. 테이블이 존재하는지는 목적지가 결정하고, *N*개의
레플리카가 테이블을 만들려고 경쟁하는 것이 일반적인 경우입니다. `AlreadyExists`는 성공이며,
이것은 경쟁을 줄일 뿐입니다. TTL은 캐시가 **틀릴 수 있는** 시간의 길이입니다.

| 백엔드 | 용도 |
|---|---|
| `memory` | 기본값이며 아무것도 필요 없습니다. 레플리카가 **하나**일 때는 맞는 답입니다 |
| `redis` | `addr_from`이 필요합니다. `SETNX`가 클레임이고 `INCR`이 카운터입니다 |
| `memcached` | `addr_from`이 필요합니다. `Add`가 클레임이며 만료는 **초** 단위입니다 |

**라우팅 키가 있는 stream에서는 Redis가 더 빠르며**, 그 차이는 형용사가 아니라 숫자로 말할
가치가 있습니다. 트리거가 테이블별이면 윈도우는 테이블마다 클레임 하나를 요청합니다. Redis는
94개를 파이프라인된 한 번의 왕복으로 답하고, 다른 모든 백엔드는 32로 제한된 병렬로 답합니다.
25 ms 떨어진 백엔드에 대해 클레임 94건을 측정한 결과입니다.

| 백엔드 | 하나씩 | 실제 동작 방식 |
|---|---:|---:|
| `redis` | 2.589 s | **29.8 ms** |
| `memcached` | 2.577 s | **89.4 ms** |

세 번의 웨이브 대 한 번의 왕복입니다. 둘 다 클레임에 주어진 500 ms 안에 있으므로,
memcached는 더 느린 선택이지 거부되는 선택이 아닙니다.

레플리카가 여럿이면 `memory`는 각자 자기 것을 갖습니다. 디바운스가 아무것도 디바운스하지
못하고 `max_new_per_hour`는 프로세스 하나를 제한합니다. 나머지 둘이 존재하는 유일한
이유입니다.

**내려간 metastore가 쓰기까지 내려가게 해서는 안 됩니다.** 오류가 나는 `Get`은 미스이고,
오류가 나는 클레임은 이긴 것처럼 동작하며, 오류가 나는 카운터는 제한을 완화합니다. 수집을
멈출 수 있는 캐시는 캐시가 없는 것보다 나쁩니다.

**그리고 "내려감"에는 "느림"도 포함됩니다.** 클레임은 버퍼의 락을 쥔 채 실행되므로, 응답에
2초가 걸리는 백엔드는 그 stream이 2초 동안 아무것도 수락하지 못한다는 뜻입니다. 모든
호출은 **500 ms**로 제한되며 그 이후에는 fail-open입니다. Redis는 호출자의 context로
이를 따르고, `gomemcache`는 context를 전혀 받지 않으므로 memcached는 소켓 데드라인으로
따릅니다. 두 클라이언트 중 하나는 중단하라고 말할 수 없으므로, 같은 한도에 두 가지 방법으로
도달합니다.

**`ttl: 0`은 만료 없음을 뜻하며**, 아무것도 쓰지 않으면 1분의 기본값을 사용합니다. 상태는
둘이 아니라 셋입니다.

틀린 항목에서 복구하는 것은 시계가 **아닙니다**. 목적지가 거부한 쓰기는 `invalidate`를
호출하여 항목을 즉시 버리고, 파이프 자체의 재시도가 차가운 캐시를 상대로 돌아오게 합니다.
손으로 삭제한 테이블의 비용은 1분이 아니라 **실패한 시도 한 번**입니다.

만료에는 대가가 있습니다. `sinkFor`는 테이블을 **알지** 못할 때 `naming.max_new_per_hour`를
차감하므로, 미스는 몇 주 전부터 존재한 테이블에 대해서도 차감하고, 테이블의 마지막 이벤트
1분 뒤에 재시작하는 pod는 비용을 치르는 공유 백엔드에서 아무것도 얻지 못합니다. 오래
유지되는 테이블에서는 `ttl: 0`이 답입니다.

> `0.12.0`과 `0.13.1`에서는 그 표기가 **파싱되지 않습니다**. YAML이 맨 0을 정수로 읽기
> 때문이며, 둘 다 받아들이는 첫 버전은 `0.13.2`입니다. 해당 버전에서는 `ttl: 0s`로
> 쓰세요.

`addr_from`은 주소가 아니라 주소를 담은 **환경 변수**의 이름입니다. 주소에 비밀번호가
들어 있는 경우가 흔하기 때문입니다.

### BigQuery에는 flush 하한이 있습니다

**BigQuery는 테이블당 하루 load job 1,500건을 허용합니다.** 기본 1초 윈도우는 86,400건으로
쿼터의 57배이며 약 25분 만에 소진됩니다. Pub/Sub에서는 문제가 아니고, 그 숫자는 Pub/Sub
구성에서 *건너와서* 여기에 도착할 것입니다.

그래서 BigQuery에 직접 또는 라우터를 통해 쓰는 stream은 윈도우가 60초 미만이면 **로드
시점에 거부됩니다**. 진짜 해법은 아직 작성되지 않은 Storage Write API이며, 그때까지는 첫
배포에서 알게 되도록 두지 않고 지킬 수 없는 윈도우를 구성이 거부합니다.

`auto_table`은 **Redshift**로 라우팅할 수 없습니다. 해당 드라이버는 테이블을 만들지 않으므로
모든 새 이름이 로드에서 실패합니다. 시작 시점에 이름을 명시하며 거부됩니다.

## 아직 존재하지 않는 것

`sqlite`, `dynamodb`, `kinesis`, `sqs`, `sns`, `kafka`, `rabbitmq`는 계획 중이며 아무것도
작성되지 않았습니다. (`redis`와 `memcached`는 **metastore**로 존재하는데 이는 다른
것입니다. 테이블에 대해 알려진 것의 캐시이며, 목적지가 아닙니다.) 이 페이지 맨 위의 표가
각각의 상태를 알려 주며, 상태가 바뀔 때 갱신됩니다. 그 전에는 갱신되지 않습니다.
