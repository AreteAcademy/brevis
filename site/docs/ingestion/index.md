# Ingestão

> Um endpoint HTTP que pousa dados: valida, dá identidade, agrupa e entrega. Sua própria imagem, sua própria versão.

*https://brevis.sh/docs/ingestion/ · brevis.sh docs (pt-BR)*

---

O gateway de ingestão é um **endpoint HTTP que pousa dados**. Um cliente faz
`POST`, recebe `202`, e o que chegou vira registro num tópico, numa tabela ou
num bucket.

```
POST /v1/clicks  →  decodifica  →  hook  →  ingestion_id  →  lote  →  202
                                                             ↓
                                              workers  →  Pub/Sub | Postgres | …
```

Ele é **outro binário, outro módulo Go, outra versão e outra imagem**. O motor
orquestra e nunca toca no dado do cliente; o gateway não faz outra coisa. A
numeração é independente de propósito: o motor está em 0.15 e o gateway em 0.3,
que é a afirmação verdadeira.

```bash
docker run -p 8080:8080 \
  -v ./gateway.yaml:/etc/brevis/gateway.yaml:ro \
  areteacademy/brevis-gateway:0.3.2-slim
```

## O arquivo é o contrato

Não há código para escrever, exceto os *hooks*. Streams, caminhos, formatos,
identidade, retry, destino e fila de descarte — tudo no YAML, e todo campo é
recusado quando não pode ser honrado.

```yaml
name: events_gateway

listen:
  addr: :8080
  max_body: 1MiB
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
que o gateway grava e uma linha que um pipeline do [SDK](/docs/sdk/index.md) grava são
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
  flush: {every: 1s, records: 500}
  workers: 4          # lotes entregues ao mesmo tempo
  queue: 64           # lotes cheios esperando um worker
  max_records: 10000  # eventos em memória antes de o gateway dizer não
```

**É limitado, não é "atira e esquece".** Uma goroutine por lote transformaria a
queda de um destino em memória sem teto. Quando a fila e o buffer estão cheios,
a resposta é **`503` com `Retry-After`** em vez de um `202` para um evento sem
lugar. Um evento aceito e nunca entregue é o único desfecho que este serviço
existe para não ter.

E esse `503` é **seguro de repetir**, de um jeito que quase nenhum serviço
consegue afirmar: o `ingestion_id` é função congelada do próprio evento, então
o mesmo corpo reenviado é o *mesmo registro*.

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
brevis_gateway_events_rejected_total{stream,reason}     contador
brevis_gateway_batches_total{stream,sink,outcome}       contador  delivered|retried|buried
brevis_gateway_saturated_total{stream}                  contador  os 503
brevis_gateway_delivery_seconds{stream,sink}            histograma
brevis_gateway_buffer_records{stream}                   gauge
brevis_gateway_queue_batches{stream}                    gauge
```

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
  do motor. Não há página `/ingestion`, e não há uma planejada.
- **Não evolui schema.** Um campo que a tabela não tem é recusado. Há um plano;
  não há a funcionalidade.
- **Não cria tabela nem índice.** Um serviço que cria tabelas transforma um erro
  de digitação numa segunda tabela que ninguém está lendo.
- **A imagem publicada não tem hooks**, que é o artefato honesto para um desenho
  de hook compilado: ela serve streams que não declaram `hook:`.

## Uma rota, N tabelas

O `auto_table` roteia cada evento para a tabela que o próprio payload nomeia, e
a cria se não existir — quatro colunas fixas com o documento numa coluna `JSON`.
Uma rota, N tabelas, nada declarado.

Está em [Destinos da ingestão](/docs/ingestion-sinks/#uma-rota-n-tabelas-nada-declarado).

## Por onde seguir

| se você quer | vá para |
|---|---|
| a lista de destinos e como cada um escreve | [Destinos da ingestão](/docs/ingestion-sinks/index.md) |
| entender o `ingestion_id` do outro lado | [SDK em Go](/docs/sdk/index.md) |
| montar painel e alerta | [Observabilidade](/docs/observability/index.md) |
