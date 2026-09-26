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
| `unique_key` | padrão `id` | qual campo de `data` identifica o registro — **obrigatório sob `merge`** |
| `operation` | padrão `INSERT` | `INSERT`, `UPDATE` ou `DELETE` |
| `description` | opcional | **aceito e ainda não usado** — veja abaixo |

`table_name` ao lado de `total` e `customer` era um campo do **transporte** se
passando por campo do **registro**. Separar os dois é a forma de todo formato de
CDC — e é o que torna possível reservar um prefixo, porque agora existe um lugar
onde só o produtor escreve.

### A chave é do `merge`, não do envelope

`unique_key` é opcional, e se ele é **exigido** depende do modo de escrita —
porque os dois modos querem coisas diferentes:

| | |
|---|---|
| `write: merge` | **obrigatório.** O modo existe para guardar uma linha por registro, e `brevis_record_key` é o que "por registro" significa: o `qualify` que resolve a versão atual particiona por ele. Uma tabela de merge cheia de linhas que não nomeiam registro é uma tabela que ninguém resolve. |
| `write: append` | **opcional.** Uma tabela de append é um log, e uma linha de log não precisa ser *sobre* um registro: uma linha de auditoria, um webhook, uma amostra de métrica. Exigir um `id` ali faria o produtor inventar um, o que é pior que `NULL` porque parece real. |

Sob `append` a coluna nasce anulável e a linha sem chave fica com `NULL` — não
com `""`, que é outro fato. E o `brevis_ingestion_id` continua existindo: sem
chave, **o conteúdo é a chave**, e o mesmo documento duas vezes é o mesmo id.

**Nomear um campo que não está em `data` é erro nos dois modos.** "Você não
disse" e "você disse algo que não está lá" são enganos diferentes, e só o
primeiro passa:

```json
{"accepted":0,"rejected":["event 0: \"unique_key\" names \"pedido_id\" as this
 record's identity and \"data\".\"pedido_id\" is missing or empty. Drop
 \"unique_key\" to fall back to \"id\", or send the field"]}
```

**`description` é lido e hoje não vai a lugar nenhum.** Ele existe no contrato
para a página de ingestão do console, que ainda não foi escrita. Está dito aqui
porque um campo aceito em silêncio é um campo que alguém acredita estar
gravando.

### Sete colunas fixas, mais o que o registro traz

| coluna | | |
|---|---|---|
| `brevis_ingestion_id` | `STRING` | a identidade, e a chave do merge |
| `brevis_record_key` | `STRING` | `data[unique_key]` — de qual **registro** isto fala; `NULL` sob `append` |
| `brevis_operation` | `STRING` | `INSERT`, `UPDATE` ou `DELETE` |
| `brevis_received_at` | `TIMESTAMP` | a chegada, no **nosso** relógio — a coluna de partição |
| `brevis_loaded_at` | `TIMESTAMP` | a escrita, carimbada pelo **destino** |
| `brevis_stream` | `STRING` | qual rota escreveu |
| `brevis_gateway` | `STRING` | qual implantação |
| `brevis_received_bytes` | `INT64` | o tamanho com que o evento **chegou**, envelope incluído |

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

### Volume sem infra para medir volume

`brevis_received_bytes` é **grátis**: o gateway já conta esses bytes no decode
para alimentar o `buffer.flush.size`, e até então o número morria ali. Medir só
o `data` custaria um `json.Marshal` por evento — 2,0 µs contra os 3,1 µs que o
parse já gasta, **67% a mais no caminho quente** — para refinar um número cujo
trabalho é tendência e atribuição.

Ele responde o que as métricas **estruturalmente não alcançam**. Toda série
`brevis_gateway_*` é por *stream* e não carrega label `table`, de propósito: com
`auto_table` uma rota vira N tabelas e esse label é escolhido pelo produtor. Então
"qual tabela está crescendo, e desde quando" não tinha resposta em lugar nenhum.

```sql
select date(brevis_received_at) dia,
       count(*) eventos,
       sum(brevis_received_bytes)/1e9 gb
from app_orders
group by 1 order by 1 desc
```

Sem a coluna, a mesma pergunta significa varrer a coluna JSON inteira —
`sum(length(to_json_string(data)))` — o que escaneia cerca de **cinquenta vezes
mais bytes**, toda vez que alguém perguntar. A coluna se paga na primeira
consulta.

**É entrada, não armazenamento.** O destino guarda a linha tipada e comprimida:
400 bytes de JSON podem virar 80 no disco. Somar essa coluna dá o que *chegou*,
nunca o que é cobrado para manter.

E **o produtor não consegue escrevê-la**. O gateway carimba o valor depois do
hook e sobrescreve o que vier no envelope — um cliente que mandasse o campo
reportaria o próprio volume, e somar deixaria de significar alguma coisa.

### As séries de volume são opt-in

```bash
BREVIS_INGESTION_METRICS=true
```

```
brevis_gateway_ingested_bytes_total{stream,table}
brevis_gateway_ingested_events_total{stream,table}
```

**Desligadas por padrão**, e o padrão é o ponto: `table` é um label que o
*produtor* escolhe. Todo o resto deste arquivo é rotulado pelo que um operador
escreveu no YAML, então a contagem de séries é conhecida antes do processo
subir. Ninguém deveria descobrir uma conta de métricas porque atualizou.

Um valor que ninguém consegue interpretar lê como **desligado**, nunca como
falha: isso é observabilidade, e um erro de digitação aqui não pode ser o motivo
de um endpoint de ingestão não subir.

A coluna existe dos dois jeitos. A métrica compra o *agora*; a coluna compra o
*desde quando* e o *de quem*.

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

No BigQuery isso passou a valer só na **0.13.0**. Antes dela o
`auto_table` declarava `Evolve: additive` em todo destino e só os drivers de
Postgres e MySQL liam a declaração — então lá o formato ficava congelado na
criação e nada dizia isso.

> **Vindo de 0.11.0 ou 0.12.0**, as tabelas criadas antes da `0.11.0` precisam
> de uma migração única, porque a oitava coluna fixa entrou na declaração e não
> na tabela:
>
> ```sql
> ALTER TABLE `<dataset>.<tabela>` ADD COLUMN IF NOT EXISTS brevis_received_bytes INT64
> ```
>
> Postgres e MySQL nunca foram afetados.

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

**`ttl: 0` significa nunca**, e dizer nada continua pegando o padrão de um
minuto — três estados, não dois.

O relógio **não** é o que recupera de uma entrada errada. Uma escrita que o
destino recusa chama `invalidate`, que derruba a entrada na hora e deixa o
retry da própria esteira voltar contra um cache frio: uma tabela dropada na mão
custa **uma tentativa falha**, não um minuto.

Expirar tem preço. O `sinkFor` cobra o `naming.max_new_per_hour` quando **não
sabe** de uma tabela — então um miss cobra por uma tabela que existe há semanas,
e um pod que reinicia um minuto depois do último evento de uma tabela não
aproveita nada do backend compartilhado que está pagando. Com tabelas de vida
longa, `ttl: 0` é a resposta.

> Em `0.12.0` e `0.13.1` essa grafia **não parseia** — o YAML lê um zero puro
> como inteiro e a `0.13.2` é a primeira que aceita as duas. Nessas versões,
> escreva `ttl: 0s`.

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
