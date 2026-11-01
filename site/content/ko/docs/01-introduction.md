---
title: 소개
description: brevis.sh가 무엇이고 어떤 문제를 해결하는지, 그리고 어떤 경우에 맞지 않는 선택인지 설명합니다.
group: 시작하기
order: 1
slug: introduction
---

brevis.sh는 **Go로 작성된 데이터 오케스트레이션 런타임**입니다. 보통 네 가지
도구가 필요한 기능, 즉 선언적 변환, workflow 오케스트레이션, 영속 큐, 스케줄러,
그리고 이 모두를 운영하는 인터페이스를 단일 바이너리 하나에 담았습니다.

MIT 라이선스의 오픈 소스이며, 프로젝트는
[Aretê Academy](https://areteacademy.com.br/)에서 시작되었습니다.

## 문제

일반적인 데이터 스택은 변환 도구, 오케스트레이터, 큐, 인터페이스를 조합합니다.
각 도구는 맡은 일을 잘하지만, 이들 사이의 통합은 끝나지 않는 일이 됩니다.
버전 관리 방식 네 가지, 설정 방식 네 가지, 문제가 생겼을 때 확인할 곳 네 군데가
생깁니다.

이 비용은 아키텍처 다이어그램에는 나타나지 않습니다. 대신 팀만이 아는 도메인에
집중하지 못하고 연결 코드를 유지보수하는 데 쓰는 시간으로 나타납니다.

## 제안

산출물은 하나이고, 역할에 필요한 하위 명령을 사용합니다.

```bash
brevis serve        # API + interface
brevis scheduler    # materialises schedules and drains the queue
brevis run wf.yaml  # runs now, no database
```

workflow는 버전 관리되는 YAML 파일입니다. 파이프라인을 리뷰한다는 것은 클릭 세션을
보는 것이 아니라 diff를 읽는 일이 됩니다.

```yaml
name: ingest
schedule: "0 5 * * *"

steps:
  - id: bronze
    image: registry.example/dbt:1.10.3
    run: dbt build --select bronze+

  - id: notify
    image: ghcr.io/example/notify:0.3
    run: /notify --channel data
    depends_on: [bronze]
```

## 무엇이 다른가

**모든 step은 자기만의 이미지로 자기만의 pod에서 실행됩니다.** 작업을 기다리는
범용 worker는 없습니다. 작업이 자신의 런타임을 직접 가져옵니다. `dbt` step은 dbt
이미지로 올라오고, 그 옆의 Go fetcher는 5.8 MB와 32Mi 메모리로 올라오므로, 가장 큰
이웃의 크기를 물려받지 않습니다.

**스케줄러는 run을 만들고, 큐는 그것을 실행합니다.** 서로 독립된 두 루프이며, 이
분리는 의도된 것입니다. 한쪽이 멈춰도 다른 쪽이 중단되지 않고, 지난달을 재처리하는
일이 시계에 의존하지 않습니다.

**검증에는 데이터베이스가 필요 없습니다.** `brevis validate`는 CI에서 테스트와 함께
실행되므로, YAML 오류가 클러스터가 아니라 pull request에서 실패합니다.

## 의도적으로 분리된 세 가지 구성 요소

| 구성 요소 | 설명 | 버전 |
|---|---|---|
| **engine** | `brevis`, 이 페이지에서 다루는 바이너리: 오케스트레이션, 스케줄링, 실행, 운영 | `0.15` |
| **SDK** | 추출하고 적재하는 step을 작성하기 위한 Go 모듈 | `0.66` |
| **gateway** | 가져오러 가는 대신 *도착하는* 데이터를 받아 적재하는 HTTP 엔드포인트 | `0.8` |

성숙도가 서로 다르기 때문에 버전도 독립적입니다. engine이 있다는 이유로 gateway를
`0.15.3`이라고 번호 매기면 갖추지 못한 성숙도를 주장하는 셈이고, 한 번 게시한
버전은 취소할 수 없습니다.

세 가지를 하나로 엮는 것은 숫자 하나, 즉 `provider|entity|source_key|record_ts`에
대한 고정된 UUID v5인 `ingestion_id`입니다. 같은 레코드에 대해
[gateway](/docs/ingestion/)가 적재한 행과 [SDK](/docs/sdk/) fetcher가 적재한 행은
*하나의 행*이며, 둘 사이에 조정(reconciliation) 작업이 없습니다.

## brevis.sh가 맞지 않는 경우

도구가 하지 못하는 일을 문서화하는 편이 하는 일을 문서화하는 것보다 시간을 더
절약해 줍니다.

- **이미 Airflow나 Dagster를 운영하고 있고 만족하는 경우.** 바이너리 하나를 덜
  운영해서 얻는 절감보다 마이그레이션 비용이 더 큽니다.
- **workflow가 데이터와 무관한 경우.** brevis.sh는 변환과 적재의 DAG를 전제로 하므로,
  범용 오케스트레이터가 더 적합합니다.
- **미리 만들어진 오퍼레이터 카탈로그가 필요한 경우.** 여기서 step은 이미지와 명령
  하나입니다. 단순하지만 한계도 있어서, 끌어다 쓸 수백 개의 통합은 없습니다.
- **팀이 Kubernetes를 쓰지 않고 쓸 계획도 없는 경우.** 로컬 모드도 동작하지만,
  설계는 pod를 전제로 합니다.

## 열린 코드. 참여 방식은 직접 선택하세요.

brevis.sh는 100% 오픈 소스입니다. 독점 라이선스도, 기능이 제한된 에디션도, 만드는 것에 대한 제약도 없습니다.

### 100% 오픈

*코드 · 오픈 소스 라이선스*

런타임을 읽고, 사용하고, 수정하고, 개선해서 자신의 운영 환경에 적용하세요. 완전한 런타임 · 감사 가능한 코드 · 열린 기여.

[저장소 보기](https://github.com/AreteAcademy/brevis)

### 함께 만들기

*커뮤니티 · 열린 이슈* · **주요 경로**

프로젝트의 방향을 결정하는 논의에 참여하세요. 이슈와 제안 · 동료 간 교류 · 공개 로드맵.

[프로젝트 팔로우](https://github.com/AreteAcademy/brevis/issues)

### 깊이 배우기

*aretê · 목적이 있는 기술*

철학적 사고와 소프트웨어 엔지니어링을 잇는 학교를 만나 보세요. 오리지널 콘텐츠 · 지속적인 학습 · 열린 소통 창구.

[Aretê 방문](https://areteacademy.com.br/)

## 다음으로 이동

| 원하는 것 | 이동할 곳 |
|---|---|
| 설치하고 실행되는 모습 보기 | [설치](/docs/installation/) 및 [빠른 시작](/docs/quickstart/) |
| workflow 형식 이해하기 | [Workflows](/docs/workflows/) |
| 아키텍처 이해하기 | [스케줄러와 큐](/docs/scheduler-and-queue/) |
| 명령과 플래그 목록 | [CLI](/docs/cli/) |
| Go로 fetcher 작성하기 | [SDK](/docs/sdk/) |
| 한 step의 값을 다음 step으로 전달하기 | [step 간 컨텍스트](/docs/context/) |
| Python으로 step 작성하기 | [클라이언트 라이브러리](/docs/libraries/) |
| 데이터를 가져오는 대신 HTTP로 받기 | [수집](/docs/ingestion/) |
| 대시보드와 알림 구성하기 | [관측성](/docs/observability/) |

:::note 이름에 대하여
*Brevis*는 라틴어로 "짧은, 간결한"이라는 뜻이며, *brevity*(간결함)의 어원입니다.
세네카의 **De Brevitate Vitae**(인생의 짧음에 관하여)에서 따왔습니다. 우리에게
시간이 적은 것이 아니라, 많은 시간을 잃어버리고 있다는 뜻입니다.
:::
