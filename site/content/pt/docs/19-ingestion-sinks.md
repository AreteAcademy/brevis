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
| os seis destinos, os dois object stores, os dois metastores | 884 | 55,0 MB |
| `postgres` + `files` local | 233 | **10,2 MB** |

Os object stores são um **segundo** registro, porque um scheme não é um destino:
`files` é um destino só que escreve em diretório, em `gs://` e em `s3://`, e um
build que só escreve local não deveria carregar o SDK da AWS para isso.

### Duas imagens publicadas

| imagem | carrega | pull (amd64) |
|---|---|---|
| `areteacademy/brevis-gateway:0.8.0` | os seis destinos, S3, GCS, Redis, memcached | 17,9 MB |
| `areteacademy/brevis-gateway:0.8.0-slim` | `postgres`, `files` local, `auto_table` | **4,9 MB** |

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
  shape: columns
  naming: {pattern: '^(app|svc)_[a-z0-9_]{2,40}$', allow: [app_, svc_], max_new_per_hour: 20}
  metastore: {type: redis, addr_from: BREVIS_METASTORE_ADDR, ttl: 60s}
  into: {type: bigquery, project: acme-prod, dataset: landing, write: merge}
```

O `auto_table` **roteia e não escreve**: `into` é quem escreve, e não carrega
`table` — a tabela vem de cada evento. Um `into` com `table` é recusado na
inicialização, porque uma tabela fixa sob um roteador venceria em silêncio e
todo evento pousaria nela.

### O envelope

O corpo é um **envelope**: campos de controle em cima, o registro dentro de
`data`.

```json
{
  "table_name": "app_orders",
  "description": "Pedidos do app",
  "operation": "INSERT",
  "unique_key": "id",
  "data": {
    "id": "A-1",
    "total": 150,
    "customer": {"id": 7, "uf": "SP"},
    "items": [{"sku": "X", "qtd": 2}]
  }
}
```

| campo | | o que é |
|---|---|---|
| `table_name` | **obrigatório** | onde isso pousa |
| `data` | **obrigatório** | o registro, e só ele |
| `unique_key` | padrão `id` | qual campo de `data` identifica o registro |
| `operation` | padrão `INSERT` | `INSERT`, `UPDATE` ou `DELETE` |
| `description` | opcional | **aceito e ainda não usado** — veja abaixo |

`table_name` ao lado de `total` e `customer` era um campo do **transporte** se
passando por campo do **registro**. Separar os dois é a forma de todo formato de
CDC — e é o que torna possível reservar um prefixo, porque agora existe um lugar
onde só o produtor escreve.

**`description` é lido e hoje não vai a lugar nenhum.** Ele existe no contrato
para a página de ingestão do console, que ainda não foi escrita. Está dito aqui
porque um campo aceito em silêncio é um campo que alguém acredita estar
gravando.

### Sete colunas fixas, mais o que o registro traz

| coluna | | |
|---|---|---|
| `brevis_ingestion_id` | `STRING` | a identidade, e a chave do merge |
| `brevis_record_key` | `STRING` | `data[unique_key]` — de qual **registro** isto fala |
| `brevis_operation` | `STRING` | `INSERT`, `UPDATE` ou `DELETE` |
| `brevis_received_at` | `TIMESTAMP` | a chegada, no **nosso** relógio — a coluna de partição |
| `brevis_loaded_at` | `TIMESTAMP` | a escrita, carimbada pelo **destino** |
| `brevis_stream` | `STRING` | qual rota escreveu |
| `brevis_gateway` | `STRING` | qual implantação |

**`brevis_` é reservado.** Um `data` carregando qualquer chave com esse prefixo é
recusado **por evento** — senão um produtor forja um campo de controle, e um
`brevis_received_at` forjado é pior que nenhum, porque parece real.

**`brevis_loaded_at` é um `DEFAULT` do banco**, não um valor que o gateway manda.
O gateway sabe a hora do **despacho**, o destino sabe a da **escrita**, e a
diferença entre as duas colunas é a latência ponta a ponta de verdade, por
linha, sem instrumentar nada.

**`brevis_received_at` é a coluna de partição, nunca o relógio do cliente**: um
relógio de produtor pode estar errado ou ausente, e uma coluna de partição que o
cliente controla é um cliente que pode escrever em 2035. Uma tabela de pouso sem
partição é uma conta de consulta que só cresce. O agrupamento é por
`brevis_record_key`, porque ler o histórico de um registro é o que se faz com
uma tabela dessas.

Uma declaração serve a todos os destinos, porque o gerador de DDL do SDK traduz
cada tipo: `STRING` vira `TEXT` no Postgres, `LONGTEXT` no MySQL e `STRING` no
BigQuery; `JSON` vira `JSONB`, `JSON` e `JSON`.

### Duas formas, um contrato

`shape` decide o que o **registro** contribui para a linha. O contrato do
produtor é o mesmo dos dois jeitos — o envelope nunca muda. O que muda é a cara
da tabela, que é decisão de quem opera e não de quem produz.

```yaml
shape: document   # padrão
shape: columns
```

Com **`document`**, `data` inteiro vai para uma coluna `JSON`:

```
 brevis_ingestion_id | brevis_record_key | … | data
 d7179bfe-…          | A-1               |   | {"id": "A-1", "total": 150, "customer": {…}}
```

Nunca há DDL depois do create. Um campo novo é uma chave nova: sem cota de
alteração de schema, sem reabertura de stream de escrita, sem corrida entre
réplicas — e consultável no dia em que chega. É onde o mercado pousou: Fivetran,
Airbyte e Snowpipe entregam numa camada raw que não pode falhar e modelam
depois.

Com **`columns`**, cada campo do registro ganha uma coluna:

```
 id   | total | customer              | items
 A-1  | 150   | {"id": 7, "uf": "SP"} | [{"qtd": 2, "sku": "X"}]
```

**Escalar vira `STRING`, objeto e array viram `JSON`, e não há inferência em
lugar nenhum.** `150` chegou como número e virou a string `150`. Isso é uma
escolha e não uma limitação: um campo que chega inteiro hoje e fracionado amanhã
mudaria o tipo de uma coluna sem ninguém escrever nada, e a linha que não couber
vai para a fila de descarte. Tipar é a **promoção** — escrita no YAML e revisada
num diff.

Um nome de campo precisa casar com a regra do BigQuery, a mais estreita das
quatro. O Postgres aceitaria quase tudo entre aspas, e essa é a armadilha: a
tabela nasce lá e quebra no dia em que alguém aponta um stream para o BigQuery.

### `UPDATE` e `DELETE` são registrados, não aplicados

`brevis_operation` é uma coluna. Uma tabela de pouso é **histórico**: um `UPDATE`
aplicado no lugar perde a versão anterior, e o dia em que você quer essa versão é
o dia em que algo quebrou. Resolver "a versão atual de cada registro" é trabalho
do modelo lá embaixo:

```sql
qualify row_number() over (partition by brevis_record_key
                           order by brevis_received_at desc) = 1
   and brevis_operation <> 'DELETE'
```

É o que Debezium, Fivetran e Airbyte fazem — e significa que CDC custa **nada** no
caminho de escrita: nenhum modo upsert, nenhum lock, nenhum delete.

### A identidade não tem relógio

```
brevis_ingestion_id = uuid5(auto_table | tabela | data[unique_key] | sha256(canônico(data)))
```

Não há `occurred_at` na fórmula, e isso é deliberado: a chegada virou
responsabilidade **nossa**, e o nosso relógio é `time.Now()` — diferente em cada
entrega. Uma identidade com ele dentro faria de cada reentrega um evento novo, e
o `merge` pararia de absorver qualquer coisa.

```
mesmo registro, mesmo conteúdo, duas vezes  →  mesmo id, o merge absorve
mesmo registro, conteúdo mudou              →  id diferente, as duas versões pousam
```

A ressalva é a documentada: dois eventos genuinamente distintos com `data`
idêntico byte a byte viram um. Para CDC isso está **certo** — dois updates
iguais no mesmo registro com os mesmos valores são o mesmo fato.

A impressão digital é sobre o documento em forma **canônica** — chaves ordenadas
em todo nível, arrays na ordem, tudo entre aspas. O Go randomiza a iteração de
map, então um hash ingênuo diferiria entre duas entregas do mesmo evento, que é
exatamente o que ele existe para impedir.

### A tabela cresce uma coluna sozinha

Um `data` com um campo que a tabela não tem faz a coluna nascer. **Aditivo, e só
aditivo**: nada é removido, nada é estreitado.

Um lote que perde a corrida pelo `ALTER` falha, e o retry comum da esteira
resolve — um lote esperando DDL e um lote esperando worker são a mesma coisa,
então não existe um segundo buffer. Entre réplicas, o metastore segura um
*debounce* de um segundo por tabela e por forma, para que dez réplicas vendo o
mesmo campo novo não virem dez `ALTER` contra a cota do BigQuery de cinco
operações de metadados por tabela a cada dez segundos.

**Debounce e não lock**: nada é liberado e nenhum lease é renovado. Quem perde
está sendo *retentado*, não bloqueado, e uma réplica que morre segurando custa um
segundo às outras. É o que o mercado faz — Delta Lake e Iceberg commitam otimista
e retentam, o Kafka Connect ganha serialização da ordem da partição, o Fivetran
tem um escritor por tabela.

### O nome é a superfície de ataque

O `auto_table` transforma uma string de um payload em DDL, então todo campo de
`naming` é uma recusa:

- **`pattern`** tem como padrão `^[a-z][a-z0-9_]{2,48}$` — a interseção do que
  Postgres, MySQL, BigQuery e Redshift aceitam sem aspas. Um nome que passa não
  precisa de aspas em lugar nenhum e não consegue carregar uma injeção.
- **`allow`** estreita mais, por prefixo.
- **`max_new_per_hour`** (padrão 20) limita **criações** numa hora móvel, nunca
  escritas: uma tabela que já existe nunca é limitada. Com `metastore: memory` o
  orçamento vive no processo e quatro réplicas admitem quatro vezes isso; com
  `redis` ou `memcached` elas dividem um contador só.

Um nome fora das regras é recusado **por evento**, na resposta, e todo evento
bem formado da mesma requisição pousa assim mesmo:

```json
{"accepted": 0, "rejected": ["event 0: the table name \"pedidos\" does not match ^(app|svc)_[a-z0-9_]{2,40}$"]}
```

Por evento e não por lote, de propósito. Recusar na hora da escrita derrubaria o
lote inteiro — um evento malformado de um produtor enterrando os eventos de todos
os outros da mesma janela, e nenhum deles avisado.

**O `auto_table` é recusado num endpoint sem `listen.auth`**, inclusive em
`BREVIS_ENV=local`. Um produtor que pode nomear uma tabela pode criar uma, então
ele exige autenticação mesmo onde um stream comum não exigiria. Uma
NetworkPolicy não cobre isso: ela limita quem alcança a porta, não que nome de
tabela a pessoa pede.

### O metastore é um cache

Ele existe para que uma escrita por evento não vire uma consulta por evento, e
guarda três coisas: quais tabelas existem, o *debounce* do DDL e o contador do
`max_new_per_hour`.

É um cache e **não** uma fonte de verdade. O destino decide se a tabela existe, e
*N* réplicas correndo para criar uma é o caso normal: `AlreadyExists` é sucesso, e
isto só reduz a corrida. O TTL é quanto tempo ele pode estar **errado**.

| backend | quando |
|---|---|
| `memory` | o padrão, não precisa de nada. Com **uma** réplica é a resposta certa |
| `redis` | `addr_from` obrigatório. Um `SETNX` é o claim, um `INCR` é o contador |
| `memcached` | `addr_from` obrigatório. Um `Add` é o claim, e a expiração tem granularidade de **segundo** |

Com várias réplicas, `memory` dá a cada uma a sua: o debounce não debounce nada
e o `max_new_per_hour` limita um *processo*. É a única razão de os outros dois
existirem.

**Um metastore fora do ar não pode derrubar uma escrita.** Um `Get` que erra é
miss, um claim que erra se comporta como ganho, um contador que erra relaxa o
limite. Um cache capaz de parar a ingestão é pior que nenhum cache.

`addr_from` nomeia a **variável de ambiente** que guarda o endereço, nunca o
endereço: ele carrega senha com frequência suficiente.

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
então todo nome novo falharia na carga. Recusado pelo nome, na inicialização.

## O que ainda não existe

`sqlite`, `dynamodb`, `kinesis`, `sqs`, `sns`, `kafka` e `rabbitmq` estão
planejados e nada foi escrito. (`redis` e `memcached` existem como **metastore**,
que é outra coisa: um cache do que se sabe sobre as tabelas, nunca um destino.) A tabela no topo desta página diz o estado
de cada um, e ela é atualizada quando o estado muda — não antes.
