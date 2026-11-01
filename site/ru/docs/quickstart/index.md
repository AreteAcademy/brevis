# Быстрый старт

> От нуля до работающего DAG в интерфейсе за четыре шага.

*https://brevis.sh/ru/docs/quickstart/ · brevis.sh docs (ru)*

---

Это руководство поднимает brevis.sh, публикует workflow и запускает его. В
конце вы видите граф с текущим состоянием каждого шага.

Требование: доступный PostgreSQL и переменная `BREVIS_DATABASE_URL`.

## 1. Примените схему

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
brevis migrate up
```

## 2. Напишите workflow

Создайте `hello.yaml`. В этом примере используется только shell, поэтому он
работает где угодно:

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

Проверьте его перед публикацией — база данных при этом не затрагивается:

```bash
brevis validate hello.yaml
```

```text
  ok    hello                        dag  4 steps, 4 dependencies  (manual)
```

## 3. Запустите сразу, без очереди

Кратчайший путь, чтобы увидеть работу графа:

```bash
brevis run hello.yaml
```

```text
workflow hello (dag, 4 steps) in .
  ▶ prepare
    prepare | preparing
  ✓ prepare
  ▶ extract
  ▶ validate
    validate | validated
  ✓ validate
    extract | 42 extracted
  ✓ extract
  ▶ publish
    publish | published
  ✓ publish

workflow hello finished
```

`extract` и `validate` стартуют вместе, потому что зависят от одного и того же
шага. Раннер обходит граф **по уровням**: всё внутри уровня выполняется
параллельно, а следующий уровень начинается только после полного завершения
предыдущего.

:::note `run` по природе локален
`brevis run` работает только при `BREVIS_ENV=local` (пустое значение считается
локальным) и исполняет шаги как процессы на самом экземпляре — даже те, что
объявляют `image:`. Pod создаёт `scheduler`.
:::

## 4. Опубликуйте и управляйте

Чтобы workflow существовал в базе данных, появился в интерфейсе и выполнялся
по расписанию:

```bash
brevis publish hello.yaml
```

```text
  published  hello                      (manual)
```

Запустите интерфейс и планировщик в двух терминалах:

```bash
brevis serve
```

```bash
brevis scheduler --interval 5s --concurrency 4
```

Откройте `http://localhost:8080`, найдите `hello` в списке workflow и нажмите
▶. На графе видно, как каждый шаг меняет состояние в реальном времени.

## Что произошло

| | |
|---|---|
| `migrate up` | создала таблицы |
| `validate` | проверила YAML, не обращаясь к базе данных |
| `run` | выполнил на самом экземпляре, без очереди |
| `publish` | сохранил workflow и его расписание |
| `serve` | поднял API и интерфейс |
| `scheduler` | материализовал расписание и разобрал очередь |

## Дальнейшие шаги

- [Workflows](/ru/docs/workflows/index.md) — полный формат YAML
- [Параметры](/ru/docs/parameters/index.md) — что меняется между двумя запусками
- [Pod на шаг](/ru/docs/pod-per-step/index.md) — как это превращается в Kubernetes
