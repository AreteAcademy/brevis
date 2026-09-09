---
title: Bibliotecas cliente
description: Como um passo em qualquer linguagem lê o contexto e o relógio do run — e quais linguagens já têm biblioteca.
group: SDK e bibliotecas
order: 13
slug: libraries
---

Um passo precisa de duas coisas do orquestrador: **o que o passo anterior
publicou** e **que relógio este run deve ler**. As bibliotecas cliente entregam
essas duas, e nada além.

## O contrato vem antes das bibliotecas

O que o engine define são **duas variáveis de ambiente e um arquivo**:

| | |
|---|---|
| `BREVIS_INPUT` | `{"<id do passo>": {…}}` — o que as dependências publicaram |
| `BREVIS_OUTPUT` | um caminho onde escrever **um** objeto JSON |

Mais as variáveis do run e os parâmetros automáticos, todas de leitura:
`BREVIS_RUN_*` e `BREVIS_AUTO_*`. Em Kubernetes o `BREVIS_OUTPUT` é
`/dev/termination-log`, que o engine já lê para obter o código de saída.

Então **não há chamada de API, não há token e não há porta**. Um passo em
qualquer linguagem participa com um parse de JSON e uma escrita em arquivo:

```bash
window=$(jq -r '.extract.watermark' <<< "$BREVIS_INPUT")
./publish --until "$window"
jq -n --arg n "$(date -Is)" '{published_at: $n}' > "$BREVIS_OUTPUT"
```

Uma biblioteca aqui não abre capacidade nova — ela poupa a validação, o
tratamento de ausência e as mensagens de erro que valem a pena não reescrever.

## Quais existem

| linguagem | | |
|---|---|---|
| **Python** | `pip install brevis` | [disponível](/docs/python/) — `0.2.1` no PyPI |
| **Go** | `brevis/sdk/context` | disponível, dentro do módulo do SDK |
| **Node.js** | — | planejado |
| **Rust** | — | planejado |
| qualquer outra | as variáveis acima | hoje |

:::note Go não fica em `lib/`
O suporte a contexto em Go vive no módulo `sdk/` que o projeto já publica, como
subpacote que se poda a nada para quem não o importa. Um `lib/go-context` seria
um segundo módulo Go para um pacote que já tem módulo.

A regra é **uma pasta por linguagem que ainda não tenha artefato publicado**.
:::

Cada biblioteca é um artefato separado, com versão e tag próprias: uma correção
no cliente Python não força uma release do engine.

## O que elas são, e o que não são

São **clientes finos sobre o contrato**, não ports do SDK em Go.

Um port da maquinaria de ETL do SDK — drivers, paginação, checkpoints, ids de
ingestão — não pertence a este lugar e provavelmente a lugar nenhum: toda
linguagem que isso miraria já tem ferramenta melhor para aquilo, e o trabalho do
Brevis é executá-las. É por isso que a biblioteca Python existe para que um time
use pandas, Polars ou dbt e ainda receba a única coisa que um orquestrador tem
para dar.

## Métricas

A biblioteca **emite** métricas, e o engine é quem as expõe:

```python
from brevis import metrics

metrics.set("rows_loaded", 48213)      # gauge: a última escrita vence
metrics.inc("vendor_rejected_total")   # counter: soma
```

Elas saem no `/metrics` **do engine**, na porta do scheduler, rotuladas com o
workflow e o passo — ver [Observabilidade](/docs/observability/).

**Isso não abre porta, e não poderia.** Um passo roda no próprio pod por
quarenta segundos e sai; uma porta que ele abrisse seria raspada nunca, ou uma
vez por sorte. Então a linha vai para o stdout — o mesmo pipe que o engine já lê
para desenhar as fases de um pipeline — e o engine registra. Nada a instalar,
nada a operar, e os rótulos são do engine porque um passo não sabe o slug do
próprio workflow.

Os detalhes da API estão em [Python](/docs/python/#metricas-que-so-o-seu-pipeline-conhece).

## Escrevendo um cliente para outra linguagem

Se você for implementar Node.js ou Rust antes de nós, o que um cliente precisa
acertar — e o que a implementação em Python já aprendeu na prática:

1. **Chave qualificada pelo passo.** `get("bucket")` sem o passo tem de ser
   recusado. Dois passos podem publicar `bucket`, e escolher um por busca é
   precedência por acidente.
2. **Mesclar as escritas.** Duas chamadas com chaves diferentes preservam as
   duas; a mesma chave duas vezes mantém a última.
3. **Gravar só na saída do processo.** Um passo que morre de forma abrupta não
   publica nada — o que está certo, porque a saída de um passo que quebrou
   descrevia trabalho que não terminou.
4. **Recusar acima de 4096 bytes.** A plataforma **trunca** em vez de recusar,
   e um objeto cortado ao meio chega como JSON inválido para o passo seguinte.
5. **Funcionar fora do Brevis.** Sem `BREVIS_INPUT`, `get()` devolve o padrão e
   `set()` aceita e descarta, sem erro: um script que não roda à mão não pode
   ser desenvolvido.

## Próximos passos

- [Python](/docs/python/) — a API completa
- [Contexto entre passos](/docs/context/) — o conceito e o teto de 4096 bytes
- [Parâmetros](/docs/parameters/) — os declarados e os automáticos
