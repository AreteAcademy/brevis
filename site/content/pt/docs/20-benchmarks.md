---
title: Benchmarks
description: Como medimos o gateway, o que os números dizem e as três testemunhas que precisam concordar.
group: Ingestão
order: 20
slug: benchmarks
---

Um comando, e ele mede o gateway inteiro:

```bash
BREVIS_BENCH_IMAGE=areteacademy/brevis-gateway:0.11.0 ./bench/run.sh
```

Ele sobe um Postgres, sobe o gateway, carrega com [k6], **espera o buffer
drenar**, raspa o `/metrics` do próprio gateway e escreve
`bench/results/REPORT.md`.

## Três testemunhas, e a terceira é a que importa

O k6 sabe o que o produtor viu. O gateway sabe o que fez. Um teste de carga que
só tem o primeiro número não distingue um gateway rápido de um que responde
`202` e perde o que aceitou.

Então o relatório põe os dois lado a lado — e conta as linhas no Postgres:

| testemunha | eventos |
|---|---:|
| o k6 foi informado `accepted` | 4.593.000 |
| o gateway contou recebidos | 4.593.000 |
| linhas no Postgres | 4.593.000 |
| registros na fila de descarte | 0 |

As duas primeiras são aritmética do próprio gateway — uma da resposta que ele
escreveu, outra do contador que ele incrementou — e um bug que perdesse eventos
poderia mantê-las coerentes **entre si**. O banco é a única que não aceitou a
nossa palavra.

**Uma execução em que as três discordam falhou**, por melhores que sejam os
percentis.

## A corrida acima

`areteacademy/brevis-gateway:0.11.0`, Darwin arm64, 11 CPUs, Postgres 17 em
container no mesmo kernel, `auto_table[columns]` com `write: append`:

| | |
|---|---:|
| eventos aceitos | **4.593.000** (102.064/s) |
| corpo enviado | 978 MiB em 45s |
| `503` (contrapressão) | 0 |
| p50 / p95 / p99 | 9,2 ms / 29,0 ms / 46,9 ms |
| lotes entregues | 766, nenhum enterrado |

## Os thresholds são o ponto

O k6 sai com código diferente de zero quando um falha, então isso é **portão** e
não gráfico:

| threshold | por quê |
|---|---|
| `p95 < 500ms` | o caminho de aceitação tem que ficar fora da latência do destino. Uma requisição esperando um `COPY` é o defeito contra o qual a esteira assíncrona existe. |
| `http_req_failed < 1%` | um `503` é aceitável; um timeout ou um 5xx não. |
| `eventos aceitos > 1000` | uma suíte de thresholds passa contra um gateway que não respondeu nada. |

São deliberadamente frouxos: isso roda na máquina que o CI deu. Pegam **uma
ordem de grandeza**, que é a cara de uma regressão, não um por cento.

**Um `503` não é falha.** É o gateway recusando um evento que não tem onde pôr,
e é seguro repetir — o `ingestion_id` é função congelada do evento, então o
mesmo corpo reenviado é o mesmo registro. Contá-lo como erro faria contrapressão
parecer bug.

## O que ele não mede

- **Não é BigQuery.** O destino é um Postgres em container. Cota de load job,
  latência de commit e concorrência de DDL de um warehouse não estão nesses
  números e não têm como estar.
- **Não é durabilidade.** `durability: memory` é a única camada, então um `202`
  significa *aceito na RAM*. Isso mede a velocidade disso, nunca o que sobrevive
  a um `SIGKILL`.
- **Não é o seu hardware.** Leia como forma e proporção.

## A nuvem local, por profile

```bash
docker compose -f docker-compose.drivers.yml --profile gcp up -d
docker compose -f docker-compose.drivers.yml --profile all up -d
```

Um arquivo com profiles, e não um por nuvem: dois arquivos são dois nomes de
projeto, e `postgres` nos dois já substituiu o banco de desenvolvimento do motor
uma vez.

### O floci cobre menos do BigQuery do que parece

Medido, não suposto:

| | |
|---|---|
| criar dataset e tabela | ✅ |
| `timePartitioning` e `clustering` preservados | ✅ — a forma do bug da `0.9.1` |
| `PATCH` de coluna | ✅ — a evolução do `auto_table` |
| `insertAll` | ✅ |
| queries | ✅, só com `/var/run/docker.sock` montado |
| **load jobs** | ❌ `Only QUERY jobs are supported by the floci BigQuery emulator` |

O SDK escreve no BigQuery **só** por load job. Então o floci exercita cada linha
do `auto_table` que transforma payload em DDL — e **nenhuma escrita do Brevis**.
Ainda é a metade que hoje não tem teste local nenhum, e vale ter. Mas uma suíte
verde contra o floci não significa que o BigQuery funciona.

[k6]: https://k6.io

