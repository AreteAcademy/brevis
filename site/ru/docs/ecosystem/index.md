# Экосистема

> Четыре компонента brevis.sh, что делает каждый из них и через что проходят данные.

*https://brevis.sh/ru/docs/ecosystem/ · brevis.sh docs (ru)*

---

Core планирует, ставит в очередь и наблюдает, но никогда не касается данных. Данных касаются SDK, который их забирает, Gateway, который их принимает, и SQL, который моделирует то, что уже легло в хранилище.

## Четыре компонента

| компонент | что делает | как получить | руководство |
|---|---|---|---|
| **Core** | Планирует, ставит в очередь и запускает по одному pod на каждый шаг. Никогда не касается данных. | `brevissh/brevis` | [Core](/ru/docs/core/index.md) |
| **SDK** | Библиотека на Go, которая извлекает, преобразует и загружает данные внутри вашего шага. | `go get github.com/AreteAcademy/brevis/sdk` | [Go SDK](/ru/docs/sdk/index.md) |
| **Gateway** | Принимает HTTP POST, отвечает 202 и пишет пакетами. Собственной базы данных нет. | `brevissh/brevis-gateway` | [Приём данных](/ru/docs/ingestion/index.md) |
| **SQL** | Обычные .sql-файлы превращаются в таблицы и представления в порядке зависимостей. | `brevissh/brevis-sql` | [SQL](/ru/docs/sql/index.md) |

## Плоскость управления и плоскость данных

**Плоскость управления** — это Core: он решает, когда запускается каждый шаг,
хранит очередь и состояние в Postgres и запускает по одному pod на шаг.
Данные через него не проходят.

**Плоскость данных** — остальные три компонента. Данные поступают из API,
базы данных или файлов и попадают в место назначения одним из двух путей:
их **забирает SDK** внутри вашего шага, либо их **отправляют в Gateway**
по HTTP из webhook и приложений. Место назначения — таблица, топик или файлы
в бакете. Когда данные легли, **SQL** моделирует таблицы прямо там, в той
базе, куда они попали.

### Вокруг них

Три вспомогательных компонента:

- **[Console](/ru/docs/console/index.md)** — веб-интерфейс поверх Core
- **[agent](/ru/docs/pod-per-step/#where-each-step-runs)** — запускает шаг в уже имеющемся у вас кластере
- **[`brevis` для Python](/ru/docs/python/index.md)** — контекст запуска для шагов на Python

## Использование только одного компонента

Можно. У каждого компонента свой модуль, своя версия и свой образ: SDK — это библиотека на Go, которая работает в любой программе, Gateway запускается сам по YAML-файлу и без базы данных, а brevis-sql работает как контейнер. Core оркестрирует остальные, когда вы этого хотите, но ни для одного из них он не обязателен.

## Та же строка

Заказ, который забирает SDK, и заказ, который приходит в Gateway, попадают в одну и ту же таблицу с одним и тем же brevis_ingestion_id. Выбор между ними становится решением о развёртывании, а не о схеме.

Через SDK, с помощью `sdk.Landing` (подробности в разделе [Структура приземления](/ru/docs/sdk/#the-landing-layout)):

```go
sdk.Execute(ctx, &sdk.Pipeline{
	Source:    sdk.Source{From: from.Files{Path: "order.json"}},
	Transform: []sdk.Transformer{sdk.Landing("app_orders", sdk.LandingKey("id"))},
	Target: sdk.Target{
		To:     postgres.Table{DSN: dsn, Name: "app_orders"},
		Schema: sdk.LandingSchema(sdk.LandingOptions{}),
	},
}, nil)
```

Через Gateway, POST-запросом на `/v1/tables` (подробности в разделе [Один маршрут, N таблиц](/ru/docs/ingestion/#one-route-n-tables)):

```bash
curl -X POST localhost:8080/v1/tables -H 'Authorization: Bearer dev-key' \
    -d '{"table_name":"app_orders","unique_key":"id",
         "data":{"id":"A-1","customer":"acme.example.com","total":150}}'
```

Ответ:

```text
{"accepted":1,"rejected":null}  202
```

Что попало в `app_orders`:

| столбец | через SDK | через Gateway |
|---|---|---|
| `brevis_ingestion_id` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` |
| `brevis_record_key` | `A-1` | `A-1` |
| `brevis_operation` | `INSERT` | `INSERT` |
| `brevis_gateway` | `NULL` | `acme_gateway` |
| `data` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` |

Реальный вывод: оба пути выполнялись на одном и том же локальном Postgres с опубликованными образами и SDK. Разница только в том, кто записал строку: в строке от SDK brevis_gateway пуст.
