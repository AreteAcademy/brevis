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
| `depends_on` | | lista de `id` que precisam terminar antes; uma entrada pode ser `{step, label}` |
| `marker` | `false` | um passo sem comando, para um `start` ou um `end` |
| `resources` | | `cpu`, `memory` e `limits` daquele passo |
| `when` | `all_success` | sob que estado das dependências este passo roda — veja abaixo |
| `unless_empty` | | uma chave do contexto que decide se há o que fazer — veja abaixo |
| `for_each` | | uma chave do contexto com uma lista; o passo roda uma vez por elemento — veja abaixo |
| `group` | | desenha este passo dentro de uma caixa nomeada que colapsa — veja abaixo |
| `uses` | | outro workflow cujos passos tomam o lugar deste — veja abaixo |
| `on_error` | | anuncia as falhas deste passo — veja abaixo |

## Rodando um passo só quando há o que fazer

```python
# no extract
context.set(has_rows=len(rows) > 0)
```

```yaml
  - id: transform
    run: ./transform.sh
    depends_on: [extract]
    unless_empty: extract.has_rows
```

O `unless_empty` nomeia uma **chave**, não uma expressão. O passo decide e
publica a resposta; o motor lê uma chave e pergunta se ela está vazia.

| conta como vazio | conta como presente |
|---|---|
| `false`, `0`, `""`, `null`, `[]`, `{}` | todo o resto |

`"false"` como **string** está presente. Um passo que publicou esses cinco
caracteres publicou alguma coisa, e adivinhar que ele queria dizer um booleano é
como uma regra começa a ter opiniões que o autor dela não enxerga. Publique um
booleano de verdade.

### Por que não uma expressão

O caminho tentador é `when: "{{ context.extract.rows > 0 }}"`. Uma mini
linguagem de expressão é um compromisso grande: precisa de parser, de um sistema
de tipos para dizer o que `>` significa sobre um JSON `any`, de uma história de
segurança porque a expressão vem de um YAML que outra pessoa escreveu, e de
mensagens de erro que apontam para dentro de uma string. Todo orquestrador que
tem uma tem um bug tracker cheio disso.

Aqui a decisão fica na linguagem que o autor já escreve, onde o framework de
teste dele alcança.

### Uma chave que não existe é falha, não valor vazio

Se a chave não está lá, o passo **falha**, e a mensagem a nomeia junto do que o
publicador de fato escreveu:

```
step "transform" reads `extract.has_row`, and "extract" published "has_rows" but not "has_row"
```

A alternativa — ler chave ausente como "vazio, então pula" — transforma um erro
de digitação num passo que para de rodar em silêncio e para sempre, sem nada em
lugar nenhum dizendo por quê. É o pior resultado que essa feature pode ter.

A chave é sempre qualificada pelo passo que a publica, e esse passo precisa ser
um de que este depende. Os dois são recusados no **publish**: um passo travado
numa chave que ele nunca vai ver ficaria pulado para sempre, e descobrir isso
quando um noturno para de rodar é tarde demais.

## Rodando um passo uma vez por elemento

```python
# no extract
context.set(partitions=["2026-01", "2026-02", "2026-03"])
```

```yaml
  - id: load
    run: ./load.sh "$BREVIS_MAP_VALUE"
    depends_on: [extract]
    for_each: extract.partitions
```

O passo roda uma vez por elemento, cada um com linha, retry e código de saída
próprios. O grafo mostra **um nó com `[3]`**, não três nós.

| variável | |
|---|---|
| `BREVIS_MAP_INDEX` | `0`, `1`, `2` … |
| `BREVIS_MAP_VALUE` | o elemento |

Uma string JSON chega **sem as aspas**, então `for_each` sobre `["2026-01"]`
entrega ao shell `2026-01` e não `"2026-01"`. Qualquer outra coisa — número,
objeto, lista — chega como o JSON dela.

Nenhuma das duas existe num passo não mapeado. Uma variável que está sempre lá e
sempre vazia ensina quem lê o ambiente a ignorá-la.

### A forma do DAG não muda

Um passo mapeado continua sendo um nó com um conjunto de arestas. Só varia
quantas linhas existem embaixo dele, então o layout é o que sempre foi e o `[3]`
é contado dessas linhas na leitura — nada é armazenado, então nada precisa ficar
sincronizado e uma contagem errada não sobrevive ao conserto da consulta.

Enquanto instâncias ainda rodam, o card mostra `[2/3]`.

### Uma instância que falha derruba o passo

Três partições carregando e uma não é um passo que não fez o trabalho dele, e os
passos abaixo enxergam isso. Um nó que fica verde porque a maior parte deu certo
é um selo que mente.

**Um retry refaz só as instâncias que falharam.** Dezoito partições que deram
certo não são recarregadas porque duas quebraram.

### Lista vazia é `skipped`, não sucesso

Um passo que não fez nada porque não havia o que fazer não teve sucesso em
fazê-lo. Um nó verde sobre zero instâncias é exatamente o tipo de coisa em que
alguém constrói um dashboard.

Um valor que **não é lista** falha, e uma chave que não existe também — a mesma
política do `unless_empty:`, e pelo mesmo motivo: um erro de digitação que
silenciosamente produzisse zero instâncias desligaria o passo para sempre.

### O fan-out é limitado, e o limite é herdado

A lista viaja no contexto, e o contexto tem um teto de 4096 bytes que vem do
kubelet. Um workflow não consegue pedir dez mil pods sem antes achar um jeito de
dizer isso em quatro kilobytes. É um limite que vale manter, não contornar.

### O que um passo mapeado publica

A saída dele é gravada na linha de cada instância e **não** fica visível para os
passos abaixo. Quatro instâncias publicando sob o nome de um passo são quatro
valores para uma chave, e não existe resposta para `context.String("load.bucket")`
que não seja um chute. O passo diz isso no log dele, em vez de descartar calado.

## Agrupando passos no grafo

```yaml
steps:
  - id: extract_orders
    group: sales_data_reporting
    run: ./extract.sh
  - id: load_orders
    group: sales_data_reporting
    depends_on: [extract_orders]
    run: ./load.sh
```

Os passos são desenhados dentro de uma caixa nomeada que colapsa — o TaskGroup
do Airflow, para quando um DAG cresce a ponto de deixar de ser legível.

**Só visual.** Os TaskGroups do Airflow também *prefixam* os ids dentro deles,
então `extract` vira `sales.extract`. Aqui não: prefixar mudaria todo
`depends_on`, toda chave de contexto e toda linha registrada de um workflow que
já existe, por uma feature cujo valor inteiro é um grafo grande ficar mais
legível. Namespacing pode vir depois — é uma adição estrita a isto.

Um grupo é um **rótulo**, não um contêiner. Os passos mantêm os ids globais, um
grupo pode atravessar níveis, e nada da execução muda.

Clicar no nome do grupo o colapsa: os passos somem e as setas que cruzavam a
fronteira passam a apontar para a caixa.

## Reaproveitando outro workflow

```yaml
# nightly.yaml
steps:
  - id: prepare
    run: ./prepare.sh

  - id: mlops
    uses: ml_training       # outro workflow no mesmo publish
    depends_on: [prepare]

  - id: report
    run: ./report.sh
    depends_on: [mlops]
```

No **publish**, os passos do `ml_training` tomam o lugar desse passo, prefixados
com o id dele, e o nó `uses` desaparece:

```
prepare → mlops.train → mlops.evaluate → report
```

Eles chegam como um grupo que colapsa, então o grafo mostra o que o arquivo
disse.

**Expandido no publish, não executado em runtime.** Um passo que dispara um
*run* filho e espera é o desenho que travou o Airflow, e este motor tem o mesmo
ingrediente: o teto de pods é um semáforo por processo, então um pai segurando
uma vaga enquanto espera um filho que precisa de vagas do mesmo pool trava — e
só sob carga, ou seja, em produção. Um run, um grafo, um pool.

O que se abre mão é um run filho com id e histórico próprios.

### As regras

| | |
|---|---|
| o filho precisa estar no **mesmo publish** | não basta já estar publicado |
| setas | que entram no passo viram setas para cada **raiz** do filho; as que saem, de cada **folha** |
| `image`, `env`, `secrets`, `resources` do filho | materializados em cada passo, então ele roda no que o arquivo dele disse |
| chaves de `unless_empty` / `for_each` do filho | acompanham o prefixo |
| aninhamento | achatado, em profundidade |
| um ciclo | recusado no publish, nomeando a cadeia |

**"Mesmo publish", e não "já publicado", é a regra que importa.** Uma expansão
que lesse o banco faria o `brevis validate` — que não toca banco, de propósito —
responder outra pergunta que o `brevis publish`, e o arquivo que passou na CI
seria o que quebrou no deploy.

O filho continua publicável sozinho: a expansão **copia**, não consome. Um passo
não pode ter `uses:` e também declarar `run:`, `action:` ou `marker:` — isso é um
arquivo dizendo duas coisas.

## Dizendo o que uma seta significa

Uma dependência pode carregar um rótulo, mostrado na seta do grafo.

```yaml
steps:
  - id: determine_load_type
    run: ./decide.sh

  - id: load_full
    run: ./full.sh
    depends_on:
      - {step: determine_load_type, label: dados adicionais}

  - id: load_delta
    run: ./delta.sh
    depends_on:
      - step: determine_load_type
        label: dados que mudaram
```

A forma simples — `depends_on: [extract]` — continua funcionando e continua
sendo a normal. A maioria das dependências não tem nada a dizer, e um rótulo em
toda seta é ruído.

Os rótulos ganham o lugar deles numa **bifurcação**: duas setas saindo do mesmo
passo sem nada escrito é um diagrama que exige abrir o código para ler, que é
exatamente o que um grafo existe para evitar.

## Um passo que não faz nada

```yaml
steps:
  - id: start
    marker: true

  - id: end
    marker: true
    depends_on: [load_full, load_delta, report]
```

`marker: true` é um passo sem comando. Ele não roda nada, dá certo na hora, e
aparece no grafo.

Não é enfeite. Um `end` que depende de todos os ramos transforma *terminou tudo?*
num nó só, em vez de seis setas para seguir — e ele se comporta como qualquer
outro passo, então um `end` embaixo de um ramo que falhou fica **skipped**, não
verde.

**Um passo sem `run:` e sem `marker: true` continua recusado.** Os dois casos não
podem virar um: um comando vazio é quase sempre um engano, e o `marker: true` é
como alguém diz que fez de propósito. Um marker que também declara `run:` também
é recusado — isso é um arquivo dizendo duas coisas.

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
| `all_success` | o padrão. Tudo de que este passo depende deu certo — as dependências **dele**, como no Airflow |
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

### `all_success` é local, como no Airflow

A regra pergunta sobre as dependências **do próprio passo** e nada mais. Uma
falha num ramo sem relação não para este:

```
extract_orders ──✕                 (falhou)
extract_users  ──✓── transform_users ──✓     continua
```

Um ramo **abaixo** da falha continua parando — `all_success` ser local não quer
dizer que ele sumiu.

Este motor abortava o grafo inteiro na primeira falha. Isso tinha um motivo:
seguir depois de um erro produziu um resultado parcial que parecia completo, e
um pipeline rodou 28 dias atrasado sem ninguém ver.

**O que substitui essa proteção**, e não é nada:

- a run continua **falhando**, com o mesmo erro;
- o grafo mostra o passo que falhou em vermelho e cada passo pulado na cor
  dele, dizendo qual passo o parou;
- o alerta continua saindo quando a run desiste.

Um resultado parcial não parece mais completo, porque a run diz que não está.

**O que se abre mão**, dito na lata: um ramo sem relação agora escreve os dados
dele numa run que falhou em outro lugar.

### Um retry roda de novo só o que falhou

O retry de uma run não refaz o grafo inteiro. Um passo que já deu certo numa
tentativa anterior da **mesma run** mantém o resultado e não roda de novo — como
se comporta uma DAG run limpa no Airflow.

```
tentativa 1   extract ✓    load ✕    report (pulado)
tentativa 2   extract –    load ✓    report ✓          o extract não roda de novo
```

Ele mantém a linha original: a duração, o log e o que ele publicou são os da
primeira tentativa, porque foi quando o trabalho aconteceu. **O passo abaixo
dele continua lendo o que ele publicou** — o contexto da run é guardado, não
reconstruído.

A distinção que importa: isso é sobre as tentativas anteriores **desta run**. Um
passo que deu certo na run de *ontem* roda normalmente hoje. (Essa outra
pergunta também existe, e é o que diz ao SDK se é a primeira vez de um passo —
veja `BREVIS_RUN_FIRST`.)

Um passo que ficou **skipped** não está resolvido: ele nunca rodou, e o retry
decide sobre ele de novo com os resultados da nova tentativa.

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
