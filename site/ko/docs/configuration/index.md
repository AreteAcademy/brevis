# 구성

> 모든 환경 변수의 기본값, 형식, 누락되었을 때의 동작입니다.

*https://brevis.sh/ko/docs/configuration/ · brevis.sh docs (ko)*

---

모든 구성은 **환경**에서 오며 부팅 시 한 번 읽습니다. 그 이후에는 어떤 것도 환경을
참조하지 않습니다. 실행 도중 구성이 바뀌는 프로세스는 동작을 아무도 재현할 수 없는
프로세스이기 때문입니다.

## 필수

| 변수 | |
|---|---|
| `BREVIS_DATABASE_URL` | Postgres. 데이터베이스를 사용하는 하위 명령 다섯 개에서 누락되면 **부팅 오류**입니다 |

## 프로세스

| 변수 | 기본값 | |
|---|---|---|
| `BREVIS_ENV` | `production` | `local`은 텍스트로 로그를 남기고 인터페이스를 열어 둡니다. 그 외의 값(설정하지 않은 경우 포함)은 자격 증명이 필요하며 JSON으로 로그를 남깁니다. 개발 시에는 `BREVIS_ENV=local`을 설정하세요 |
| `BREVIS_HTTP_ADDR` | `:8080` | 수신 주소 |
| `BREVIS_LOG_LEVEL` | `info` | |
| `BREVIS_SHUTDOWN_TIMEOUT_SECONDS` | `15` | 정수. 숫자가 아닌 값은 부팅 오류입니다 |
| `BREVIS_DB_MAX_CONNS` | 드라이버 기본값, `max(4, CPUs)` | 각 프로세스의 Postgres 풀 상한. 숫자가 아니거나 음수인 값은 부팅 오류입니다 |
| `BREVIS_CONSOLE_STATEMENT_TIMEOUT_SECONDS` | `0` (꺼짐) | 콘솔(`brevis serve`) 전용입니다. 실행 시간이 더 길어지는 구문은 Postgres가 중단시켜, 폭주한 목록 쿼리가 연결을 붙잡지 못하게 합니다. 풀이 시작 매개변수를 받는 환경에서는 `30`을 권장합니다. 트랜잭션 모드의 PgBouncer는 그렇지 않으면 연결을 거부하므로 기본값은 꺼짐입니다. 연결 시작 매개변수로 전달되므로, 트랜잭션 모드의 PgBouncer에는 `ignore_startup_parameters = statement_timeout`이 필요합니다 |
| `BREVIS_RETENTION_TRIM_DAYS` | `0` (꺼짐) | 선택적으로 켜는 예약 prune으로, `brevis scheduler`가 실행합니다. 이 값보다 오래된 run의 로그, 단계, 출력을 비웁니다. `brevis prune --trim-after`와 효과가 같습니다. `0` = scheduler는 prune하지 않습니다 |
| `BREVIS_RETENTION_PURGE_DAYS` | `0` (안 함) | 위 변수와 함께 쓰면 이 값보다 오래된 run도 삭제합니다. trim 값 이상이어야 합니다 |
| `BREVIS_RETENTION_INTERVAL_HOURS` | `24` | 예약 prune을 실행하는 주기(시작할 때 한 번, 이후 N시간마다) |
| `BREVIS_BRAND_FILE` | `brand.yaml` | 브랜딩. 없으면 기본값을 사용합니다 |
| `BREVIS_UI_URL` | — | 알림에 들어가는 run 링크의 기준 주소 |
| `BREVIS_METRICS_ADDR` | — | Prometheus 메트릭 포트. 비어 있으면 노출하지 않습니다. [관측성](/ko/docs/observability/index.md)을 참고하세요 |
| `BREVIS_SLACK_WEBHOOK` | — | 실패 알림의 목적지. 비어 있으면 아무에게도 알리지 않습니다 |

## 인증

| 변수 | |
|---|---|
| `BREVIS_AUTH_USER` | 인터페이스 사용자 |
| `BREVIS_AUTH_PASSWORD_HASH` | `brevis hash`로 생성한 `pbkdf2-sha256$…` 해시 |
| `BREVIS_AUTH_SECRET` | 세션 서명용 32바이트 이상(`openssl rand -base64 48`) |

세 변수는 함께 설정하거나 모두 설정하지 않아야 합니다.

| 변수 | 기본값 | |
|---|---|---|
| `BREVIS_AUTH_CLIENT_IP_HEADER` | — | 프록시가 클라이언트 주소를 담아 설정하는 헤더. 예: `X-Forwarded-For`. **마지막** 항목(프록시가 덧붙인 항목)을 사용합니다. 비어 있으면 = 연결의 주소 |

로그인은 클라이언트 주소별로(IPv6는 /64 기준) 제한됩니다. 세 번 실패하면 해당 주소가 2초 동안 잠기고, 잠금 시간은 최대 15분까지 두 배씩 늘어납니다. **ingress나 로드 밸런서 뒤에서는 이 변수를 설정하세요**(`api.ingress.enabled`가 켜져 있으면 Helm chart가 설정하며, `auth.clientIPHeader`로 덮어쓸 수 있습니다). 설정하지 않으면 모든 클라이언트가 프록시의 주소를 공유하므로, 한 사람이 비밀번호를 추측하는 것만으로 모두가 잠깁니다. 이 경우 `BREVIS_ENV=local`이 아니면 잠금은 30초로 제한되고 부팅 시 경고가 로그에 남습니다. 헤더를 신뢰할 수 있는 프록시가 기록하고 클라이언트가 위조할 수 없는 경우에만 이 변수를 설정하세요. 주소당 비밀번호 확인은 한 번에 하나만 실행되므로, 같은 NAT 뒤의 두 사람이 같은 초에 로그인하면 잠시 429가 보일 수 있습니다.

:::danger 일부만 구성하면 부팅 오류입니다
사용자 이름을 채운 사람은 문이 잠겼다고 믿습니다. 인터페이스는 파이프라인을
트리거합니다. `POST /workflows/<slug>/trigger`는 웨어하우스에 쓰는 `dbt build`를
실행합니다. 인터넷에 열려 있다면 웨어하우스의 리모컨이 되며, 로그 경고로는 그 믿음을
바로잡을 수 없습니다. 정상 동작하는 프로세스의 로그를 읽는 사람은 없기 때문입니다.
:::

`BREVIS_ENV=local`이 아닌 환경에서는 자격 증명 없이 시작하는 것이 거부됩니다.

```
error: BREVIS_ENV=production requires a credential: set BREVIS_AUTH_USER,
BREVIS_AUTH_PASSWORD_HASH (generate one with `brevis hash`) and BREVIS_AUTH_SECRET
```

:::note 이전 이름도 여전히 동작합니다
2026-09-10에 변수 네 개의 이름이 바뀌었으며, 프로젝트의 운영자 쪽에서 마지막 포르투갈어
이름이 사라진 때입니다. 대상은 `BREVIS_AUTH_USUARIO`, `BREVIS_AUTH_SENHA_HASH`,
`BREVIS_AUTH_SEGREDO`, `BREVIS_POD_MANTER_EM_FALHA`입니다. 이전 이름을 설정한 설치
환경은 계속 동작하며 새 이름을 알려 주는 경고를 로그에 남깁니다.

새 이름을 추가하고, 배포한 다음, 이전 이름을 제거하세요. 둘 다 설정되어 있으면 새
이름이 우선합니다. 이전 이름은 메이저 버전에서 사라집니다.

SDK에서는 `BREVIS_SDK_LIMITE_INLINE`이 같은 규칙으로 `BREVIS_SDK_INLINE_LIMIT`이
되었습니다.
:::

## 작업 환경

| 변수 | 기본값 | |
|---|---|---|
| `BREVIS_TASK_ENV` | 비어 있음 | 각 step이 받는 것 |

작업은 오케스트레이터의 환경을 상속하지 **않습니다**. 이유는 구체적입니다. 프로세스는
Postgres 사용자 이름과 비밀번호가 담긴 `BREVIS_DATABASE_URL`을 가지고 있고, workflow는
다른 사람이 작성한 임의의 명령입니다. 상속하면 모든 파이프라인의 모든 step에 데이터베이스
자격 증명을 건네주게 됩니다.

작업에 필요한 것은 선언합니다.

```bash
BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE   # forwards these two from the process
BREVIS_TASK_ENV=STAGE=prod                # sets a literal
BREVIS_TASK_ENV='*'                       # forwards everything EXCEPT BREVIS_*
```

`PATH`와 `HOME`은 항상 포함됩니다. `PATH`가 없으면 어떤 명령도 해석되지 않고, 그
오류는 아무것도 설명하지 않는 "not found"가 되기 때문입니다.

없는 이름은 빈 문자열이 **되지 않습니다**. `GOOGLE_PROJECT_ID=""`는 변수가 없는 것보다
더 나쁜 메시지와 함께 dbt를 실패시킬 것입니다.

## Kubernetes

workflow가 아니라 **설치 환경**이 내리는 결정입니다.

| 변수 | 기본값 | 형식 |
|---|---|---|
| `BREVIS_PODS` | `auto` | `auto`, `on`, `off`. 그 외는 부팅 오류입니다 |
| `BREVIS_POD_NAMESPACE` | 클러스터의 네임스페이스 | |
| `BREVIS_POD_SERVICE_ACCOUNT` | — | |
| `BREVIS_POD_PULL_SECRETS` | — | 쉼표로 구분한 목록 |
| `BREVIS_POD_ENV_FROM_SECRETS` | — | 쉼표로 구분한 목록 |
| `BREVIS_POD_ENV_FROM_CONFIGMAPS` | — | 쉼표로 구분한 목록 |
| `BREVIS_POD_ALLOWED_SECRETS` | — | 쉼표로 구분한 목록 |
| `BREVIS_POD_CREDENTIAL_PATH` | — | pod에 마운트되는 경로 |
| `BREVIS_POD_CREDENTIAL_PVC` | — | 자격 증명을 담은 PVC |
| `BREVIS_POD_NODE_SELECTOR` | — | `key=value,other=value` |
| `BREVIS_POD_TOLERATIONS` | — | `key=value:effect,…`. `Equal` 연산자만 허용합니다 |
| `BREVIS_POD_KEEP_ON_FAILURE` | `false` | `true`이면 점검을 위해 pod를 남겨 둡니다 |

목록에서 빈 항목은 무시됩니다. `a,,b`는 오타이고, 빈 시크릿 이름은 서버가 pod 전체를
거부하게 만들기 때문입니다.

## 테스트

| 변수 | |
|---|---|
| `BREVIS_TEST_DATABASE_URL` | 통합 테스트용 데이터베이스. 없으면 테스트를 건너뜁니다 |

## 헬스 엔드포인트

| | |
|---|---|
| `GET /health` | liveness — 데이터베이스를 조회하지 **않습니다** |
| `GET /ready` | readiness — 조회하며 실패한 의존 대상을 명시합니다 |

분리는 의도된 것입니다. 외부 서비스에 의존하는 liveness 프로브는 데이터베이스가 흔들릴
때 로드 밸런서에서 빼기만 하는 것이 아니라 Kubernetes가 **pod를 죽이게** 만듭니다.

## 전체 예시

```bash
# required
export BREVIS_DATABASE_URL='postgres://brevis:secret@db:5432/brevis?sslmode=require'

# production requires credentials
export BREVIS_ENV=production
export BREVIS_AUTH_USER=operator
export BREVIS_AUTH_PASSWORD_HASH="$(brevis hash < password.txt)"
export BREVIS_AUTH_SECRET="$(openssl rand -base64 48)"

# what the tasks need, and nothing more
export BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE,DBT_KEYFILE

# pods
export BREVIS_PODS=on
export BREVIS_POD_NAMESPACE=data
export BREVIS_POD_SERVICE_ACCOUNT=brevis-runner
export BREVIS_POD_ENV_FROM_SECRETS=bigquery-cred

# alerts
export BREVIS_SLACK_WEBHOOK='https://hooks.slack.com/services/...'
export BREVIS_UI_URL='https://brevis.example.com'
```
