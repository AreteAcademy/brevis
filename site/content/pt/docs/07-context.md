---
title: Contexto entre passos
description: O que um passo diz ao seguinte — duas variáveis de ambiente e um arquivo.
group: Conceitos
order: 7
slug: context
---

Um passo publica valores; os passos que dependem dele leem. É tudo o que o
recurso faz.

```yaml
steps:
  - id: extract
    run: python fetch.py

  - id: transform
    run: python transform.py
    depends_on: [extract]
```

```python
# extract
from brevis import context
context.set(bucket="s3://landing/2026-09-07", rows=48213)
```

```python
# transform
from brevis import context
bucket = context.get("extract.bucket")
```

A chave é sempre `<id do passo>.<nome>`. Um passo lê o que **suas dependências**
publicaram, e nada além.

:::note O que isto não é
Não é fila de mensagens, não é estado entre execuções e não é lugar para
guardar dados. É o bilhete que um passo deixa para o próximo — um caminho, uma
marca d'água, uma contagem.
:::

## Qualquer linguagem, sem biblioteca

O contrato são **duas variáveis de ambiente e um arquivo**, então um passo sem
SDK nenhum participa:

```bash
window=$(jq -r '.extract.watermark' <<< "$BREVIS_INPUT")
./publish --until "$window"
jq -n --arg n "$(date -Is)" '{published_at: $n}' > "$BREVIS_OUTPUT"
```

| | |
|---|---|
| `BREVIS_INPUT` | `{"<id do passo>": {…}}` — o que as dependências publicaram |
| `BREVIS_OUTPUT` | um caminho onde escrever **um** objeto JSON |

Em Kubernetes esse caminho é `/dev/termination-log`, que o engine já lê para
obter o código de saída. Então **não há chamada de API, não há token e não há
porta** — e uma biblioteca para isso é um parse de JSON e uma escrita em
arquivo.

| linguagem | |
|---|---|
| Python | [`pip install brevis`](/docs/python/) — traz também `brevis.run`, com os [parâmetros automáticos](/docs/parameters/) |
| Go | `sdk/context`, parte do módulo do SDK, sem dependência |
| qualquer outra | as duas variáveis acima |

## Isolamento

Nenhum passo pode sobrescrever o que outro publicou. As chaves são prefixadas
pelo id de quem escreveu, e o engine — não o passo — decide esse prefixo. Dois
passos que publicam `bucket` produzem `extract.bucket` e `load.bucket`, e
nenhum dos dois some.

## O teto de 4096 bytes

O que um passo publica cabe em **4096 bytes**. O limite não é arbitrário: em
Kubernetes o transporte é o `/dev/termination-log`, e é o que o kubelet aceita.

Passar do teto é **erro do passo**, com a mensagem dizendo quantos bytes foram
escritos. A alternativa seria truncar — e um JSON truncado é um JSON inválido,
descoberto pelo passo seguinte, que não tem como saber que a culpa não é dele.

Se o valor não cabe, ele não é contexto: escreva num bucket e publique o
caminho.

## Retries e execuções retomadas

Numa nova tentativa, o passo publica de novo e o valor anterior é substituído.
Um passo que já tinha sucesso não re-executa nem republica — o que ele disse
continua valendo para os que dependem dele.

## Rodando um passo à mão

As duas variáveis são o contrato inteiro, então reproduzir uma execução é
exportá-las:

```bash
export BREVIS_INPUT='{"extract":{"bucket":"s3://landing/2026-09-07"}}'
export BREVIS_OUTPUT=/tmp/out.json
python transform.py
cat /tmp/out.json
```

## Próximos passos

- [Parâmetros](/docs/parameters/) — o que muda entre dois disparos, e os automáticos
- [Runtime do passo](/docs/runtime/) — o que o passo declara sobre onde executa
- [Python](/docs/python/) — a biblioteca cliente, e as outras linguagens
