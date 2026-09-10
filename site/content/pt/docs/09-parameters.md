---
title: Parâmetros
description: O que muda entre dois disparos do mesmo workflow, sem editar o arquivo.
group: Referência
order: 9
slug: parameters
---

Um workflow pode declarar **parâmetros de execução**: valores que mudam entre
dois disparos sem que o arquivo mude.

## Declarando

```yaml
params:
  - name: load_full
    type: boolean
    default: "false"

  - name: start_date
    type: string
    pattern: '^\d{4}-\d{2}-\d{2}$'

steps:
  - id: run
    run: dbt build --vars '{"load_full":"{{ .load_full }}"}' --select bronze+
```

| campo | | |
|---|---|---|
| `name` | **obrigatório** | o nome usado no template e no `--param` |
| `type` | | `string` ou `boolean` |
| `default` | | usado quando o disparo não informa valor |
| `pattern` | | expressão regular que o valor precisa casar |

Um parâmetro **sem `default`** é obrigatório: o disparo que não o informar
falha antes de executar qualquer passo.

## Usando no comando

O valor entra no `run` por template, entre chaves duplas e com ponto:

```yaml
run: ./carregar.sh --desde {{ .start_date }}
```

## Passando na linha de comando

```bash
brevis run wf.yaml --param load_full=true
brevis run wf.yaml --param load_full=true --param start_date=2026-01-01
```

`--param` é repetível. Entrada sem `=` é **erro**:

```
erro: --param "load_full": use chave=valor
```

Isso é deliberado. `--param load_full`, esquecendo o valor, rodaria com o padrão
— e o operador acharia que o backfill aconteceu com o parâmetro que ele quis.

## No backfill

Os valores valem para **todos** os slots do intervalo. É o caso de uso central:
"reprocessa janeiro inteiro com `load_full=true`".

```bash
brevis backfill diario --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

## Na interface

Um workflow com `params` ganha **formulário** no lugar do botão simples de
disparo. Os campos vêm da declaração, e o `pattern` é validado antes do envio.

## No ambiente do passo

Os parâmetros do run também entram no **ambiente** de cada passo, prefixados.
Isso existe para que um fetcher escrito com o [SDK](/docs/sdk/) os enxergue sem
receber nada por argumento:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    if p.Run.Params["load_full"] == "true" {
        p.Source.URL += "&full=1"
    }
    return nil
},
```

## Auto params

Todo run carrega também os **auto params**: valores que o motor calcula
sozinho. Ninguém declara, todo run tem, e eles aparecem na tela do run.

O que importa é o `adjusted_at`, e o bug que ele elimina merece ser nomeado. Um
fetcher lê `now()`, subtrai a sua janela e pede ao fornecedor as últimas duas
horas. Num run que começa na hora, está certo. Num run que a fila atrasou em
quarenta minutos, está quarenta minutos errado — e esses quarenta minutos não
pertencem a **run nenhum**, porque o próximo slot também lê o seu próprio
`now()`. Nada falha; o dado simplesmente não existe, e isso se descobre semanas
depois.

Então o motor entrega um relógio no lugar. Leia `adjusted_at` onde você leria
`now()`:

| param | | |
|---|---|---|
| `adjusted_at` | sempre | o relógio que este run deve ler: o slot quando existe, o início caso contrário |
| `date` | sempre | o `adjusted_at` como `YYYY-MM-DD`, em UTC — a partição, a pasta, o `WHERE` |
| `delay_seconds` | sempre | o atraso desta tentativa em relação ao slot; `0` num run manual |
| `previous_error` | sempre | o run anterior a este não teve sucesso |
| `scheduled_at` | agendado | o slot que o cron pediu |
| `started_at` | | quando esta tentativa começou; um retry move |
| `interval_start` | agendado | o slot **anterior** |
| `interval_end` | agendado | este slot, **excluído** |
| `previous_success_at` | | o slot do último run que teve sucesso |

**Todo timestamp aqui é UTC**, escrito em RFC 3339 com `Z`, e a tela do run
também os mostra em UTC — então a página e o `$BREVIS_AUTO_ADJUSTED_AT` nunca
divergem. O `date` é o **dia em UTC**, que para um workflow cujo slot cruza a
meia-noite UTC não é o dia local: um `0 22 * * *` em `America/Sao_Paulo` tem
`date` do dia seguinte. O `ds` do Airflow se comporta igual, pelo mesmo motivo —
o slot é um instante, e um instante só tem um dia depois de escolhido um fuso.

`interval_start` e `interval_end` vêm do **cron**, não do histórico: um backfill
de um slot de março produz a janela que março teve, não a janela que os runs
deste workflow por acaso descrevem hoje. Um pipeline que pede `[start, end)`
nunca sobrepõe e nunca deixa buraco, por mais atrasado que rode e por mais que
seja retentado.

Eles são um **snapshot**, calculado quando o run começa e gravado nele. O
`previous_error` é um fato sobre o instante em que este run começou;
recalculá-lo amanhã — depois de o run anterior ter sido retentado e passado —
responderia outra pergunta com o mesmo nome.

### Como ler

Num passo de shell, uma variável por valor, para não precisar parsear nada:

```yaml
steps:
  - id: extract
    run: |
      curl "https://api.example.com/events?since=$BREVIS_AUTO_INTERVAL_START&until=$BREVIS_AUTO_INTERVAL_END" \
        > "/data/$BREVIS_AUTO_DATE.json"
```

O conjunto inteiro também está em `$BREVIS_AUTO_PARAMS` como um único objeto
JSON, para um passo que prefira parsear uma vez só.

Em Go, o [SDK](/docs/sdk/) lê por você:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    start, end, ok := p.Run.Auto.Window()
    if !ok { // sem agendamento: cai para uma janela fixa
        start, end = p.Run.Auto.Now().Add(-24*time.Hour), p.Run.Auto.Now()
    }
    p.Source.URL += "&from=" + start.Format(time.RFC3339) +
        "&to=" + end.Format(time.RFC3339)
    return nil
},
```

`Auto.Now()` é o relógio a ler no lugar de `time.Now()`. Fora do motor — um
fetcher que alguém roda na mão — ele *é* o relógio de parede, então nada precisa
de tratamento especial no desenvolvimento local.

Em Python, o `pip install brevis` lê os mesmos valores:

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

O `window()` devolve `None` quando o workflow não tem agendamento, em vez de um
par de tempos zerados — uma consulta desde a época seleciona tudo, e essa falha
não pode ser silenciosa. O `or` acima é a queda inteira.

### No dlt, nada a ler

O [dlt](https://dlthub.com) guarda o cursor dele, num estado que grava no
destino. Esse estado só anda para **frente**, o que funciona até você reexecutar
um slot antigo: o cursor continua dizendo hoje, então o backfill lê a janela de
hoje — e a resposta do próprio dlt para isso é descartar o estado.

A janela que o Brevis entrega vem do cron, então o run de março produz a janela
de março por mais vezes que seja repetido. O dlt aceita essa janela — os nomes
são dele, lidos antes de procurar pelo Airflow, e o motor os define em todo run
agendado:

```python
@dlt.resource(primary_key="id")
def events(
    updated_at=dlt.sources.incremental(
        "updated_at",
        initial_value=datetime(1970, 1, 1, tzinfo=timezone.utc),
        allow_external_schedulers=True,   # esta linha é a integração inteira
    )
):
    yield from api.events(since=updated_at.start_value, until=updated_at.end_value)
```

O `allow_external_schedulers=True` é tudo. O `$DLT_INTERVAL_START` e o
`$DLT_INTERVAL_END` viram o `initial_value` e o `end_value` do dlt, meio-abertos
dos dois lados — a linha que cai exatamente no início entra, a que cai
exatamente no fim não, que é o que faz slots consecutivos não deixarem buraco
nem contarem duas vezes.

Um incremental com `end_value` **não toca no estado gravado**. Essa é a parte
que importa: reexecutar um slot é idempotente, e um backfill de março não mexe
no cursor de que a execução noturna depende.

Duas coisas antes de funcionar:

- **O cursor precisa ser um datetime, não uma string.** O dlt não converte um
  cursor `text` para comparar e recusa entrar no scheduler, com um
  `JoinSchedulerError` que não diz isso com todas as letras. Converta no
  resource — `add_map` — ou tipe o `initial_value` como acima.
- **Um workflow sem agendamento não entrega nada**, e aí o dlt levanta
  `ExternalSchedulerNotAvailable` em vez de voltar em silêncio para o cursor
  dele. Se o pipeline precisa funcionar dos dois jeitos, deixe o
  `allow_external_schedulers` desligado e passe a janela você mesmo, a partir do
  `run.window()`.

Declarar `tools: [dlt]` no passo é outra coisa, e só desenha o chip no grafo — a
janela é entregue de qualquer forma.

## O snapshot

Os parâmetros são gravados **no run**, não lidos do workflow na hora de
executar. O run carrega a entrada com que foi disparado, e o log de uma execução
de janeiro continua mostrando os valores de janeiro mesmo depois de o padrão
mudar.

## Próximos passos

- [Configuração](/docs/configuration/) — variáveis de ambiente do processo
- [SDK](/docs/sdk/) — como um fetcher lê o contexto do run
- [Contexto entre passos](/docs/context/) — o que um passo publica para o seguinte
