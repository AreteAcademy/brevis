# Scheduler e fila

> Dois laços independentes — quem cria os runs, quem os executa, e por que estão separados.

*https://brevis.sh/docs/scheduler-and-queue/ · brevis.sh docs (pt-BR)*

---

O brevis.sh separa **criar** uma execução de **executá-la**. São dois laços que
correm juntos e não dependem um do outro.

```
scheduler                          fila
─────────                          ────
lê a agenda
materializa o slot
cria o Run  ──────────────────►  (pendente no banco)
                                   reivindica
                                   percorre o grafo
                                   executa cada passo
                                   grava estado e log
```

## Por que separados

Um único laço que agenda e executa tem uma propriedade ruim: quando a execução
trava, o agendamento para junto. As doze horas seguintes de slots simplesmente
não existem, e ninguém percebe até alguém procurar um dado que nunca chegou.

Com os dois separados:

- o **scheduler** pode cair e a fila continua drenando o que já foi criado;
- a **fila** pode cair e os slots continuam sendo materializados, para serem
  executados quando ela voltar;
- **reprocessar não depende do relógio** — `backfill` cria runs passados pelo
  mesmo caminho que o cron usaria.

## O processo

Os dois laços vivem no mesmo comando:

```bash
brevis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| flag | padrão | |
|---|---|---|
| `--interval` | `10s` | intervalo entre ciclos do scheduler |
| `--concurrency` | `5` | **runs** simultâneos |
| `--max-pods` | `5` | **passos** simultâneos no total |

`--concurrency` e `--max-pods` contam coisas diferentes, e isso é deliberado:
cinco runs com três passos paralelos cada dariam quinze pods se o único limite
fosse o de runs.

:::note `serve` não materializa agendas
O processo da API sobe o mesmo scheduler, mas **sem o laço** — ele existe ali só
para atender o disparo manual da interface, e assim a regra "o scheduler cria os
runs" continua com um dono só. Para as agendas rodarem, é preciso um
`brevis scheduler` ao lado.
:::

## O snapshot da definição

Quando o scheduler cria um run, ele grava **a definição do workflow dentro do
run**. A execução lê esse snapshot, não o YAML em disco.

A razão é concreta: entre o disparo e a execução, alguém pode ter editado o
arquivo e feito deploy. Sem o snapshot, um run criado às 5h com uma definição
seria executado às 5h02 com outra — e o log não teria como explicar a
diferença.

## Retries

Um run que falha é tentado **três vezes**, e o intervalo **dobra**: com os
padrões, as tentativas caem em **0s, 30s e 1m30s**.

```bash
brevis scheduler --max-attempts 3 --retry-backoff 30s --retry-backoff-max 1h
```

| flag | padrão | |
|---|---|---|
| `--max-attempts` | `3` | tentativas por run, contando a primeira |
| `--retry-backoff` | `30s` | o primeiro intervalo, dobrado a cada tentativa seguinte |
| `--retry-backoff-max` | `1h` | teto para esse intervalo |

O processo imprime o cronograma no boot, para que ele nunca precise ser
deduzido de dois números:

```
scheduler and dispatcher are up  max_attempts=3 retry_backoff=30s retry_at="0s, 30s, 1m30s"
```

:::tip
**Um fornecedor com rate limit quer mais tempo.** Esses pipelines falham por um
motivo acima de todos os outros: um upstream transitório — uma API que responde
200 com "você atingiu o limite de requisições", uma cota do warehouse que
oscila. Isso leva cerca de um minuto para passar, e um retry que termina antes
disso é um retry que não aconteceu. `--retry-backoff 1m` coloca as tentativas
em 0s, 1m e 3m.
:::

O retry é por **run**, e ele re-executa apenas os passos que falharam — um
`dbt_build` que falhou não re-executa o fetch acima dele que deu certo.

A tentativa é persistida, não é estado de processo. Uma reinicialização do
worker não apaga o que já se sabia sobre aquele run.

A tentativa **do run** entra no nome do pod. Sem isso, o retry recomeçaria o run
do zero, encontraria o pod da tentativa anterior com o mesmo nome e ficaria
preso em `Pending` para sempre.

## Backfill

Materializa slots passados de um workflow já publicado:

```bash
brevis backfill diario --from 2026-01-01 --to 2026-01-31
brevis backfill diario --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

```
  31 run(s) de backfill enfileirados para diario (2026-01-01 a 2026-01-31)
  rode `brevis scheduler` para executa-los
```

**Enfileira, não executa.** Quem executa é o `scheduler` — de novo, a mesma
separação. `--to` inclui o dia inteiro.

## Alertas

```bash
export BREVIS_SLACK_WEBHOOK='https://hooks.slack.com/services/...'
export BREVIS_UI_URL='https://brevis.exemplo.com'
```

Sem o webhook, o processo avisa no boot que falhas não serão comunicadas. Uma
instalação que falha em silêncio costuma ser descoberta pelo cliente, não pelo
time.

`BREVIS_UI_URL` monta o link da execução dentro do alerta — sem ela, o aviso diz
o que falhou mas obriga quem lê a procurar a run na mão.

## Próximos passos

- [Pod por passo](/docs/pod-per-step/index.md) — onde cada passo realmente executa
- [CLI](/docs/cli/index.md) — todas as flags de `scheduler` e `backfill`
