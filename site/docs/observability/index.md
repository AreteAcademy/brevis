# Observabilidade

> Métricas Prometheus dos dois processos, o que medir e o que ainda não existe.

*https://brevis.sh/docs/observability/ · brevis.sh docs (pt-BR)*

---

O Brevis expõe métricas Prometheus numa porta própria, a partir dos **dois**
processos.

```
GET http://<pod>:9090/metrics
```

A porta vem de `BREVIS_METRICS_ADDR`. Ela é separada da porta HTTP de propósito:
métricas não passam pela autenticação da interface, e expor as duas no mesmo
lugar obrigaria a escolher entre um scrape autenticado ou um painel aberto.

## Dois processos, dois endpoints

É a parte que a maioria das instalações erra, raspando só a API.

| | serve | reporta |
|---|---|---|
| `brevis serve` | a interface e a API HTTP | profundidade da fila |
| `brevis scheduler` | nada por HTTP | profundidade da fila, latência de claim, slots, órfãos, runs, passos |

O scheduler é quem executa, então é dele que vem quase tudo. Um painel montado
só sobre a API mostra a fila crescendo e **nada** sobre o que a está drenando.

## As métricas que o passo publica

Além do que o engine mede, um passo pode publicar as suas próprias — quantas
linhas entraram, quantas o fornecedor recusou:

```python
from brevis import metrics
metrics.set("rows_loaded", 48213)
```

Elas aparecem no **mesmo** `/metrics` do scheduler, prefixadas e rotuladas pelo
engine:

```
brevis_step_rows_loaded{workflow="daily_sales",step="load"} 48213
```

O passo não abre porta: escreve uma linha no stdout e o engine registra — um pod
que vive quarenta segundos não é raspável. Ver [Python](/docs/python/index.md).

## O que vigiar

| sinal | por que |
|---|---|
| profundidade da fila subindo sem cair | o scheduler morreu, ou `--concurrency` é baixo para a carga |
| latência de claim crescendo | contenção no banco, ou fila grande demais para o intervalo |
| órfãos > 0 | runs reivindicados por um processo que morreu antes de terminar |
| slots não materializados | o laço do scheduler parou, e as agendas param com ele |

Os dois primeiros sozinhos não distinguem "carga alta" de "processo morto" — é
por isso que os slots importam. Fila subindo **com** slots sendo criados é
carga; fila subindo **sem** slots é o laço parado.

## Kubernetes

```yaml
env:
  - name: BREVIS_METRICS_ADDR
    value: ":9090"
ports:
  - name: metrics
    containerPort: 9090
```

Com o Prometheus Operator, um `PodMonitor` por papel — e é preciso mesmo ser um
por papel, porque os dois Deployments têm labels diferentes e reportam conjuntos
diferentes.

## O que falta, deliberadamente

**Traces.** Não há OpenTelemetry ainda. Um passo executa como pod próprio e o
que interessa dele — início, fim, tentativa, código de saída, log — já está no
banco e na tela. O trace útil seria o de dentro do passo, e esse pertence ao
código do passo, não ao orquestrador.

Isso vale enquanto o passo for a unidade. No dia em que um passo chamar outro
serviço e a pergunta virar "onde os 40 segundos foram", o trace passa a valer o
que custa.

## Próximos passos

- [Kubernetes](/docs/kubernetes/index.md) — o deploy dos dois processos
- [Configuração](/docs/configuration/index.md) — `BREVIS_METRICS_ADDR` e o resto
