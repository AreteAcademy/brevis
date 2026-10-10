---
title: Ingestão
description: Um endpoint HTTP que pousa dados: valida, dá identidade, agrupa e entrega. Sua própria imagem, sua própria versão.
group: Ingestão
order: 18
slug: ingestion
---

O gateway de ingestão é um **endpoint HTTP que pousa dados**. Um cliente faz
`POST`, recebe `202`, e o que chegou vira registro num tópico, numa tabela ou
num bucket.

![O fluxo do Gateway: um POST em /v1/clicks é decodificado, recebe seu ingestion_id, é respondido com 202 na hora e entra num lote; um worker entrega o lote ao destino, aqui arquivos. O Gateway não tem banco de dados.](/assets/flow-gateway.svg)

Ele é **outro binário, outro módulo Go, outra versão e outra imagem**. O motor
orquestra e nunca toca no dado do cliente; o gateway não faz outra coisa. A
numeração é independente de propósito: o motor está em 0.17 e o gateway em 0.27,
que é a afirmação verdadeira.

Com o `gateway.yaml` da seção abaixo, e a chave que ele pede em `keys_from`:

```bash
docker run -p 8080:8080 -e BREVIS_INGEST_KEYS=dev-key \
  -v ./gateway.yaml:/etc/brevis/gateway.yaml:ro \
  areteacademy/brevis-gateway:0.27.1
```

```text
2026/10/09 01:03:01 drain budget 30s; set terminationGracePeriodSeconds >= 40
2026/10/09 01:03:01 connections idle out after 1m30s and are recycled after 1m0s
2026/10/09 01:03:01 listening on :8080
2026/10/09 01:03:01 metrics on :9090/metrics
```

Um `POST` volta `202` na hora:

```bash
curl -X POST localhost:8080/v1/clicks -H 'Authorization: Bearer dev-key' \
  -d '{"event_id":"e-1","occurred_at":"2026-10-08T12:00:00Z","page":"/pricing"}'
```

```text
{"accepted":1,"rejected":null}
```

Sem credenciais do Google Cloud, o lote é tentado de novo e vai para a fila de
descarte — veja [Quando o destino recusa](#quando-o-destino-recusa).

A imagem `-slim` carrega só os destinos `auto_table`, `files` e `postgres`; o
`pubsub` deste exemplo e os outros estão na imagem completa. Uma slim com um
destino que ela não tem é recusada na subida, com o motivo.

## O arquivo é o contrato

Não há código para escrever, exceto os *hooks*. Streams, caminhos, formatos,
identidade, retry, destino e fila de descarte — tudo no YAML, e todo campo é
recusado quando não pode ser honrado.

```yaml
name: events_gateway

listen:
  addr: :8080
  max_body: 1MiB
  idle_timeout: 90s                  # o padrão; não pode ser desligado
  max_conn_age: 60s                  # desligado a menos que você ponha
  auth:
    type: bearer
    keys_from: BREVIS_INGEST_KEYS   # o NOME da variável, nunca as chaves

metrics:
  addr: :9090                        # porta própria, nunca a de ingestão

streams:
  - name: clicks
    path: /v1/clicks
    format: json                     # json | array | ndjson — declarado, nunca adivinhado

    identity:
      provider: web
      entity: click
      source_key: event_id
      record_ts: occurred_at

    buffer:
      flush: {every: 1s, records: 500}

    sink:
      type: pubsub
      project: acme-prod
      topic: clicks

    dead_letter:
      type: files
      path: /var/dead-letter/
```

`format` é declarado e nunca inferido: adivinhar é como um lote de mil vira uma
linha contendo um array.

## O `ingestion_id`, que é o ponto

Todo registro sai com um `ingestion_id`: um **UUID v5 congelado** sobre
`provider|entity|source_key|record_ts`.

É o **mesmo id** que um fetcher em lote calcula para o mesmo registro. Uma linha
que o gateway grava e uma linha que um pipeline do [SDK](/docs/sdk/) grava são
*a mesma linha*, sem nenhuma reconciliação entre as duas.

Duas consequências práticas:

- **Um `POST` repetido é o mesmo registro.** O cliente pode reenviar sem medo;
  um destino com `merge` absorve a repetição em vez de gravar uma segunda
  linha.
- **A fórmula é congelada.** Os quatro campos são obrigatórios, e deixar um de
  fora produz um id *diferente*, não um id mais fraco. O arquivo recusa a
  configuração incompleta em vez de aceitá-la.

Ferramentas de ingestão movem bytes muito bem e não têm opinião sobre o que um
registro *é*. Essa opinião é o que o gateway acrescenta.

## A requisição não espera o destino

Ela decodifica, roda o hook, calcula a identidade, empilha no buffer e volta.
**Só isso.** Um lote cheio vai para uma pool de workers, e a publicação, o
`COPY`, os retries e a fila de descarte acontecem lá.

Isso não é cosmético. Antes de existir, a requisição que por acaso fechava o
lote rodava a entrega inline — então um chamador a cada `flush.records` pagava
a ida e volta inteira, e com o destino fora, a janela inteira de retry. Um p99
moldado por quem teve azar não é um p99 sobre o qual alguém age.

```yaml
buffer:
  flush:
    every: 1s         # idade
    records: 500      # contagem
    size: 8MiB        # bytes — o que for atingido PRIMEIRO manda o lote
    max_age: 30s      # e este manda DE QUALQUER JEITO
  workers: 4          # lotes entregues ao mesmo tempo
  queue: 64           # lotes cheios esperando um worker
  max_records: 10000  # eventos em memória antes de o gateway dizer não
  max_bytes: 256MiB   # o mesmo teto em bytes
```

### O relógio do flush pertence ao deployment

`buffer.flush.every` é um `time.AfterFunc` em **cada réplica**. Então os load
jobs que uma tabela recebe são função da contagem de réplicas, não do tráfego:

```
jobs / dia / tabela  =  réplicas × 86400 / every
```

No BigQuery, contra 1.500 por tabela por dia, **duas réplicas a 60s já são 192%
de uma cota que não sobe** — antes de um HPA fazer qualquer coisa. E a direção é
contraintuitiva: o gateway escala para concorrência HTTP enquanto o destino quer
concentração.

```yaml
buffer:
  flush:
    every: 60s
    max_age: 300s
    claim: true
```

Com `claim`, a réplica pega um *set-if-absent* para o stream e a janela antes de
descarregar no timer — e o timer acorda na **fronteira da janela**, para todas
pedirem a mesma chave. Um flush por janela, qualquer que seja a contagem de
réplicas. E ele se auto-ajusta: uma réplica só espera quando outra está de fato
competindo, então escalar para baixo devolve a latência menor sem ninguém editar
config.

Medido com duas réplicas e um Redis, janelas de 5s: **8 load jobs viraram 6, uma
por janela, alternando 3/3** — nenhuma janela com duas.

**Precisa de backend compartilhado.** Com `memory` cada réplica ganha o próprio
claim e nada coordena. E **exige `max_age`**, que o config recusa sem: o claim
transforma `every` em alvo, e alvo sem teto é latência sem limite.

**Metastore fora do ar descarrega assim mesmo.** Perder coordenação custa load
jobs; recusar o flush segura dado. Verificado derrubando o Redis no meio de uma
corrida: 40 de 40 eventos pousaram, zero segurados.

**`outcome="unreachable"` é o que merece alerta.** O backend não pôde ser
consultado, então o claim falhou aberto: a coordenação está desligada e os load
jobs voltaram a multiplicar por contagem de réplica — em silêncio, porque todo
flush continua parecendo um flush.

E o claim dá **exclusividade, não justiça**: quem pede primeiro vence. Uma
corrida real com duas réplicas mediu 4/1, não uma divisão par. É o `max_age` que
impede a azarada de passar fome.

Com `claim: true` e `metastore: memory`, o gateway avisa **uma vez no boot**:
cada réplica vence o próprio claim, então tudo parece certo e nada coordena.

### `every` é um alvo; `max_age` é uma promessa

Os três gatilhos acima decidem quando um lote **tenta** sair. O `max_age` decide
quando ele sai **de qualquer jeito**.

Hoje a diferença é inerte, porque nada adia o alvo. Ela passa a valer quando o
relógio do flush deixar de ser por processo: `buffer.flush.every` é um
`time.AfterFunc` em cada réplica, então o número de load jobs que uma tabela
recebe é função da **contagem de réplicas**, não do tráfego —
`réplicas × 86400 / every`. No BigQuery, com 1.500 por tabela por dia, **duas
réplicas a 60s já são 192% da cota**.

Quando uma réplica puder ceder a janela para outra, ela continua enchendo — e é
o `max_age` que garante que ninguém segura dado indefinidamente esperando a vez.

Ele também resolve o dreno: o que um buffer pode estar **segurando** no
`SIGTERM` passa a ser limitado por ele, e é dele que sai o
`terminationGracePeriodSeconds` que o gateway imprime no boot.

Um teto **abaixo** do alvo é recusado no carregamento — significaria que o alvo
nunca se aplica.

### Por que existe um gatilho de tamanho

Os outros dois são **contagens**, e uma contagem não distingue 500 eventos de
2 KB de 500 eventos de 2 MB — 1 MB contra 1 GB, o mesmo número no arquivo. O
`max_records: 10000` tem o mesmo ponto cego: são 20 MB ou 20 GB, e nada no YAML
conseguia dizer qual.

Um produtor que passa a mandar o documento inteiro em vez do id derruba o pod, e
antes disso **não existia campo que expressasse o limite**.

O tamanho é medido no que **chegou** — não no que o evento pesa em memória (um
`map[string]any` é várias vezes o JSON dele) e não depois de um hook que o
infle. É uma aproximação, e é a certa: sai de graça no decode, anda junto com o
payload, e a coisa contra a qual ela protege é um registro que cresceu dez
vezes.

**No BigQuery `size` é teto, não alvo.** Aquele destino permite 1.500 load jobs
por tabela por dia — é por isso que `every` tem piso de 60s. Mas o piso governa
o **timer**: um `size` que dispara a cada poucos segundos passa direto por ele e
gasta a mesma cota. Nada recusa isso no carregamento, porque a taxa de chegada
não é conhecível ali. O que existe é visibilidade:

```
brevis_gateway_flushes_total{stream,trigger}   trigger = time | records | size
```

`trigger="size"` subindo num stream de BigQuery está te dizendo que não é a
janela que está fazendo o lote.

Esse contador é por stream, e a cota é por **tabela**: com `auto_table` uma
rota vira muitas tabelas, e 94 delas reportando um número só não diz nada sobre
nenhuma. Atrás de `BREVIS_INGESTION_METRICS` existe um que nomeia a tabela:

```
brevis_gateway_table_flushes_total{stream,table,trigger}
```

Um flush de uma tabela é um load job dela, então esta é a série para alertar em
1.200 dos 1.500 que o BigQuery permite. A metade incondicional é uma linha no
boot, que diz quanto `flush.every` custa por tabela por dia antes de qualquer
evento chegar.

**É limitado, não é "atira e esquece".** Uma goroutine por lote transformaria a
queda de um destino em memória sem teto. Quando a fila e o buffer estão cheios,
a resposta é **`503` com `Retry-After`** em vez de um `202` para um evento sem
lugar. Um evento aceito e nunca entregue é o único desfecho que este serviço
existe para não ter.

E esse `503` é **seguro de repetir**, de um jeito que quase nenhum serviço
consegue afirmar: o `ingestion_id` é função congelada do próprio evento, então
o mesmo corpo reenviado é o *mesmo registro*.

## Uma conexão decide qual réplica recebe a carga

Atrás de um `ClusterIP` do Kubernetes, o kube-proxy escolhe o backend **uma
vez por conexão TCP**, não por requisição. A maioria dos clientes mantém
conexões vivas e as reutiliza, então um produtor com um punhado delas manda
tudo para as mesmas uma ou duas réplicas, quantas quer que estejam rodando —
e uma réplica que o HPA adiciona não recebe nada, porque ninguém disca para
ela.

```yaml
listen:
  idle_timeout: 90s     # fecha uma conexão a que ninguém voltou
  max_conn_age: 60s     # depois disso, a próxima resposta pede que o cliente rediscque
```

**`max_conn_age` vem desligado**, de propósito: por quanto tempo um cliente
pode manter uma conexão é política, e troca um handshake dentro do cluster
por conexão por idade pela carga se espalhando. A requisição é sempre
servida — o cabeçalho vai na resposta, nada é repetido e nenhum lote se
perde.

**`idle_timeout` tem padrão e não pode ser desligado.** Servir sem nenhum é
um descritor de arquivo por cliente abandonado até o processo reiniciar.
Ponha um valor longo se é isso que você quer; aí é um número que alguém
escolheu.

Os dois são independentes: uma conexão encontra o limite que vier primeiro,
e só chega à idade sendo usada — que é exatamente o produtor de conexão longa
que a idade existe para reequilibrar.

## Quando o destino recusa

O lote é tentado de novo — quatro vezes em cerca de sete segundos por padrão,
dobrando com jitter — e então vai para a **fila de descarte**, com o motivo em
cada registro:

```json
{
  "event_id": "dl-9",
  "ingestion_id": "84aaee4b-66af-5cc0-b97b-6856dec95b25",
  "_dead_letter_reason": "rpc error: code = NotFound desc = Topic not found",
  "_dead_letter_sink": "pubsub:acme-prod/clicks",
  "_dead_letter_at": "2026-09-25T02:02:32Z"
}
```

O motivo viaja **no registro** e não só num log, porque quem encontra esse
arquivo depois tem os eventos e não tem o log — e *por que isto está aqui* é a
primeira pergunta.

**Um stream sem `dead_letter` é recusado no carregamento.** Deixar o padrão no
silêncio colocaria a decisão onde ninguém a toma, e um lote recusado que é só
uma linha de log é perder dado em silêncio.

## O hook é Go, compilado junto

```go
hooks := gateway.NewHooks()
hooks.MustRegister("enrich_clicks", enrichClicks)
gateway.Main(hooks, gateway.WithSinks(sinks))

func enrichClicks(e map[string]any) (map[string]any, error) {
    host, _ := e["host"].(string)
    e["tenant"] = strings.Split(host, ".")[0]
    return e, nil     // devolver nil DESCARTA o evento, de propósito
}
```

O YAML nomeia um hook; ele não carrega um. Medido antes de escolher: uma função
Go custa **93 ns/op**, contra 959 do Starlark e 1.281 do yaegi, sem nada
acrescentado ao binário — e `plugin.Open` nem é opção, porque sob
`CGO_ENABLED=0`, que é como todo artefato aqui é compilado, ele devolve
`plugin: not implemented`.

O que isso custa, dito onde alguém vai ler: **acrescentar um hook é recompilar
e implantar, não mudar configuração.** É a troca certa enquanto os hooks são
escritos por quem publica o binário, e a errada no dia em que um cliente
precisar mudar um sem release.

Um erro no hook manda *aquele* evento para o descarte e nunca derruba o lote ao
lado. Um `panic` também: ele é recuperado por evento, porque um registro
malformado não pode derrubar um processo que serve todos os outros streams.

## Quando o evento é grande demais

```yaml
oversize:
  larger_than: 256KiB
  archive: {type: files, path: gs://acme-oversize/clicks/}
  hook: strip_heavy_clicks
```

O evento é escrito **inteiro** no `archive`, e uma versão reduzida segue no
stream carregando o ponteiro de volta:

```json
{
  "event_id": "big-1",
  "ingestion_id": "bd8ac083-9796-50f5-bab6-77a4e2e57ad0",
  "_oversize": true,
  "_oversize_archive": "files:gs://acme-oversize/clicks/",
  "_oversize_bytes": 2149
}
```

É o padrão **Claim Check**, e ele ganha de um `413` seco porque o `413` perde o
evento — e um payload grande demais costuma ser o mais interessante que alguém
tem: é a requisição com o documento inteiro anexado, que é o caso que vale
depurar.

`larger_than` mede **um evento**, depois do hook. `listen.max_body` é outra
coisa: limita a **requisição** e recusa com `413` antes de um byte ser lido.

## O que ele conta

Onze séries Prometheus, numa **porta própria** — nunca na de ingestão, e essa
regra pesa mais aqui do que no motor: a porta de ingestão é pública por
construção, é onde o cliente faz `POST`, então um `/metrics` nela publicaria
todo nome de stream, caminho e destino para quem encontrasse o path. Declarar o
mesmo endereço nos dois é recusado no carregamento.

```
brevis_gateway_events_received_total{stream,format}     contador
brevis_gateway_events_rejected_total{stream,reason}     contador  por EVENTO, depois do decode
brevis_gateway_requests_refused_total{stream,reason}    contador  por REQUISIÇÃO: unauthorized|body_too_large|malformed|empty|saturated
brevis_gateway_batches_total{stream,sink,outcome}       contador  delivered|retried|buried
brevis_gateway_flushes_total{stream,trigger}            contador  time|records|size
brevis_gateway_flush_windows_total{stream,outcome}      contador  won|yielded|ceiling|unreachable
brevis_gateway_ingested_bytes_total{stream,table}        contador  opt-in
brevis_gateway_ingested_events_total{stream,table}      contador  opt-in
brevis_gateway_table_flushes_total{stream,table,trigger}  contador  opt-in
process_start_time_seconds                              gauge     sem prefixo, de propósito
brevis_gateway_saturated_total{stream}                  contador  os 503
brevis_gateway_delivery_seconds{stream,sink}            histograma
brevis_gateway_buffer_records{stream}                   gauge
brevis_gateway_queue_batches{stream}                    gauge
```

`process_start_time_seconds` **não** leva o prefixo `brevis_`, e isso é
proposital: coletores procuram exatamente esse nome. Ele diz quando o processo
começou, e sem ele um coletor que faz ajuste de hora de início — o receptor
Prometheus do OpenTelemetry, sobre o qual o Google Managed Prometheus é
construído — ancora cada série cumulativa no **primeiro scrape** e gasta o valor
dele como linha de base. O resultado é todo contador lendo baixo, com os deltas
certos e os totais errados.

As três últimas são as que ninguém pede antes do primeiro incidente.
`saturated_total` é o único número que diz que um cliente foi mandado esperar;
`buffer_records` contra `buffer.max_records` é o único que **prevê** isso.

`reason` é um conjunto fechado e nunca o texto do erro: uma string de erro
carrega nome de tabela, coluna, às vezes linha, e um cliente malformado
cunharia uma série nova por requisição. O texto fica na fila de descarte, no
registro.

## `202`, não `200`

O gateway **aceitou** os eventos. Com `durability: memory` — a única camada
implementada — é tudo o que ele pode afirmar honestamente. A camada que merece
um `200` é a que já escreveu os eventos em algum lugar, e ela não existe.

`disk` é recusado **pelo nome**, não aceito e tratado como memória: quem escreve
`disk` acredita que seus eventos sobrevivem a uma queda, e concordar sem cumprir
é exatamente a falha que esse campo existe para impedir.

O que está no buffer no desligamento é entregue, não descartado — pelo mesmo
caminho que um lote cheio toma, com retry e descarte.

## O que ele não faz

- **Não aparece no console.** Ele não compartilha banco nenhum com a interface
  do motor, então não existe página `/ingestion` — nem a evolução de schema que
  o `auto_table` faz aparece lá. É o próximo passo e ainda não foi escrito.
- **Um stream com `table` não evolui schema.** Quando você nomeia a tabela, o
  contrato é dela: um campo que ela não tem é recusado com a mensagem que
  resolve, em vez de alterar uma tabela que alguém revisou. Crescer uma coluna
  sozinho é o que o `auto_table` faz, e é a diferença entre os dois.
- **Um stream com `table` não cria tabela nem índice.** Um serviço que cria
  tabelas transforma um erro de digitação numa segunda tabela que ninguém está
  lendo — por isso criar só acontece sob `auto_table`, onde `naming` limita que
  nome pode existir e `listen.auth` é obrigatório.
- **A imagem publicada não tem hooks**, que é o artefato honesto para um desenho
  de hook compilado: ela serve streams que não declaram `hook:`.

## Uma rota, N tabelas

O `auto_table` roteia cada evento para a tabela que o **envelope** nomeia, cria
a tabela se ela não existir e cresce uma coluna quando um campo novo aparece:

```json
{"table_name": "app_orders", "operation": "INSERT", "unique_key": "id",
 "data": {"id": "A-1", "total": 150, "customer": {"id": 7, "uf": "SP"}}}
```

Campos de controle em cima, o registro dentro de `data`, e sete colunas
`brevis_*` de rastreio em toda tabela. Uma rota, N tabelas, nada declarado.

Está em [Destinos da ingestão](/docs/ingestion-sinks/#uma-rota-n-tabelas-nada-declarado).

## Por onde seguir

| se você quer | vá para |
|---|---|
| a lista de destinos e como cada um escreve | [Destinos da ingestão](/docs/ingestion-sinks/) |
| entender o `ingestion_id` do outro lado | [SDK em Go](/docs/sdk/) |
| montar painel e alerta | [Observabilidade](/docs/observability/) |
