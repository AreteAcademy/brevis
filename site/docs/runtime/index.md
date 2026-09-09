# Runtime do passo

> O que o passo declara sobre a linguagem e as ferramentas que dirige — e por que quase nunca é preciso declarar.

*https://brevis.sh/docs/runtime/ · brevis.sh docs (pt-BR)*

---

O grafo desenha um selo em cada passo dizendo a linguagem e as ferramentas que
ele dirige:

```
┌──────────────────────────────────────┐
│ ● transform            SDK v0.57.0   │
│   dbt build --select gold            │
│   ⬤ Python  ▢ dbt                    │
│   2m14s · attempt 1                  │
└──────────────────────────────────────┘
```

**Na maior parte dos casos você não escreve nada.** O engine lê `run:` e
`image:` e deduz.

## Quando declarar

Dois campos, ambos opcionais, ambos por passo:

```yaml
steps:
  - id: fetch
    run: /opt/brevis/bin/fetch-weather
    runtime: go            # um binário compilado; o caminho não diz nada

  - id: transform
    run: dbt build --select gold+
    tools: [dbt]           # o que ele dirige, não o que ele é
```

| campo | |
|---|---|
| `runtime` | a linguagem do passo: `go`, `python`, `node`, `shell`… |
| `tools` | as ferramentas que o comando dirige: `dbt`, `dlt`, `spark`… |

A regra prática: **declare quando a inferência erra.** Um `run:` que aponta para
um binário compilado não tem como dizer que é Go; um wrapper que chama `dbt`
por dentro não tem como dizer que dirige dbt.

## Por que um vocabulário fechado

Um id fora do vocabulário é **recusado no publish**, nomeando o que é válido.
A alternativa seria um selo que renderiza em branco numa tela três dias depois,
sem nada para rastrear.

```
workflow "daily": step "transform": `runtime: pyton` is not valid
  (valid: go, python, node, shell, …)
```

## O que o selo serve para

Não é decoração. Quando um run falha às 3h da manhã, a primeira pergunta é "o
que esse passo executa?", e a resposta costuma estar em outro repositório. O
selo põe a resposta no próprio grafo — junto com a versão do SDK que o passo
usou, que é o que distingue "quebrou hoje" de "quebrou desde a atualização de
terça".

## Próximos passos

- [Workflows](/docs/workflows/index.md) — todos os campos do passo
- [Pod por passo](/docs/pod-per-step/index.md) — onde o passo realmente executa
- [Observabilidade](/docs/observability/index.md) — as métricas que acompanham
