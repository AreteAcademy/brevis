---
title: Workflows
description: O formato YAML completo — passos, dependências, imagens, recursos e agenda.
group: Conceitos
order: 4
slug: workflows
---

Um workflow é um arquivo YAML. Ele declara **o que executar**, **em que ordem**
e **com que runtime** — e nada além disso.

## Estrutura mínima

```yaml
name: hello
steps:
  - id: unico
    run: echo pronto
```

`name` é o identificador do workflow no banco e na interface. `steps` é a lista
de passos, cada um com um `id` único e um `run`.

## Ordem: chain ou dag

```yaml
type: chain   # a ordem é a do arquivo
type: dag     # a ordem vem de depends_on
```

`chain` é açúcar: o parser converte a sequência em arestas, e **o motor conhece
apenas DAG**. Use `dag` quando a ordem não for linear:

```yaml
type: dag

steps:
  - id: preparar
    run: ./preparar.sh

  # Irmãos: dependem do mesmo passo, então rodam em paralelo.
  - id: extrair
    run: ./extrair.sh
    depends_on: [preparar]

  - id: validar
    run: ./validar.sh
    depends_on: [preparar]

  - id: publicar
    run: ./publicar.sh
    depends_on: [extrair, validar]
```

O runner percorre o grafo **por níveis**: tudo dentro de um nível roda em
paralelo, e o nível seguinte só começa quando o anterior fecha inteiro.

:::warning Falha para o nível inteiro
O runner para na **primeira** falha do nível, sem iniciar o próximo. Continuar
depois de um erro produziria resultado parcial que parece completo.
:::

## Agenda

```yaml
schedule: "0 5 * * *"   # cron de cinco campos
concurrency: 1
```

Sem `schedule`, o workflow é manual: só roda por disparo na interface, por
`brevis run` ou por `backfill`.

`concurrency: 1` limita execuções simultâneas do mesmo workflow — é o que
impede um `*/15` de se sobrepor a si mesmo quando uma execução passa dos quinze
minutos.

## Imagem e recursos

```yaml
image: us-central1-docker.pkg.dev/exemplo/apps/dbt:1.10.3
resources:
  cpu: 200m
  memory: 1Gi
  limits: {memory: 2Gi}
```

Declarados no topo, valem como **padrão de todos os passos**. Cada passo pode
sobrescrever os seus:

```yaml
steps:
  - id: bronze
    run: dbt build --select bronze+          # herda a imagem do topo

  - id: notificar
    image: ghcr.io/exemplo/notify:0.3        # outro runtime
    shell: false                             # distroless não tem shell
    run: /notify --canal dados
    resources: {cpu: 25m, memory: 32Mi, limits: {memory: 64Mi}}
    depends_on: [bronze]
```

É isto que faz um fetcher em Go custar 12 MB e 32Mi ao lado de um `dbt build` de
1,9 GB, em vez de os dois pagarem o tamanho do maior. Ver
[Pod por passo](/docs/pod-per-step/).

## Campos do passo

| campo | | |
|---|---|---|
| `id` | **obrigatório** | único no workflow; é o nome que aparece no grafo e nos logs |
| `run` | | o comando |
| `image` | | a imagem do passo; sem ela, herda a do topo |
| `shell` | `true` | `false` executa sem shell — necessário em distroless |
| `depends_on` | | lista de `id` que precisam terminar antes |
| `resources` | | `cpu`, `memory` e `limits` daquele passo |
| `when` | `all_success` | sob que estado das dependências este passo roda — veja abaixo |
| `on_error` | | anuncia as falhas deste passo — veja abaixo |

## Rodando um passo só quando algo falhou

Por padrão um passo roda quando tudo antes dele deu certo. O `when:` muda isso.

```yaml
steps:
  - id: extract
    run: python fetch.py

  - id: notify_failure
    run: ./notify.sh
    depends_on: [extract]
    when: any_failed          # roda exatamente quando o extract não conseguiu

  - id: cleanup
    run: ./cleanup.sh
    depends_on: [extract, transform]
    when: all_done            # roda de qualquer jeito
```

| regra | |
|---|---|
| `all_success` | o padrão. Nada na run falhou **e** tudo de que este passo depende deu certo |
| `any_failed` | pelo menos um passo de que este depende falhou |
| `all_done` | tudo de que este passo depende terminou, como quer que tenha terminado |

Uma regra desconhecida é recusada no publish, dizendo o que é válido. E tem que
ser: um `when: on_failure` lido como "o padrão" rodaria no **sucesso** — o
oposto do que ele diz, descoberto na noite em que importava.

### Um passo que não roda fica `skipped`

É um estado próprio, desenhado na própria cor, e carrega o motivo: *`extract`
was failed*. Antes disso, um passo abaixo de uma falha não deixava registro
nenhum e a tela o mostrava pendente para sempre.

`skipped` não é falha e não é sucesso. Um passo cujo anterior quebrou não
falhou — nunca teve a chance — e dizer que ele deu certo é uma mentira que
chega ao status da própria run.

Uma regra de gatilho decide quais passos **rodam**. Ela não decide o resultado
da run: um `notify_failure` que entregou a mensagem não quer dizer que o
pipeline funcionou, e a run continua falha.

### `all_success` aqui não é o `all_success` do Airflow

No Airflow a regra é local aos anteriores de cada task, então um ramo saudável e
sem relação continua depois que um irmão falha. **No Brevis não**: assim que
qualquer coisa na run falha, o grafo para de descer.

É o que este motor sempre fez, e tem uma história atrás — seguir depois de um
erro produziu um resultado parcial que parecia completo, e um pipeline rodou 28
dias atrasado sem ninguém ver. Um workflow que quer um passo rodando de todo
jeito diz isso com `all_done`.

## Anunciando a falha de um passo

Toda falha já é anunciada: com o webhook configurado, uma run que desiste manda
uma mensagem, sem bloco repetido em arquivo nenhum. O `on_error` é para o passo
que precisa do próprio.

```yaml
steps:
  - id: fetch_observations
    run: python fetch.py
    on_error:
      type: SLACK
```

| campo | | |
|---|---|---|
| `type` | **obrigatório** | `SLACK`. Um valor desconhecido é recusado no publish, dizendo o que é válido |
| `when` | `give_up` | `attempt` anuncia toda tentativa que falha, não só a última |

Chegam duas mensagens em vez de uma, e é esse o ponto: o alerta da run diz
*`id_verification` falhou*, que é o que quem cuida do pipeline precisa; o do
passo diz *`fetch_observations` falhou*, que é o que quem cuida daquela
integração precisa.

**Não existe campo `webhook` nem `url`, e não vai existir.** O destino é uma
credencial — quem tem a URL posta no canal como se fosse a plataforma — e um
arquivo de workflow é escrito por alguém que não necessariamente pode escolher
para onde vão os alertas da empresa. O arquivo diz **se** e **como**; a
instalação diz **onde**, pelo `BREVIS_SLACK_WEBHOOK`.

**`when: give_up` é o padrão por causa de quem está de plantão.** Um passo que
falha duas vezes e passa na terceira mandaria duas mensagens no outro padrão, e
a segunda chegaria depois que o problema já tinha ido embora.

**Um passo que não falhou nunca é anunciado.** Num workflow onde um ramo quebra,
quem cuida do outro ramo não é acordado.

A entrega é trabalho do `brevis alert` — o alerta é gravado na mesma transação
da falha, então uma queda do Slack o atrasa em vez de perdê-lo.

## Tags

```yaml
tags: [analytics, dbt, diario]
```

Servem para filtrar na interface. Não afetam a execução.

## Validação

A validação não precisa de banco, então roda na CI junto com os testes:

```bash
brevis validate workflows/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencias  cron 0 2 * * *
```

Aceita arquivo ou diretório. Num diretório, casa `*.y*ml` e ordena — dois runs
produzem o mesmo log, e a diferença entre dois deploys não vira ruído.

## Publicando

```bash
brevis publish workflows/
brevis publish workflows/ --project acme --prune
```

`--prune` remove do projeto os workflows ausentes da lista publicada,
preservando o histórico. **Não é o padrão**, e a razão é prática: `publish
um-arquivo.yaml` não pode apagar os outros quarenta e oito só porque não foram
citados na linha de comando.

## Próximos passos

- [Parâmetros](/docs/parameters/) — o que muda entre dois disparos
- [Scheduler e fila](/docs/scheduler-and-queue/) — como o workflow vira execução
