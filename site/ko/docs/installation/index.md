# 설치

> 바이너리를 받는 방법, 필요한 요구 사항, 설치가 올바른지 확인하는 방법입니다.

*https://brevis.sh/ko/docs/installation/ · brevis.sh docs (ko)*

---

brevis.sh는 **단일 바이너리**와 **Docker 이미지**로 제공됩니다. 함께 설치해야 하는
런타임은 없습니다. 마이그레이션, 인터페이스 에셋, 폰트가 모두 바이너리 안에
들어 있습니다.

## 요구 사항

| | |
|---|---|
| **PostgreSQL** | `serve`, `scheduler`, `migrate`, `publish`, `backfill`에 필요합니다 |
| **Go 1.25+** | 소스에서 빌드하는 경우에만 필요합니다 |
| **Kubernetes** | 선택 사항입니다. 클러스터가 없으면 step이 로컬 프로세스로 실행됩니다 |

`brevis run`, `validate`, `marca`, `hash`, `version`은 데이터베이스가 **필요 없습니다**.

## 소스에서 빌드하기

버전과 커밋이 새겨진 바이너리를 만드는 방법입니다.

```bash
git clone https://github.com/AreteAcademy/brevis.git
cd brevis
make build
./bin/brevis version
```

```
brevis 0.11.2
  commit  bb832ff
  build   2026-09-05T12:00:00Z
  go      go1.25.7 darwin/arm64
```

:::warning 엔진에는 아직 태그된 릴리스가 없습니다
저장소의 모든 태그는 SDK(`sdk/v*`)의 것이며, 루트 모듈에는 태그가 없습니다.
`go install github.com/AreteAcademy/brevis/cmd/brevis@latest`는 브랜치의 최신
커밋으로 해석되어, 커밋도 날짜도 없이 `brevis dev`라고 자신을 소개하는 바이너리를
만듭니다. `-ldflags`는 `make build`와 `docker build`를 통해서만 적용되기
때문입니다. 추적 가능한 버전이 필요하면 이미지나 `make build`를 사용하세요.
:::

## Docker

같은 바이너리로 이미지를 두 가지 제공하는데, 두 역할의 요구가 정반대이기 때문입니다.

| 태그 | 베이스 | 이유 |
|---|---|---|
| `:0.11.2` | distroless | API는 HTTP를 제공할 뿐 아무것도 실행하지 않으므로 셸이 없고 공격 표면이 최소입니다 |
| `:0.11.2-worker` | alpine + tini | `run:` step에는 셸이 필요합니다 |

```bash
docker run --rm areteacademy/brevis:latest version
```

두 이미지 모두 `amd64`와 `arm64`용으로
[hub.docker.com/r/areteacademy/brevis](https://hub.docker.com/r/areteacademy/brevis)에
있습니다.

## 전체 로컬 환경

저장소에는 Postgres, API, 스케줄러를 띄우는 `docker-compose.yml`이 포함되어 있습니다.

```bash
make up      # Postgres + API + scheduler
make smoke   # checks /health and /ready
make logs
make down
```

인터페이스는 `http://localhost:8080`에 있습니다.

## 최소 구성

필수 변수는 하나입니다.

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
```

이 변수가 없으면 데이터베이스를 사용하는 하위 명령은 부팅 시점에 실패하며, 이는
의도된 동작입니다. 자격 증명 없이 올라온 프로세스는 첫 요청에서야 문제를 발견하는데,
그때는 이미 readiness가 오케스트레이터에게 거짓말을 한 뒤입니다.

```
$ brevis migrate status
erro: BREVIS_DATABASE_URL e obrigatoria
```

전체 목록은 [구성](/ko/docs/configuration/index.md)에 있습니다.

## 스키마 적용하기

마이그레이션은 바이너리에 내장되어 있고 별도의 하위 명령으로 적용합니다.
**`serve`는 스키마를 절대 변경하지 않습니다**.

```bash
brevis migrate up
brevis migrate status
```

## 확인하기

```bash
brevis version
brevis validate examples/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencias  cron 0 2 * * *
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

두 명령 모두 응답하면 설치가 올바른 것입니다. [빠른 시작](/ko/docs/quickstart/index.md)으로
넘어가세요.
