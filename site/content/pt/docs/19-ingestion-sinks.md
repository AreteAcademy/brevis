---
title: Destinos da ingestão
description: Os seis destinos, como cada banco escreve um registro, e por que a imagem carrega só o que você usa.
group: Ingestão
order: 19
slug: ingestion-sinks
---

Seis destinos, e a lista é explícita de propósito — um conector *planejado* e um
conector que *roda* são coisas diferentes para quem está escolhendo.

| destino | estado | o que é uma escrita | provado contra |
|---|---|---|---|
| `pubsub` | **roda** | uma publicação por lote, atributos e chave de ordenação vindos dos campos do evento | o emulador |
| `postgres` | **roda** | `COPY FROM STDIN`, ou um `INSERT` em transação a partir de uma tabela temporária | um Postgres real |
| `mysql` | **roda** | `INSERT` multi-linha em transação, ou `INSERT IGNORE` | um MySQL real |
| `files` | **roda** | objetos NDJSON num diretório, `gs://` ou `s3://` | o disco, e o S3 |
| `bigquery` | **roda** | linhas carregadas, ou preparadas e feito `MERGE` no `ingestion_id` | **só a configuração — não existe emulador** |
| `redshift` | **roda** | o lote para o S3, depois `COPY`, depois `MERGE` | **só a configuração — não existe emulador** |

**Dois dos seis não estão provados ponta a ponta**, e a tabela diz isso em vez de
deixar a palavra "roda" carregar uma afirmação que ninguém verificou. BigQuery e
Redshift não têm emulador local, então o que está testado é a configuração, as
recusas e o driver por baixo — que já é coberto pela suíte do SDK. O primeiro
uso em produção de qualquer um dos dois é a prova de verdade, e deveria ser um
stream de baixo risco.

Um `type:` desconhecido é **recusado na inicialização**, nomeando o que *este
binário* carrega. Um gateway que sobe com um destino que ele não tem é um
gateway que descarta eventos por um motivo que ninguém enxerga.

`gs://` e `s3://` não são conectores separados: `files` lê os três formatos de
caminho, então a fila de descarte é uma pasta num laptop e um bucket em
produção sem mudar nada aqui.

## Como cada um escreve

`write` é **obrigatório e nunca tem padrão** nos quatro destinos que escrevem em
tabela, porque os dois modos são genuinamente diferentes e só o dono da tabela
sabe qual é:

| | `append` | `merge` |
|---|---|---|
| **postgres** | `COPY FROM STDIN` | `INSERT … ON CONFLICT (ingestion_id) DO NOTHING` |
| **mysql** | `INSERT` multi-linha por bloco | `INSERT IGNORE` |
| **bigquery** | linhas carregadas (via GCS acima do limite inline) | `MERGE … WHEN NOT MATCHED THEN INSERT` |
| **redshift** | `COPY` do S3 | `COPY` para uma tabela de preparo, depois `MERGE` |

`merge` significa **a mesma coisa nos quatro**: a inserção idempotente, e a
**primeira entrega vence**. Uma reentrega é ignorada, não aplicada — que é o
certo para um evento, que aconteceu uma vez e não muda, e o errado para uma
linha que carrega um estado mutável.

Postgres e MySQL **exigem um índice `UNIQUE` em `ingestion_id`** para o `merge`,
e recusam sem ele em vez de gravar em append silenciosamente:

```sql
CREATE UNIQUE INDEX CONCURRENTLY ON landing.orders (ingestion_id);
```

Sem o índice, o `ON CONFLICT` não tem o que casar, e toda reentrega duplicaria
numa tabela cujo dono pediu o oposto. A recusa não é um lote perdido: ele é
tentado de novo, e então enterrado na fila de descarte com o motivo e o
`CREATE INDEX` no registro — ninguém perde eventos enquanto alguém cria o
índice.

**`upsert` é recusado pelo nome.** `ON CONFLICT DO UPDATE`, onde a *última*
entrega vence, é um modo real que **não está implementado**. Aceitar a palavra
e se comportar como `merge` é exatamente a falha que esse campo existe para
impedir, então a configuração diz isso em vez de fazer aquilo.

## Cada um com o que só é verdade dele

```yaml
# Postgres: nome qualificado pelo schema, e a tabela precisa existir.
sink: {type: postgres, dsn_from: BREVIS_DSN, table: landing.clicks, write: merge}

# MySQL: não tem COPY, então a vazão fica uma ordem abaixo do Postgres no
# mesmo hardware. Agrupe lotes maiores aqui.
sink: {type: mysql, dsn_from: BREVIS_DSN, table: landing.clicks, write: merge}

# BigQuery: sem dsn_from. Ele autentica com a credencial do próprio pod, que é
# para isso que existe workload identity. O nome não tem pontos — o projeto e o
# dataset são campos próprios.
sink: {type: bigquery, project: acme-prod, dataset: landing, table: clicks, write: merge}

# Redshift: dois saltos, não um. Ele é colunar, um INSERT linha a linha paga o
# custo de um bloco, e a única carga viável é COPY do S3. Então todo lote vira
# um objeto no prefixo de preparo e depois um COPY — um stream com flush de um
# segundo escreve 86.400 objetos por dia. Agrupe muito mais aqui.
sink:
  type: redshift
  dsn_from: BREVIS_RS_DSN
  table: landing.clicks
  staging: s3://acme-staging/gateway/
  iam_role: arn:aws:iam::123456789012:role/redshift-copy
  write: merge
```

`dsn_from` nomeia a **variável de ambiente** que guarda a string de conexão,
nunca a string: um DSN carrega uma senha e esse arquivo está no git. É a mesma
separação que `secrets:` faz num workflow.

`iam_role` é uma role e o driver **não aceita uma chave**: uma chave na URL de
um `COPY` termina no log de queries do cluster, que muita gente lê.

## Nenhuma declaração de colunas

O gateway não envia lista de colunas. Um pipeline declara as dele porque as
linhas têm uma forma que ele controla; o lote de um gateway é o que *N* clientes
postaram numa janela de flush, e as formas podem diferir.

O driver resolve a lista a partir da **própria tabela**, cruzada com o que o
lote carrega, **antes** de tocar no servidor. Um campo que a tabela não tem é
recusado com a mensagem que resolve, em vez de falhar no meio de um `COPY` com
`column "x" of relation "y" does not exist`; um campo que um evento omite é
gravado como `NULL`.

## A lista de imports é a seleção

Os destinos são **compilados junto**, como os hooks, e o binário registra o que
ele tem:

```go
func main() {
    sinks := gateway.NewSinks()
    sinks.MustRegister(postgres.Sink, postgres.New)
    sinks.MustRegister(files.Sink, files.New)
    gateway.Main(nil, gateway.WithSinks(sinks))
}
```

O linker do Go descarta o que nada referencia, então um binário que nunca
importa o driver de BigQuery não carrega BigQuery — nem o Arrow, nem a Storage
Write API. É como o `database/sql` sempre funcionou.

Vale o que parece:

| build | pacotes | binário |
|---|---|---|
| os seis destinos, os dois object stores | 864 | 48,9 MB |
| `postgres` + `files` local | 232 | **10,0 MB** |

Os object stores são um **segundo** registro, porque um scheme não é um destino:
`files` é um destino só que escreve em diretório, em `gs://` e em `s3://`, e um
build que só escreve local não deveria carregar o SDK da AWS para isso.

### Duas imagens publicadas

| imagem | carrega | pull |
|---|---|---|
| `areteacademy/brevis-gateway:0.3.2` | os seis destinos, S3 e GCS | 16,1 MB |
| `areteacademy/brevis-gateway:0.3.2-slim` | `postgres`, `files` local | **4,8 MB** |

As duas saem de um build da mesma árvore, então as duas tags são sempre o mesmo
commit. Não existe `latest-slim` — `latest` já é uma tag que ninguém deveria
implantar.

**Quer outro par?** `cmd/gateway-slim` tem quinze linhas e o bloco de imports é
toda a configuração dele. Copie, troque os imports, compile. Quem quer um hook
já está compilando o próprio binário, então escolher os destinos não custa nada
a mais.

### A recusa diz qual build você está segurando

```
sink type "bigquery" is not one this binary carries (it has: files, postgres).
Sinks are compiled in, so this is a build that left it out rather than a
destination that does not exist.
```

Uma lista fixa mandaria essa pessoa procurar um erro de configuração que ela não
cometeu. Vale igual para um object store: uma fila de descarte em `s3://` num
binário sem o backend de S3 é recusada **na inicialização**, nomeando o scheme,
em vez de no primeiro lote que ela precisaria enterrar.

## Uma rota, N tabelas, nada declarado

```yaml
sink:
  type: auto_table
  table_from: table_name
  naming: {pattern: '^[a-z][a-z0-9_]{2,48}$', allow: [app_, svc_], max_new_per_hour: 20}
  metastore: {type: memory, ttl: 60s}
  into: {type: bigquery, project: acme-prod, dataset: landing, write: merge}
```

Um produtor faz `POST {"table_name": "app_orders", ...}` e a tabela nasce se não
existir. O `auto_table` **roteia e não escreve**: `into` é quem escreve, e não
carrega `table` — a tabela vem de cada evento.

### Quatro colunas, sempre

| coluna | | |
|---|---|---|
| `ingestion_id` | `STRING` | a identidade, e a chave do merge |
| `ingested_at` | `TIMESTAMP` | a chegada, no **nosso** relógio — a coluna de partição |
| `occurred_at` | `TIMESTAMP` | o do produtor, quando ele manda; `NULL` quando não |
| `data` | `JSON` | o evento inteiro |

**Uma coluna JSON, não uma coluna por campo**, e é nessa decisão que o resto se
apoia. Um campo novo é uma chave nova: não há DDL, não há cota de alteração de
schema, não há reabertura de stream de escrita, não há corrida entre réplicas —
e ele é consultável no dia em que chega. Uma coluna por campo compra tipos que
ninguém declarou e paga com as quatro coisas.

É também onde o mercado chegou: Fivetran, Airbyte e Snowpipe pousam numa camada
raw que não pode falhar e modelam depois. E é o que mantém o `table_from`
barato — toda tabela tem a mesma forma, então criar uma é um template e não uma
decisão.

Uma declaração serve a todos os destinos, porque o gerador de DDL do SDK
transforma `TypeJSON` em `JSON` no BigQuery, `JSONB` no Postgres, `JSON` no
MySQL e `SUPER` no Redshift.

**`ingested_at` e não `occurred_at` como coluna de partição**: o relógio de um
produtor pode estar errado ou ausente, e uma coluna de partição que o cliente
controla é um cliente que pode escrever em 2035. Uma tabela de pouso sem
partição é uma conta de consulta que só cresce.

### A identidade, sem nada declarado

```
ingestion_id = uuid5(auto_table | <tabela> | <idempotency_key>   | <occurred_at>)
             = uuid5(auto_table | <tabela> | sha256(canônico)    | <occurred_at>)
```

Um stream declarado nomeia quatro campos que o dono dele conhece. Um produtor
que envia `{table_name, data}` não nomeia nenhum, então a chave é a dele quando
ele manda uma, e uma impressão digital do documento quando não manda.

**Mande `idempotency_key`.** Ela significa *estas duas requisições são o mesmo
fato de negócio*, que só o produtor sabe. A impressão digital significa *estas
duas requisições têm bytes idênticos* — uma boa aproximação e uma promessa pior,
porque dois eventos genuinamente distintos com dados byte a byte iguais viram
um. Os dois ganham de um id aleatório, sob o qual um POST repetido é uma
segunda linha para sempre.

A impressão digital é sobre o documento em forma **canônica** — chaves ordenadas
em todo nível, arrays na ordem, tudo entre aspas. O Go randomiza a iteração de
map, então um hash ingênuo diferiria entre duas entregas do mesmo evento, que é
exatamente o que ele existe para impedir.

### O nome é a superfície de ataque

O `auto_table` transforma uma string de um payload em DDL, então todo campo de
`naming` é uma recusa:

- **`pattern`** tem como padrão `^[a-z][a-z0-9_]{2,48}$` — a interseção do que
  Postgres, MySQL, BigQuery e Redshift aceitam sem aspas. Um nome que passa não
  precisa de aspas em lugar nenhum e não consegue carregar uma injeção.
- **`allow`** estreita mais, por prefixo.
- **`max_new_per_hour`** (padrão 20) limita **criações** numa hora móvel, nunca
  escritas: uma tabela que já existe nunca é limitada. **Por réplica** — o
  orçamento vive no processo, então quatro réplicas admitem quatro vezes isso.

Um nome fora das regras é recusado **por evento**, na resposta, e todo evento
bem formado da mesma requisição pousa assim mesmo:

```json
{"accepted": 1, "rejected": ["event 1: the table name \"x\";DROP TABLE y;--\" does not match ^[a-z][a-z0-9_]{2,48}$"]}
```

Por evento e não por lote, de propósito. Recusar na hora da escrita derrubaria
o lote inteiro — um evento malformado de um produtor enterrando os eventos de
todos os outros da mesma janela, e nenhum deles avisado.

**O `auto_table` é recusado num endpoint sem `listen.auth`.** Um produtor que
pode nomear uma tabela pode criar uma, então ele exige autenticação mesmo onde
um stream comum não exigiria. Uma NetworkPolicy não cobre isso: ela limita quem
alcança a porta, não que nome de tabela a pessoa pede.

### O metastore é um cache

Ele existe para que uma escrita por evento não vire uma consulta por evento, e
guarda **as duas** respostas — "esta tabela não existe" é o que economiza uma
ida e volta no caminho quente de um produtor novo tentando de novo.

É um cache e **não** uma fonte de verdade. O destino decide se a tabela existe,
e *N* réplicas correndo para criar uma é o caso normal: `AlreadyExists` é
sucesso, e isto só reduz a corrida. O TTL é quanto tempo ele pode estar
**errado**.

**`memory` é o único backend, e isso é o desenho.** Um gateway que não sobe sem
Redis é um gateway com uma dependência dura nova por causa de um cache. `redis`
e `memcached` são recusados **pelo nome**, porque quem escreve `redis` acredita
que suas réplicas dividem um cache — e aceitar a palavra enquanto se guarda por
processo faria o `max_new_per_hour` valer *N* vezes o que a pessoa escreveu.

### O BigQuery tem um piso de flush

**O BigQuery permite 1.500 load jobs por tabela por dia.** A janela padrão de um
segundo dá 86.400 — 57× a cota, esgotada em cerca de vinte e cinco minutos. Não
é problema para o Pub/Sub, e o número chegaria aqui *vindo* de uma config de
Pub/Sub.

Então um stream que escreve no BigQuery, direto ou por um roteador, é **recusado
no carregamento** com janela abaixo de 60s. A resposta de verdade é a Storage
Write API, que ainda não foi escrita; até lá, a config recusa uma janela que não
consegue honrar em vez de deixar o primeiro deploy descobrir.

O `auto_table` não roteia para **Redshift**: aquele driver não cria tabelas,
então todo nome novo falharia na carga. Recusado pelo nome.

## O que ainda não existe

`sqlite`, `redis`, `dynamodb`, `kinesis`, `sqs`, `sns`, `kafka` e `rabbitmq`
estão planejados e nada foi escrito. A tabela no topo desta página diz o estado
de cada um, e ela é atualizada quando o estado muda — não antes.
