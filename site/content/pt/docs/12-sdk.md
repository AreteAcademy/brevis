---
title: SDK em Go
description: Escrever um fetcher — extrair de uma origem, transformar e carregar num destino.
group: SDK e bibliotecas
order: 12
slug: sdk
---

Quando o trabalho de um passo é **buscar dados e carregá-los em algum lugar**, o
SDK em Go entrega o fetcher inteiro em poucas linhas. Ele é um módulo à parte,
versionado independentemente do engine.

```bash
go get github.com/AreteAcademy/brevis/sdk@latest
```

Requer Go 1.23 ou mais novo — o SDK entrega linhas como `iter.Seq2`.

:::warning Não use a `v0.1.0`
Ela saiu com um `go.mod` apontando para uma revisão que não existe, e o proxy de
módulos do Go é imutável. Comece na `v0.1.1`.
:::

## Três passos

```go
import (
	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to/bigquery"
)

dados, err := sdk.Extract(ctx, sdk.Source{
	From: from.HTTP{URL: "https://api.exemplo.com/v1/eventos"},
})

dados = sdk.Transform(dados, sdk.Accept("id", "criado_em", "valor"))

res, err := sdk.Load(ctx, dados, sdk.Target{
	To:      bigquery.Table{Dataset: "bronze", Name: "eventos"},
	Columns: []string{"id", "criado_em", "valor"},
})
```

`Extract` lê, `Transform` reformata, `Load` escreve. Cada um recebe e devolve
uma sequência — nada é materializado em memória de uma vez.

## O driver é um valor, não uma configuração

`from.HTTP` carrega tudo que uma origem HTTP precisa: URL, cabeçalhos, retry,
paginação e o que uma resposta significa. `from.Files` carrega um caminho e um
formato. Nenhum precisa abrir espaço para os campos do outro, então não existe
uma struct de origem com quarenta opções das quais cada driver lê seis.

Isso também decide **o que você compila**. Go poda dependências por pacote
importado, nunca por campo usado:

| o que você importa | pacotes | AWS | Google |
|---|---|---|---|
| `sdk` | 190 | não | não |
| `sdk` + `from` | 194 | não | não |
| `sdk` + `from` + `to` (arquivos) | 195 | não | não |
| `sdk` + `to/bigquery` | 456 | não | **sim** |
| `sdk` + `from` + `store/s3` | 265 | **sim** | não |

Um pipeline inteiro de arquivos — ler e escrever — custa 195 pacotes e nenhum
SDK de nuvem. **Um driver com SDK de fornecedor atrás vive no próprio pacote**,
que é por que BigQuery é `to/bigquery` e os object stores são `store/s3` e
`store/gcs`.

## Origens e destinos

| origem | pacote |
|---|---|
| HTTP | `from.HTTP` |
| arquivos (local, S3, GCS) | `from.Files` |
| Postgres | `from/postgres` |
| MySQL | `from/mysql` |
| várias origens | `from.Many` |

| destino | pacote |
|---|---|
| arquivos | `to.Files` |
| BigQuery | `to/bigquery` |
| Postgres | `to/postgres` |
| MySQL | `to/mysql` |
| Redshift | `to/redshift` |
| Pub/Sub | `to/pubsub` |

O esquema do caminho decide o backend, e o `Store` é **passado** em vez de
escolhido dentro do driver:

```go
from.Files{Path: "s3://bucket/dia=1/*.ndjson", Store: s3.New(cliente)}
to.Files{Path: "gs://bucket/landing/", Store: gcs.New(cliente)}
```

É isso que faz um programa de arquivos locais não compilar uma linha da AWS nem
do Google.

### Publicar num tópico

O `to/pubsub` é o único destino que não é uma tabela, e ele se comporta de um
jeito que vale saber antes de usar: **não acrescenta nada ao que envia.**

```go
Target: sdk.Target{
    To: pubsub.Topic{
        Project: "acme-prod",
        Name:    "orders",

        // Nulo é o padrão, e significa NENHUM atributo.
        Attributes: func(e sdk.Envelope) map[string]string {
            row, ok := e.Payload.(map[string]any)
            if !ok {
                return nil
            }
            return map[string]string{"orderId": fmt.Sprint(row["order_id"])}
        },
    },
},
```

Quem assina recebe o seu payload, byte por byte, mais exatamente os atributos
que você nomeou. Nada além disso — sem `ingestion_id`, sem `provider`, sem
envelope de espécie alguma.

Isso é uma regra, não minimalismo. Uma tabela é criada pelo pipeline que escreve
nela, então o SDK pode decidir as colunas dela. **Um tópico não**: ele existe
antes do seu pipeline, os assinantes dele foram escritos antes e os filtros
deles também. Um atributo acrescentado por conta própria seria o Brevis editando
o contrato de outra pessoa, e quem assina descobriria às três da manhã.

Daí decorrem três coisas:

- **Ele nunca cria um tópico.** Um pipeline que pode criar um pode criar o
  errado, e diferente de uma tabela com nome torto ninguém percebe — as
  mensagens vão para algum lugar, e o assinante que deveria recebê-las fica
  quieto.
- **`Schema`, `Dedup` e `PartitionBy` são recusados**, nomeando a opção. Eles
  existem para tabelas: um `Schema` é como um destino cria a dele, deduplicar
  precisa de uma linha para substituir, e partição é ideia de tabela. O parente
  aqui é o `OrderingKey`, que é outra coisa — ele agrupa mensagens que precisam
  *chegar* em ordem.
- **A falha é parcial.** Publicar não é transacional: 48 mil mensagens que
  falham em 31 mil *entregaram* 31 mil. O resultado informa o que de fato foi, e
  o erro diz que reexecutar é reentregar — quem assina precisa ser idempotente,
  e o Pub/Sub é at-least-once de qualquer forma.

Executável, contra o emulador:
[`examples/13-pubsub`](https://github.com/AreteAcademy/brevis/tree/master/examples/13-pubsub).

## Um fetcher inteiro

`sdk.Run` toma conta de flags, `-dry-run`, logging, retry, paginação,
procedência, criação de tabela e código de saída. O que sobra no arquivo é só o
que é específico daquela fonte:

```go
package main

import (
	"time"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
)

func main() {
	sdk.Run(sdk.Pipeline{
		Source: sdk.Source{
			From: from.HTTP{
				URL:     "https://api.exemplo.com/v1/eventos",
				Timeout: 15 * time.Second,
			},
			Guard:  sdk.RejectIf("error"),
			Expand: sdk.ArrayAt("results"),
		},
		Target: sdk.Target{
			Provider: "exemplo",
			Entity:   "eventos",
			Key:      sdk.Key("id"),
			When:     sdk.Field("created_at"),
		},
	})
}
```

```bash
go run ./fetcher -dry-run   # extrai, conta linhas e erros, não escreve
go run ./fetcher
```

## O contexto do run

Quando o fetcher roda **como passo de um workflow**, o engine injeta no ambiente
o que ele não teria como saber: se é a primeira execução, com que parâmetros foi
disparado, qual run é. O SDK lê isso em `Pipeline.Run`:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	if p.Run.Params["load_full"] == "true" {
		p.Source.From = from.HTTP{URL: base + "?full=1"}
	}
	return nil
},
```

Rodando à mão, `Run` vem zerado — ler é opcional, e ignorá-lo não custa nada.

Sem histórico, a resposta para "é a primeira execução?" é sempre **não**: criar
tabela sem certeza é pior do que não criar.

### O relógio

`p.Run.Auto` é o que o motor descobriu sobre este run — os
[auto params](/docs/parameters/#auto-params). `Auto.Now()` é o relógio a ler no
lugar de `time.Now()`: num run agendado ele é o slot, então não se move quando o
run atrasa nem quando o run é retentado três horas depois.

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	start, end, ok := p.Run.Auto.Window()
	if !ok { // sem agendamento: cai para uma janela fixa
		start, end = p.Run.Auto.Now().Add(-24*time.Hour), p.Run.Auto.Now()
	}
	p.Source.From = from.HTTP{URL: base +
		"?from=" + start.Format(time.RFC3339) +
		"&to=" + end.Format(time.RFC3339)}
	return nil
},
```

Rodando à mão, `Auto.Now()` *é* o relógio de parede e `Window()` devolve
`false`, então o desenvolvimento local não precisa de caso especial.

## Não confunda com as bibliotecas cliente

Este SDK é maquinaria de ETL em Go: drivers, paginação, procedência,
criação de tabela. As [bibliotecas cliente](/docs/libraries/) são outra
coisa — clientes finos que dão a um passo o contexto e o relógio do run,
em Python hoje e em Node.js e Rust depois. Um passo que usa pandas ou dbt
quer a segunda, não este.

## Referência

- [pkg.go.dev](https://pkg.go.dev/github.com/AreteAcademy/brevis/sdk) — a API completa
- [`examples/`](https://github.com/AreteAcademy/brevis/tree/master/examples) — doze exemplos executáveis
- [`CHANGELOG.md`](https://github.com/AreteAcademy/brevis/blob/master/CHANGELOG.md) — histórico versão a versão
