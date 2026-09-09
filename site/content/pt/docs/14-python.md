---
title: Python
description: pip install brevis — contexto entre passos, o relógio do run e métricas do seu pipeline, sem dependência nenhuma.
group: SDK e bibliotecas
order: 14
slug: python
---

```bash
pip install brevis
```

```python
from brevis import context, metrics, run

bucket = context.get("extract.bucket")
since, until = run.window()          # a janela que este run cobre

context.set(rows=48213, watermark="2026-09-07T03:00:00Z")
metrics.set("rows_loaded", 48213)    # chega ao /metrics do engine
```

**Sem dependências, nunca.** Lê uma variável de ambiente, escreve um arquivo e
imprime uma linha no stdout. Requer Python 3.9+.

Não é um port do SDK em Go: sem drivers, sem paginação, sem ids de ingestão. Ela
existe para que um time use pandas, Polars ou dbt e ainda receba o que um
orquestrador tem para dar.

## Lendo o contexto

Uma chave é sempre qualificada pelo passo que a publicou:

```python
context.get("extract.bucket")            # o valor
context.get("extract.nope", default=0)   # ausente não é erro
context.of("extract")                    # tudo que aquele passo publicou
context.published()                      # o que ESTE passo já publicou
```

`context.get("bucket")` sem o passo é **recusado**, nomeando os passos que a
publicaram. Os passos são isolados — `extract` e `transform` podem publicar
`bucket` sem que nenhum perca o seu — e uma chave sem qualificação joga isso
fora no momento em que dois deles publicam a mesma. Procurar e escolher um é
precedência por acidente: funciona até deixar de funcionar em silêncio.

Ler um passo de que este não depende é erro, e a mensagem aponta a correção.

## Escrevendo

```python
context.set(name="Daniel")
context.set(label="Nome")
# os dois sobrevivem: {"name": "Daniel", "label": "Nome"}
```

As chamadas **mesclam**. A mesma chave duas vezes mantém a última escrita; uma
chave diferente não mexe na primeira. **Não há argumento de passo** — o único
namespace em que um processo escreve é o seu, e é isso que faz o isolamento ser
estrutural em vez de uma regra.

Nada chega ao disco antes de o processo terminar, então um passo que morre de
forma abrupta publica nada. Isso é correto: a saída de um passo que quebrou
descrevia trabalho que não terminou.

## O relógio que este run deve ler

Todo run carrega **parâmetros automáticos**: valores que o engine calcula por
conta própria. Ninguém os declara, todo run os tem, e eles respondem a pergunta
que um pipeline responderia com `datetime.now()`.

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

:::warning Por que não `datetime.now()`
`run.now()` é o **slot** num run agendado. Um fetcher que lê `datetime.now()` e
subtrai duas horas está certo num run que começa na hora e quarenta minutos
errado num que a fila atrasou — e aqueles quarenta minutos pertencem a **run
nenhum**, porque o slot seguinte também lê o seu próprio `now()`. Nada falha, e
o buraco é encontrado semanas depois.
:::

| | |
|---|---|
| `run.now()` | o relógio: o slot, ou quando o run começou |
| `run.window()` | `(início, fim)`, fim excluído — ou `None` |
| `run.auto()` | os parâmetros automáticos, abaixo |
| `run.context()` | id, tentativa, trigger, params e os automáticos |
| `run.params()` · `run.param(nome)` | os parâmetros declarados no workflow |
| `run.map_index()` · `run.map_value()` | a instância, sob `for_each:` |

`window()` vem do **cron**, não do histórico: um backfill de um slot de março
produz a janela que março teve. Pedir `[início, fim)` nunca se sobrepõe e nunca
deixa buraco, por mais atrasado que o run esteja e por mais que ele repita.

Ele devolve `None`, e não um par de datas zero, quando o workflow não tem
agenda — uma consulta desde a época seleciona tudo, e essa falha não deveria ser
silenciosa. O `or` do exemplo acima é o fallback inteiro.

### Os parâmetros automáticos

| campo | |
|---|---|
| `date` | `"2026-09-08"` — a partição, a pasta, o `WHERE` |
| `adjusted_at` | o relógio a ler em vez de `now()` |
| `scheduled_at` | o slot que o cron pediu; `None` num disparo manual |
| `started_at` | quando **esta tentativa** começou; um retry move |
| `delay_seconds` | o quanto esta tentativa atrasou; nunca negativo |
| `interval_start` · `interval_end` | a janela, fim excluído |
| `previous_error` | o run anterior não teve sucesso |
| `previous_success_at` | o slot do último que **teve** — é o que torna `previous_error` acionável |

Rodando o script à mão, `run.now()` **é** o relógio de parede e `window()` é
`None`, então não há ramo a escrever para desenvolvimento local.

## Uma instância de um passo mapeado

Sob [`for_each:`](/docs/workflows/), o engine roda um processo por elemento e
diz a cada um qual recebeu:

```python
partition = run.map_value()
if partition is None:
    raise SystemExit("este passo é para rodar sob `for_each:`")

load(f"/data/{partition}.csv")
```

Uma string JSON chega **sem as aspas** — `for_each` sobre `["2026-01"]` entrega
`2026-01`, não `"2026-01"`. Qualquer outra coisa — objeto, número, lista — chega
como o JSON dela, então `json.loads(run.map_value())`.

As duas são `None` num passo não mapeado. `None`, e não `-1` ou `""`: um
sentinela é um número sobre o qual alguém eventualmente faz aritmética, e uma
string vazia é indistinguível de um elemento que *é* a string vazia.

Um passo mapeado **não publica contexto** adiante — quatro instâncias não podem
compartilhar uma chave. Um passo que precisa entregar algo escreve um arquivo,
ou um passo depois dele conta o que chegou.

## Métricas que só o seu pipeline conhece

O engine já mede o que ele vê: duração, tentativas, profundidade da fila. O que
ele não tem como saber é quantas linhas entraram, quantas o fornecedor recusou
ou quantos bytes foram descartados. Isso o passo sabe, e a biblioteca leva.

```python
from brevis import metrics

metrics.set("rows_loaded", 48213)          # gauge: a última escrita vence
metrics.inc("vendor_rejected_total")       # counter: soma
metrics.inc("bytes_discarded_total", 4096)
```

Elas saem no `/metrics` **do engine**, na porta do scheduler, rotuladas com o
workflow e o passo:

```
brevis_step_rows_loaded{workflow="daily_sales",step="load"} 48213
brevis_step_vendor_rejected_total{workflow="daily_sales",step="load"} 5
```

| | |
|---|---|
| `metrics.set(nome, valor)` | um gauge. Depois que o passo sai, o valor fica até o próximo run escrever outro |
| `metrics.inc(nome, by=1)` | um counter. Soma entre passos e entre runs |

### Por que isto não abre uma porta

Porque **um passo não é raspável**. A `:9090` pertence ao scheduler e à API —
processos de vida longa, com endereço estável, raspados a cada quinze ou
sessenta segundos. Um passo começa no próprio pod, roda quarenta segundos e
sai: uma porta que ele abrisse seria raspada nunca, ou uma vez por sorte.

As três respostas conhecidas para isso são um Pushgateway (um componente a
operar, e séries que ficam até alguém apagar), um coletor OTLP (um componente
**e** uma dependência nesta biblioteca, que não tem nenhuma e não vai ter), ou
deixar o **orquestrador** carregá-las. O Brevis já lê o stdout de todo passo
procurando linhas `@brevis:` — é assim que as fases do SDK chegam ao grafo — e
já tem um medidor e um endpoint Prometheus.

Então a biblioteca escreve **uma linha no stdout** e o engine faz o resto. A
métrica chega rotulada com o workflow e o passo de graça, porque o engine sabe o
que estava executando; por um Pushgateway esta biblioteca teria que se rotular
sozinha, e erraria.

A escrita é `print(flush=True)`, porque o engine lê esse pipe **ao vivo**. Uma
linha em buffer chega quando o processo termina — o que, para um passo que roda
uma hora, é uma hora tarde, e para um passo que é morto, nunca.

### O que uma métrica recusa

**Um nome inválido é recusado, alto.** O Prometheus aceita letras, dígitos e
underscore, e o primeiro caractere não pode ser dígito. Um nome que ele recusa
não produz uma métrica quebrada — produz uma exposição que ele **se nega a
parsear**, e a **scrape inteira** se perde, com todas as outras métricas dentro.
Então `rows-loaded` levanta `MetricError` aqui, onde o erro foi cometido, em vez
de chegar renomeado para algo que ninguém escreveu.

| recusado | porque |
|---|---|
| `rows-loaded`, `rows loaded`, `2rows` | o Prometheus não parseia, e a scrape toda cai |
| `metrics.set("ok", True)` | `bool` é `int` em Python, e reportar `1` é um número cujo significado depende de saber disso. Reporte a contagem ou a duração, não o estado |
| `metrics.inc("x", -1)` | um counter só sobe. Todo `rate()` sobre ele assume isso, e a quebra aparece num gráfico semanas depois |

Rodando o script à mão, a linha ainda vai para o stdout, onde se lê como o que
é. Nada coleta e nada falha — o mesmo que `context.set()` num laptop.

## O que ela recusa

| | porque |
|---|---|
| mais de 4096 bytes | a plataforma **trunca** em vez de recusar, então um objeto grande chega cortado ao meio e lê adiante como "não publicou nada" |
| um valor que não é JSON | um `datetime` vira `"..."` ou desaparece, dependendo de quem serializa. O erro nomeia a chave e sugere `.isoformat()` |
| uma chave que não é string | `{1: "x"}` volta como `{"1": "x"}` e a busca não acha |

O teto é o número da plataforma, não nosso. Toda feature de "contexto entre
tarefas" falha do mesmo jeito — alguém põe **dados** onde só cabe **contexto**.
Quatro kilobytes guardam uma marca d'água, uma contagem ou uma lista de
partições. Publique uma referência e deixe as linhas onde estão.

:::danger Não é cofre de segredo
Tudo que é publicado fica visível para quem pode ver o run — está no status do
pod e no histórico. Publique um caminho, não uma URL assinada.
:::

## Rodando à mão

Fora do Brevis não há entrada e nada lê a saída. `get()` devolve o padrão,
`set()` aceita e descarta, e a biblioteca diz isso uma vez em INFO. Ela **não
levanta erro**: um script que não roda à mão não pode ser desenvolvido. A
validação continua rodando, então um valor que funciona no seu laptop funciona
em produção.

## Testando o seu passo

O contrato inteiro são duas variáveis, então não há o que mockar:

```python
def test_meu_passo(monkeypatch, tmp_path):
    monkeypatch.setenv("BREVIS_INPUT", '{"extract": {"rows": 42}}')
    monkeypatch.setenv("BREVIS_OUTPUT", str(tmp_path / "out"))
    assert context.get("extract.rows") == 42
```

## Versões

A biblioteca versiona sozinha, separada do engine e do SDK em Go.

:::warning Não use a `0.2.0`
Ela saiu só como wheel no PyPI. A `0.2.1` é o mesmo código, completa.
:::

`brevis.run` precisa de um engine na **`v0.9.0` ou mais nova** para ter o que
ler; numa mais antiga todo campo vem vazio e `run.now()` é o relógio de parede —
o mesmo que ela faz fora do engine.

## Próximos passos

- [Bibliotecas cliente](/docs/libraries/) — o contrato e as outras linguagens
- [Contexto entre passos](/docs/context/) — o conceito
- [Parâmetros](/docs/parameters/) — os declarados
