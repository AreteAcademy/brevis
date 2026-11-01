# Core

> Оркестратор: планирует запуски, хранит очередь в Postgres и запускает по одному pod на шаг — и никогда не касается данных.

*https://brevis.sh/ru/docs/core/ · brevis.sh docs (ru)*

---

Весь workflow — один версионируемый файл: ревью пайплайна превращается в ревью диффа. Core планирует, ставит в очередь и запускает каждый шаг с его собственным образом — и никогда не касается данных.

![Схема работы Core: планировщик и очередь, хранящиеся в Postgres, запускают по одному pod на каждый шаг workflow — prepare, extract, validate и publish. Из Core выходят только линии управления. Данные идут из HTTP-источника в pod шага extract, где работает SDK, и оттуда в BigQuery; через Core данные не проходят.](/assets/flow-core.svg)

## Что он делает

- **Планирует.** Планировщик читает расписание каждого workflow и создаёт запуски. Он и очередь — два независимых цикла: застрявший один не останавливает другой — [Планировщик и очередь](/ru/docs/scheduler-and-queue/index.md).
- **Ставит в очередь.** Очередь живёт в Postgres: тот, кто исполняет, забирает ожидающий запуск, обходит граф и записывает состояние каждого шага.
- **Запускает по одному pod на шаг**, каждый со своим образом — [Pod на шаг](/ru/docs/pod-per-step/index.md).
- **Никогда не касается данных.** Из Core выходят только линии управления: данные читает и записывает шаг, с помощью [SDK](/ru/docs/sdk/index.md) или любого вашего кода.

## Один workflow от начала до конца

Workflow — это один YAML-файл. Два шага, зависящие от одного и того же, выполняются параллельно:

```yaml
name: hello
type: dag
tags: [example]

steps:
  - id: prepare
    run: sh -c 'echo preparing; sleep 1'

  # The next two depend on the same step, so they run IN PARALLEL.
  - id: extract
    run: sh -c 'sleep 2; echo 42 extracted'
    depends_on: [prepare]

  - id: validate
    run: sh -c 'sleep 1; echo validated'
    depends_on: [prepare]

  - id: publish
    run: echo published
    depends_on: [extract, validate]
```

Чтобы запустить его локально, без очереди и кластера:

```bash
docker run --rm -v ./hello.yaml:/w/hello.yaml -w /w \
  areteacademy/brevis:0.40.0-worker run hello.yaml
```

Вывод:

```text
workflow hello (dag, 4 steps) in .
  ▶ prepare
    prepare | preparing
  ✓ prepare
  ▶ validate
  ▶ extract
    validate | validated
  ✓ validate
    extract | 42 extracted
  ✓ extract
  ▶ publish
    publish | published
  ✓ publish

workflow hello finished
```

Реальный вывод опубликованного образа. brevis run выполняется локально, без очереди; pod запускает планировщик.

## Что читать дальше

| если вы хотите | перейдите к |
|---|---|
| весь формат YAML | [Workflows](/ru/docs/workflows/index.md) |
| кто создаёт запуски и кто их исполняет | [Планировщик и очередь](/ru/docs/scheduler-and-queue/index.md) |
| почему один pod на шаг | [Pod на шаг](/ru/docs/pod-per-step/index.md) |
| что шаг передаёт следующему | [Контекст между шагами](/ru/docs/context/index.md) |
| поднять всё и увидеть граф в интерфейсе | [Быстрый старт](/ru/docs/quickstart/index.md) |
