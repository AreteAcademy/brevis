# SQL

> 일반 .sql 파일이 의존성 순서대로 테이블과 뷰가 됩니다. workflow step으로도, 단독으로도 사용할 수 있습니다.

*https://brevis.sh/ko/docs/sql/ · brevis.sh docs (ko)*

---

템플릿은 없습니다. 모델은 유효한 SQL로 남는 파일이며, 순서는 SQL 자체를 읽어서 정해집니다. Postgres와 BigQuery를 같은 적합성 스위트로 검증합니다.

![The SQL flow: brevis-sql reads raw.orders, a table the SDK or the Gateway landed. The model staging.stg_orders becomes a view over it, and marts.orders a table over that view; the order comes from reading the SQL. The same models build on Postgres or BigQuery.](/assets/flow-sql.svg)

## 명령 여섯 개

| 명령 | 하는 일 |
|---|---|
| `brevis-sql compile` | 모든 모델을 파싱하고 모든 엣지를 해석하며, **아무 곳에도 연결하지 않습니다** |
| `brevis-sql graph` | 추론된 엣지를 출력하므로, 틀린 엣지를 나중에 발견하지 않고 바로 볼 수 있습니다 |
| `brevis-sql build` | 모든 모델을 의존성 순서대로 생성하거나 교체합니다 |
| `brevis-sql test` | 모든 모델의 테스트를 실행합니다. 각 테스트는 아무것도 찾지 않아야 하는 SELECT입니다 |
| `brevis-sql serve` | 읽기 전용 미리보기와 쿼리에 HTTP로 응답하므로, Console에는 웨어하우스 자격 증명이 필요 없습니다 |
| `brevis-sql generation bump` | 테이블이 다시 빌드되었음을 기록하여 그 위에서 `brevis-sql-api`가 캐시한 응답을 오래된 것으로 만듭니다. `build --generations-from`은 빌드하는 모든 모델에 대해 이를 수행합니다 |

`--select orders+`는 어느 명령이든 모델 하나와 그 다운스트림 전체로 좁혀 줍니다.


## `brevis-sql serve` — 자격 증명 없이 웨어하우스 읽기

Console의 데이터와 SQL 화면은 무엇이 적재되었고 그 안에 무엇이 있는지 보여 줍니다. 둘 다 웨어하우스에 직접 접근하지 않고, 옆에서 실행되며 엔진이 의도적으로 갖지 않는 자격 증명을 가진 이 서비스에 요청합니다.

```bash
brevis-sql serve --connections brevis.yaml --addr 127.0.0.1:8088
```

상한은 예산이 아닙니다. `--max-bytes`는 쿼리 하나를 제한하고 `--budget`은 연결당 시간당 합계를 제한하며, 견적과 대조하여 확인하므로 거부는 비용이 발생하기 전에 일어납니다. 바이트를 보고하는 웨어하우스, 즉 BigQuery만 제한하고 Postgres는 제한하지 않으며, 부팅 배너가 어떤 정책이 적용 중인지(아무 정책도 없음 포함) 알려 줍니다.

**파일에 선언된 것만 접근할 수 있으며**, 기본적으로 어떤 동작도 쓰기가 아닙니다. 모든 문장은 실행 전에 분류되고, `--max-bytes`보다 많이 스캔하는 읽기는 비용이 발생하기 전에 견적을 내어 거부되며, 웨어하우스 자체도 쓰기를 거부하도록 요청받습니다. 감사 라인에는 문장의 해시가 담기며 문장 자체는 담기지 않습니다.

**이제 자체 서비스 계정이 필요하지 않습니다.** `brevis-sql build`는 테이블을 만들므로 그 역할은 쓸 수 있고, 그런 역할을 모두 거부하는 서비스는 만들고, 보관하고, 교체해야 하는 두 번째 계정을 뜻했습니다. 이제 역할만이 방어선을 지키는 것이 아닙니다. Postgres는 각 문장을 `READ ONLY`로 연 트랜잭션 안에서 실행하며, 어떤 분류기도 내부를 볼 수 없는 쓰기인 `SELECT nextval('s')`는 Postgres 자신이 거부하고, BigQuery는 견적을 내는 드라이 런에서 문장의 종류를 보고하므로 `CREATE TABLE`이나 `EXPORT DATA`는 웨어하우스 자신의 판단으로 거부됩니다. 둘 중 어느 것도 제공하지 못하는 웨어하우스는 여전히 쓸 수 있는 자격 증명을 거부합니다.

`BREVIS_ENV=local`이 아닌 환경에서는 토큰이 필요하며 토큰 없이는 서비스가 시작되지 않습니다.

### 누군가 변경해도 되는 개발 웨어하우스

```bash
BREVIS_SQL_SERVE_WRITES=warehouse_dev
```

**연결의 이름을 지정합니다.** 서비스 하나가 여러 웨어하우스에 접근하며, 개발 중인 웨어하우스를 열었다고 옆의 웨어하우스까지 열려서는 안 됩니다. 이름을 지정한 연결에서는 모든 계층이 물러나고 이 서비스는 자격 증명만큼만 강력합니다. 행 상한은 화면을 제한하고 바이트 상한은 비용을 제한하지만, 둘 다 문장이 무엇을 바꾸는지는 제한하지 않습니다.

이 변수가 설정된 곳에서는 토큰이 필요하며 `BREVIS_ENV=local`도 예외가 아닙니다. 부팅 배너가 열린 모든 연결의 이름을 알리고, Console이 에디터 위에 배너를 표시하며, 메트릭에 `brevis_sql_writable_connection`이 나타납니다. **운영 환경에서는 설정하지 마세요.** 이 저장소의 어떤 manifest도 이를 설정하지 않습니다.

토큰에는 **이름을 붙일 수 있습니다**. `BREVIS_SQL_SERVE_TOKEN_CONSOLE=…`는 `console`이라는 호출자를 선언하며, 그 bearer가 만드는 모든 감사 라인에 `"caller":"console"`이 담깁니다. 아이덴티티는 호출자가 채우는 헤더가 아니라 자격 증명이므로, 로그는 해당 토큰의 보유자가 가지고 있지 않았던 것을 말할 수 없습니다. 거부된 토큰도 라인을 남기며, 제시된 값은 절대 기록하지 않습니다.

## 모델은 유효한 SQL로 남는 파일입니다

설정은 맨 앞의 블록 주석이므로, 파일은 하이라이팅이 되는 에디터에서 열리고, Console에서
손으로 실행할 수 있으며, `psql -f`로도 실행됩니다.

```sql
/* brevis
materialized: table
tests:
  - not_null: [order_id]
  - unique: [order_id]
  - relationships: {column: customer_id, to: staging.stg_customers, field: customer_id}
*/
select * from staging.stg_orders
```

**템플릿은 없습니다.** `models/<schema>/<name>.sql`은 `<schema>.<name>`이며, 의존성은
직접 작성해야 하는 함수 호출이 아니라 SQL을 읽어서 얻습니다. `brevis-sql graph`는
추론된 것을 출력하고, 헤더의 `depends_on:`은 드물게 놓친 경우를 위한 명시적 재정의입니다.

## compile에서 test까지

모델 두 개입니다. `raw.orders`를 정리하는 뷰와, 그것에 의존하는 테이블입니다.

`models/staging/stg_orders.sql`:

```sql
/* brevis
materialized: view
*/
select order_id, customer_id, amount, ordered_at
from raw.orders
where amount is not null
```

`models/marts/orders.sql`:

```sql
/* brevis
materialized: table
tests:
  - not_null: [customer_id]
  - unique: [customer_id]
*/
select customer_id, count(*) as orders, sum(amount) as revenue
from staging.stg_orders
group by customer_id
```

`compile`과 `graph`는 아무 곳에도 연결하지 않습니다.

```bash
docker run --rm -v ./project:/project:ro areteacademy/brevis-sql:0.6.0 \
  compile --project /project
```

```text
2 models, 2 to build, 0 function(s)
  staging.stg_orders                 view  0 test(s)
  marts.orders                       table 2 test(s)
```

```bash
docker run --rm -v ./project:/project:ro areteacademy/brevis-sql:0.6.0 \
  graph --project /project
```

```text
staging.stg_orders
  └─ raw.orders (source)
marts.orders
  ├─ staging.stg_orders
```

Postgres를 대상으로 하는 `build`와 `test`입니다.

```bash
docker run --rm -v ./project:/project:ro \
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db areteacademy/brevis-sql:0.6.0 \
  build --project /project --dsn-from BREVIS_SQL_DSN
```

```text
  staging.stg_orders                 view  new   4ms
@brevis:{"type":"landed","target":"postgres://postgres/staging/stg_orders"}
  marts.orders                       table new   4ms
@brevis:{"type":"landed","target":"postgres://postgres/marts/orders","rows":2}
2 models built on postgres
```

```bash
docker run --rm -v ./project:/project:ro \
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db areteacademy/brevis-sql:0.6.0 \
  test --project /project --dsn-from BREVIS_SQL_DSN
```

```text
2 tests passed on postgres
```

게시된 이미지에서 얻은 실제 출력입니다. compile과 graph는 아무 곳에도 연결하지 않습니다. Postgres를 대상으로 하면 build와 test는 "2 models built"와 "2 tests passed"로 끝납니다.

### 테스트가 실패할 때

`raw.orders`에 고객이 없는 주문이 있으면 `build`는 통과하지만 `not_null` 테스트는 통과하지 못합니다. 실패 메시지는 모델, 컬럼, 행 수, 그리고 **쿼리**를 명시하며, 쿼리는 Console에 붙여 넣고 계속 좁혀 나갈 수 있는 것입니다.

```text
FAIL  marts.orders                   not_null         customer_id          1 row(s)
      SELECT COUNT(*) FROM (SELECT customer_id FROM marts.orders WHERE customer_id IS NULL) AS v
brevis-sql: 1 of 2 tests failed
```

프로세스는 코드 1로 종료하며, workflow step도 함께 실패합니다.

## 테스트는 위반 행을 반환하는 SELECT입니다

`not_null`, `unique`, `accepted_values`, `relationships`가 있습니다. 행이 0개면 통과이고, 그 외에 해석할 것은 없습니다.

## 무엇을 썼는지 알려 줍니다

구체화되는 모든 모델은 Brevis 엔진이 이미 읽고 있는 `@brevis:` 라인을 출력하므로, 다른
모든 것과 나란히 `/data`에 나타납니다(위의 `build` 출력을 참고하세요). 뷰는 행이 없으므로
0이 아니라 행 수를 보고하지 않습니다. 0은 실제로 비어 있는 테이블과 구별되지 않기
때문입니다.

## 웨어하우스 둘, 스위트 하나

PostgreSQL과 BigQuery(`--dialect bigquery`)가 있으며, 둘 다 같은 테스트 스위트를
실행합니다. 구현이 하나뿐일 때 작성된 스위트이므로, 두 번째 구현이 조용히 어긋날 수
없습니다.

BigQuery는 Application Default Credentials로 인증합니다. 워크로드 아이덴티티에서는 pod
자신의 아이덴티티이며, `--dsn-from`은 프로젝트 id를 담은 변수를 지정합니다.

## 13 MB

Distroless, nonroot이며 셸이 없습니다. 공식 BigQuery 클라이언트는 빈 바이너리에서 31 MB와
패키지 526개이므로, 이 도구는 REST 엔드포인트로 BigQuery와 통신합니다. 약 200줄이며, 조용히
틀릴 수 있는 방식마다 테스트가 하나씩 있습니다. 저장소의 게이트가 해당 클라이언트를 이름으로
거부하므로, 일상적인 의존성 갱신에서도 이 결정은 유지됩니다.

## dbt와 대조해 검증

jaffle-shop을 dbt가 사용한 것과 같은 시드 데이터로 이 이미지가 빌드했습니다.

|  | 결과 |
|---|---|
| 모델별 모든 행의 행 수와 md5 | **동일, 13개 중 13개** |
| 모델별 컬럼과 타입 | **동일, 13개 중 13개** |
| 모델 13개, 첫 빌드 | 1.56 s |

## 하지 않는 것

**dbt 호환성은 없으며**, 빈틈이 아니라 결정입니다. 실제 dbt 프로젝트의 모델 중 약 3분의
1은 `ref`, `source`, `config` 외에는 아무것도 쓰지 않습니다. 나머지는 매크로를 쓰며,
패키지는 설계상 템플릿입니다. 아무도 예측할 수 없는 절반의 호환성은 없는 것보다 나쁩니다.

## 다음으로 이동

| 원하는 것 | 이동할 곳 |
|---|---|
| 네 가지 구성 요소 중 SQL의 위치 보기 | [에코시스템](/ko/docs/ecosystem/index.md) |
| brevis-sql을 스케줄된 step으로 실행하기 | [Core](/ko/docs/core/index.md) |
| 모델링할 데이터 적재하기 | [Go SDK](/ko/docs/sdk/index.md) 또는 [수집](/ko/docs/ingestion/index.md) |
