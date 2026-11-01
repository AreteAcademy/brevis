# 관측성

> 두 프로세스의 Prometheus 메트릭, 무엇을 지켜봐야 하는지, 그리고 의도적으로 없는 것입니다.

*https://brevis.sh/ko/docs/observability/ · brevis.sh docs (ko)*

---

Brevis는 **두** 프로세스 모두에서 별도의 포트로 Prometheus 메트릭을 노출합니다.

```
GET http://<pod>:9090/metrics
```

포트는 `BREVIS_METRICS_ADDR`에서 옵니다. HTTP 포트와 의도적으로 분리되어 있습니다.
메트릭은 인터페이스의 인증을 거치지 않으며, 둘을 한곳에서 제공하면 인증된 스크레이프와
열려 있는 대시보드 중 하나를 선택해야 합니다.

## 프로세스 두 개, 엔드포인트 두 개

대부분의 구성이 API만 수집해서 틀리는 부분입니다.

| | 제공하는 것 | 보고하는 것 |
|---|---|---|
| `brevis serve` | UI와 HTTP API | 큐 깊이 |
| `brevis scheduler` | HTTP로는 아무것도 없음 | 큐 깊이, 클레임 지연, slot, 고아 run, run, step |

실행하는 것은 스케줄러이므로 거의 모든 것이 스케줄러에서 나옵니다. API만으로 만든
대시보드는 큐가 커지는 것은 보여 주지만, 큐를 비우는 것에 대해서는 **아무것도** 보여
주지 않습니다.

## step이 게시하는 메트릭

엔진이 측정하는 것 외에, step도 자기만의 메트릭을 게시할 수 있습니다. 적재된 행 수,
벤더가 거부한 행 수 같은 것입니다.

```python
from brevis import metrics
metrics.set("rows_loaded", 48213)
```

이 메트릭은 **같은** 스케줄러 `/metrics`에 접두사와 레이블이 엔진에 의해 붙은 채
나타납니다.

```
brevis_step_rows_loaded{workflow="daily_sales",step="load"} 48213
```

step은 포트를 열지 않습니다. stdout에 한 줄을 쓰고 엔진이 기록합니다. 40초 동안 사는
pod는 수집할 수 없기 때문입니다. [Python](/ko/docs/python/index.md)을 참고하세요.

## 무엇을 지켜볼까

| 신호 | 이유 |
|---|---|
| 큐 깊이가 줄지 않고 계속 증가 | 스케줄러가 죽었거나, 부하에 비해 `--concurrency`가 낮습니다 |
| 클레임 지연 증가 | 데이터베이스 경합이거나, 주기에 비해 큐가 너무 큽니다 |
| 고아 run > 0 | 끝나기 전에 죽은 프로세스가 클레임한 run입니다 |
| slot이 구체화되지 않음 | 스케줄러 루프가 멈췄고 스케줄도 함께 멈춥니다 |

앞의 두 신호만으로는 "높은 부하"와 "죽은 프로세스"를 구별할 수 없으며, slot이 중요한
이유가 이것입니다. slot이 만들어지는 **동시에** 큐가 증가하면 부하이고, slot이 **없는**
채 큐가 증가하면 루프가 멈춘 것입니다.

## Kubernetes

```yaml
env:
  - name: BREVIS_METRICS_ADDR
    value: ":9090"
ports:
  - name: metrics
    containerPort: 9090
```

Prometheus Operator에서는 역할마다 `PodMonitor` 하나가 필요합니다. 두 Deployment가 서로
다른 레이블을 가지고 서로 다른 집합을 보고하기 때문에 역할마다 하나여야 합니다.

## 의도적으로 없는 것

**트레이스.** 아직 OpenTelemetry는 없습니다. step은 자기만의 pod로 실행되며, 그에 관해
중요한 정보인 시작, 종료, 시도, 종료 코드, 로그는 이미 데이터베이스와 화면에 있습니다.
유용한 트레이스는 step 안의 트레이스일 텐데, 그것은 오케스트레이터가 아니라 step의 코드에
속합니다.

이는 step이 단위인 동안에만 성립합니다. step이 다른 서비스를 호출하고 질문이 "40초가
어디로 갔는가"가 되는 날부터 트레이스는 그 비용만큼의 가치를 갖기 시작합니다.

## 다음 단계

- [Kubernetes](/ko/docs/kubernetes/index.md) — 두 프로세스 배포하기
- [구성](/ko/docs/configuration/index.md) — `BREVIS_METRICS_ADDR`와 나머지
