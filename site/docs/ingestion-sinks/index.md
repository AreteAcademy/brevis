# Destinos da ingestão

> Os seis destinos, como cada banco escreve um registro, e por que a imagem carrega só o que você usa.

*https://brevis.sh/docs/ingestion-sinks/ · brevis.sh docs (pt-BR)*

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

## O que ainda não existe

`sqlite`, `redis`, `dynamodb`, `kinesis`, `sqs`, `sns`, `kafka` e `rabbitmq`
estão planejados e nada foi escrito. A tabela no topo desta página diz o estado
de cada um, e ela é atualizada quando o estado muda — não antes.
