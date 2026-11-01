# step 런타임

> step이 다루는 언어와 도구에 대해 선언하는 것과, 대부분은 선언할 필요가 없는 이유입니다.

*https://brevis.sh/ko/docs/runtime/ · brevis.sh docs (ko)*

---

그래프는 각 step에 언어와 해당 step이 다루는 도구를 알려 주는 칩을 그립니다.

```
┌──────────────────────────────────────┐
│ ● transform            SDK v0.57.0   │
│   dbt build --select gold            │
│   ⬤ Python  ▢ dbt                    │
│   2m14s · attempt 1                  │
└──────────────────────────────────────┘
```

**대부분의 경우 아무것도 작성할 필요가 없습니다.** 엔진이 `run:`과 `image:`를 읽고
알아냅니다.

## 선언해야 하는 경우

선택 사항인 필드 두 개이며, 둘 다 step별입니다.

```yaml
steps:
  - id: fetch
    run: /opt/brevis/bin/fetch-weather
    runtime: go            # a compiled binary; the path says nothing

  - id: transform
    run: dbt build --select gold+
    tools: [dbt]           # what it drives, not what it is
```

| 필드 | |
|---|---|
| `runtime` | step의 언어: `go`, `python`, `node`, `shell`… |
| `tools` | 명령이 다루는 도구: `dbt`, `dlt`, `spark`… |

경험칙은 **추론이 틀릴 때 선언하라**는 것입니다. 컴파일된 바이너리를 가리키는 `run:`은
스스로 Go라고 말할 방법이 없고, 내부에서 `dbt`를 호출하는 래퍼는 스스로 dbt를 다룬다고
말할 방법이 없습니다.

## 닫힌 어휘를 쓰는 이유

어휘에 없는 id는 **publish 시점에 거부되며** 유효한 값이 안내됩니다. 대안은 사흘 뒤
화면에서 빈칸으로 렌더링되는 칩이며, 추적할 단서가 하나도 없습니다.

```
workflow "daily": step "transform": `runtime: pyton` is not valid
  (valid: go, python, node, shell, …)
```

## 칩의 용도

장식이 아닙니다. 새벽 3시에 run이 실패하면 가장 먼저 나오는 질문은 "이 step이 무엇을
실행하는가?"인데, 답은 대개 다른 저장소에 있습니다. 칩은 그 답을 그래프 자체에
올려 줍니다. step이 사용한 SDK 버전과 함께 보여 주므로, "오늘 깨졌다"와 "화요일 버전
올림 이후로 깨졌다"를 구분할 수 있습니다.

## 다음 단계

- [Workflows](/ko/docs/workflows/index.md) — 모든 step 필드
- [step당 pod 하나](/ko/docs/pod-per-step/index.md) — step이 실제로 실행되는 곳
- [관측성](/ko/docs/observability/index.md) — 함께 쓰는 메트릭
