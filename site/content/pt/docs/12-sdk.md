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

## Parâmetros que montam a pipeline

`p.Run.Params` é legível dentro da pipeline, o que é tarde demais para um valor
que decide o que a pipeline **é** — uma source por estado, uma tabela por
cliente. `Pipeline.Flags` é parseado dentro do `Run`, então é tarde pelo mesmo
motivo.

```go
func main() {
	sdk.Run(pipeline(sdk.ParamList("ufs")))
}
```

`sdk.Param` e `sdk.ParamList` leem primeiro o ambiente do engine e depois
`-param nome=valor`:

```bash
./fetch-stations -param ufs=SP,RJ
```

O ambiente ganha — sob o engine ele é o valor, e uma flag esquecida num
manifesto não pode sobrepor o que o operador digitou. Sem engine, a flag é o que
existe, o que é melhor do que escrever `BREVIS_RUN_PARAMS` como JSON a cada
execução.

## O layout de aterrissagem

Uma pipeline pode aterrissar **a mesma tabela que um gateway aterrissa** — as
mesmas colunas e o mesmo `brevis_ingestion_id` — de modo que escolher entre os
dois vira decisão de implantação e não de esquema. Dá para ler os dois juntos,
e para migrar de um ao outro sem migração de dados.

```go
const table = "landing.orders"

sdk.Run(sdk.Pipeline{
    Source:    sdk.Source{From: from.HTTP(/* ... */)},
    Transform: []sdk.Transformer{sdk.Landing(table, sdk.LandingKey("id"))},
    Target: sdk.Target{
        To:     postgres.Table{DSN: dsn, Name: table, CreateTable: true},
        Schema: sdk.LandingSchema(sdk.LandingOptions{}),
    },
})
```

`sdk.LandingSchema` são nove colunas: as oito do layout e a do produtor.
`sdk.Landing` preenche essas colunas e põe o registro inteiro em `data`.
Acrescente colunas suas ao esquema se quiser, ou pegue
`sdk.LandingControlColumns` e dê colunas próprias aos campos do registro.

### Duas formas: o registro inteiro, ou uma coluna por campo

O que está acima é a forma **document** — o registro vai para `data` como
JSON, e a tabela é a mesma qualquer que seja o registro. Acrescente
`sdk.LandingColumns()` e cada campo do registro ganha uma coluna própria:

```go
sdk.Landing(table, sdk.LandingKey("source_key"), sdk.LandingColumns())
```

A tabela passa a ser `sdk.LandingControlColumns` mais as suas colunas — **não**
`sdk.LandingSchema`, que carrega o `data`:

```go
Target{
    To: postgres.Table{DSN: dsn, Name: table, CreateTable: true},
    Schema: append(sdk.LandingControlColumns(sdk.LandingOptions{Keyed: true}),
        sdk.Column{Name: "source_key", Type: sdk.TypeString},
        sdk.Column{Name: "series", Type: sdk.TypeString},
        sdk.Column{Name: "data", Type: sdk.TypeString},
        sdk.Column{Name: "valor", Type: sdk.TypeString},
    ),
}
```

**As colunas são você que declara.** O SDK nunca infere um esquema, e esta
opção não muda isso: ela decide como a *linha* é montada, nunca o que a tabela
é. Um gateway infere porque o produtor dele é um estranho que posta o que tem;
aqui quem escreve a pipeline conhece o formato e o escreve, num diff que
alguém revisa.

É a mesma forma que um gateway aterrissa como
[`shape: columns`](/docs/ingestion-sinks/#duas-formas-um-contrato), pelo mesmo
código — mesmas colunas, mesmos valores, mesmo `brevis_ingestion_id`.

**O nome do campo precisa poder ser nome de coluna**: letra ou sublinhado, e
depois letras, dígitos e sublinhados. É a regra do BigQuery, a mais estreita
dos quatro destinos, então um nome que passa aqui funciona em qualquer um. Um
registro com `meu-campo` é recusado pelo nome, antes de qualquer escrita. A
forma `document` não tem essa regra — um hífen é uma chave JSON perfeitamente
válida.

**Não achata.** Um objeto aninhado vira UMA coluna `JSON` sob a chave que o
continha: `customer` guarda `{"id": 7, "uf": "SP"}`, e não `customer_id` e
`customer_uf`. Um array também é `JSON`. Um registro que quer uma linha *por
elemento do array* quer o `sdk.ArrayAt` no `Expand` da fonte — ele roda antes
do `Landing`, e é outra operação com outro nome:

```go
Source: sdk.Source{From: from.HTTP(/* ... */), Expand: sdk.ArrayAt("results")},
```

**`null` continua `NULL`**, nunca `""`. Um campo enviado como null e um campo
enviado vazio são fatos diferentes, e depois a coluna não sabe distinguir.

**O `data` passa a ser seu.** Nesta forma o layout não tem coluna com esse
nome, então um produtor com um campo chamado `data` — que é exatamente o que a
série do Bacen manda — ganha uma coluna própria com o valor dele. No
`document`, o dele é uma chave dentro do JSON do layout.

**Não é um modo.** `Target.Schema` continua sendo você declarando o que a
tabela é — isso entrega um layout que já existe em vez de fazer você digitá-lo.
Nada no SDK se comporta de outro jeito por você ter usado, e uma pipeline que
declara o esquema dela não é afetada.

**Escreva o nome da tabela uma vez.** Ele vai para o `Landing`, porque é o
segundo campo do id, e para o destino, porque é onde as linhas vão. **Nada
verifica que os dois concordam**: o `Writer` conhece a própria tabela mas só a
expõe pelo `Describe`, que é o nome para logs e erros, e um id construído
sobre isso amarraria todo id já escrito a uma string de log. Deixe os dois
divergirem e as linhas aterrissam certas com ids cunhados para uma tabela em
que ninguém escreveu. Vão parecer corretas. Não vão bater com as do gateway.

**Três colunas ficam `NULL`, e é a resposta honesta.** `brevis_stream` e
`brevis_gateway` nomeiam coisas que uma pipeline não tem —
`brevis_gateway IS NULL` é como se distingue a linha de uma pipeline da de um
gateway. `brevis_received_bytes` também fica `NULL`: o número do gateway conta
o que chegou no fio, envelope incluído, e o JSON do registro é uns 30% menor.
Uma coluna com dois sentidos faria uma soma entre linhas dos dois caminhos
errar por quanto veio de onde. `length(data)` responde a versão da pipeline
exatamente.

**Merge precisa de `DedupKey`, e sem ele não há merge nenhum.** Todo driver
casa por `ingestion_id` salvo instrução contrária, e a coluna de identidade
deste layout é `brevis_ingestion_id`:

```go
Target{
    To:       postgres.Table{DSN: dsn, Name: table, CreateTable: true},
    Schema:   sdk.LandingSchema(sdk.LandingOptions{UniqueID: true, Keyed: true}),
    Dedup:    sdk.DedupMerge,
    DedupKey: sdk.LandingColumnID,
}
```

`UniqueID` é o que põe a constraint UNIQUE no id, e Postgres e MySQL recusam
fazer merge sem uma — o `ON CONFLICT` não teria com o que casar. Um `DedupKey`
que seu `Schema` não declara é recusado antes de qualquer coisa rodar: um merge
numa coluna que a tabela não tem não casa nada, e um merge que não casa nada
parece exatamente um que casa tudo que deveria.

**Depende do destino, e hoje o MySQL não consegue.** O BigQuery não tem
constraints UNIQUE e recusa a declaração. No MySQL, `string` é `LONGTEXT`, e o
MySQL não indexa um `LONGTEXT` sem comprimento — então uma tabela de
aterrissagem com merge não pode nem ser *criada* lá:

```
Error 1170 (42000): BLOB/TEXT column 'brevis_ingestion_id' used in key
specification without a key length
```

O Postgres funciona porque lá `string` é `TEXT`. No MySQL, aterrisse com
`DedupNone` e resolva a versão corrente adiante, ou crie a tabela você mesmo
com `CreateSQL` e um id dimensionado.

**O id é endereçado por conteúdo.** O mesmo registro produz o mesmo id nos dois
caminhos, e um registro alterado produz um novo — que é o que faz uma
reexecução ser um no-op em vez de duplicata.

## Referência

- [pkg.go.dev](https://pkg.go.dev/github.com/AreteAcademy/brevis/sdk) — a API completa
- [`examples/`](https://github.com/AreteAcademy/brevis/tree/master/examples) — doze exemplos executáveis
- [`CHANGELOG.md`](https://github.com/AreteAcademy/brevis/blob/master/CHANGELOG.md) — histórico versão a versão
