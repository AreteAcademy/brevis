---
title: SQL
description: Arquivos .sql comuns viram tabelas e views, na ordem de dependência — como uma etapa do workflow ou sozinho.
group: Modelagem em SQL
order: 21
slug: sql
---

Sem template: um modelo é um arquivo que continua SQL válido, e a ordem vem da leitura do próprio SQL. Postgres e BigQuery, com a mesma suíte de conformidade.

![O fluxo do SQL: o brevis-sql lê raw.orders, uma tabela que o SDK ou o Gateway pousou. O modelo staging.stg_orders vira uma view sobre ela, e marts.orders uma tabela sobre essa view; a ordem vem da leitura do SQL. Os mesmos modelos rodam no Postgres ou no BigQuery.](/assets/flow-sql.svg)

## Quatro comandos

| comando | o que faz |
|---|---|
| `compile` | lê todos os modelos e resolve todas as arestas, **sem se conectar a nada** |
| `graph` | imprime as arestas inferidas, para que uma errada seja vista e não descoberta |
| `build` | cria ou substitui cada modelo, na ordem de dependência |
| `test` | roda os testes de cada modelo; cada um é um SELECT que não pode encontrar nada |

`--select orders+` restringe qualquer um deles a um modelo e a tudo que vem depois dele.

## Um modelo é um arquivo que continua SQL válido

A configuração é um comentário de bloco no topo. O arquivo abre no editor com
realce, roda à mão num console, e `psql -f` o executa.

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

**Sem template.** `models/<schema>/<nome>.sql` é `<schema>.<nome>`, e as
dependências vêm da leitura do próprio SQL, não de uma função que você precisa
escrever. `brevis-sql graph` imprime o que foi inferido; `depends_on:` no
cabeçalho é a correção explícita para o caso raro em que ele erra.

## Do compile ao test

Dois modelos: uma view que limpa `raw.orders`, e uma tabela que depende dela.

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

`compile` e `graph` não se conectam a nada:

```bash
docker run --rm -v ./project:/project:ro areteacademy/brevis-sql:0.1.0 \
  compile --project /project
```

```text
2 models, 2 to build, 0 function(s)
  staging.stg_orders                 view  0 test(s)
  marts.orders                       table 2 test(s)
```

```bash
docker run --rm -v ./project:/project:ro areteacademy/brevis-sql:0.1.0 \
  graph --project /project
```

```text
staging.stg_orders
  └─ raw.orders (source)
marts.orders
  ├─ staging.stg_orders
```

`build` e `test`, contra um Postgres:

```bash
docker run --rm -v ./project:/project:ro \
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db areteacademy/brevis-sql:0.1.0 \
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
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db areteacademy/brevis-sql:0.1.0 \
  test --project /project --dsn-from BREVIS_SQL_DSN
```

```text
2 tests passed on postgres
```

A saída real, com a imagem publicada — compile e graph não se conectam a nada. Contra um Postgres, build e test fecham com "2 models built" e "2 tests passed".

### Quando um teste falha

Com um pedido sem cliente em `raw.orders`, o `build` passa e o teste `not_null` não. A falha diz o modelo, a coluna, quantas linhas e **a consulta** — que é o que você cola num console para continuar estreitando:

```text
FAIL  marts.orders                   not_null         customer_id          1 row(s)
      SELECT COUNT(*) FROM (SELECT customer_id FROM marts.orders WHERE customer_id IS NULL) AS v
brevis-sql: 1 of 2 tests failed
```

O processo sai com código 1, e a etapa do workflow falha com ele.

## Testes são SELECTs que devolvem as linhas que violam

`not_null`, `unique`, `accepted_values`, `relationships`. Zero linhas é sucesso, e não há mais nada a interpretar.

## Ele diz o que escreveu

Cada modelo materializado imprime a linha `@brevis:` que o engine do Brevis já
lê, então ele aparece em `/data` ao lado de todo o resto (veja a saída do
`build` acima). Uma view não informa contagem de linhas em vez de informar
zero: ela não guarda nenhuma, e um zero ali seria indistinguível de uma
tabela que está de fato vazia.

## Dois bancos, uma suíte

PostgreSQL e BigQuery (`--dialect bigquery`), e os dois rodam a mesma suíte de
testes — escrita quando havia uma só implementação, para que a segunda não
pudesse divergir em silêncio.

O BigQuery autentica com Application Default Credentials: num workload
identity, é a identidade do próprio pod, e `--dsn-from` nomeia a variável que
guarda o id do projeto.

## 13 MB

Distroless, sem root, sem shell. O cliente oficial do BigQuery tem 31 MB e 526
pacotes num binário vazio; por isso este fala com o BigQuery pelo endpoint
REST — cerca de 200 linhas, com um teste para cada jeito de errar em silêncio.
Um gate no repositório recusa aquele cliente pelo nome, para que a decisão
sobreviva a uma atualização de dependências de rotina.

## Verificado contra o dbt

O jaffle-shop, construído por esta imagem sobre os mesmos dados que o dbt usou:

|  | resultado |
|---|---|
| linhas e md5 de cada linha, por modelo | **idênticos, 13 de 13** |
| colunas e tipos, por modelo | **idênticos, 13 de 13** |
| 13 modelos, primeiro build | 1,56 s |

## O que ele não faz

**Nenhuma compatibilidade com o dbt**, e isso é uma decisão, não uma lacuna.
Cerca de um terço dos modelos de um projeto dbt real não usa nada além de
`ref`, `source` e `config`; o resto usa macros, e pacotes são template por
definição. Meia compatibilidade que ninguém consegue prever é pior que
nenhuma.

## Por onde seguir

| se você quer | vá para |
|---|---|
| ver onde o SQL entra entre as quatro peças | [Ecossistema](/docs/ecosystem/) |
| rodar o brevis-sql como uma etapa agendada | [Core](/docs/core/) |
| pousar o dado que ele vai modelar | [SDK em Go](/docs/sdk/) ou [Ingestão](/docs/ingestion/) |
