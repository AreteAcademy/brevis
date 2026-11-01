# Quickstart

> Do zero à DAG executando na interface, em quatro passos.

*https://brevis.sh/pt/docs/quickstart/ · brevis.sh docs (pt-BR)*

---

Este guia sobe o brevis.sh, publica um workflow e o executa. Ao final você vê o
grafo com o estado de cada passo ao vivo.

Requisito: um PostgreSQL acessível e a variável `BREVIS_DATABASE_URL`.

## 1. Aplicar o schema

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
brevis migrate up
```

## 2. Escrever um workflow

Crie `hello.yaml`. Este exemplo usa apenas shell, então roda em qualquer lugar:

```yaml
name: hello
type: dag
tags: [example]

steps:
  - id: prepare
    run: sh -c 'echo preparing; sleep 1'

  # The next two depend on the same step, so they run IN PARALLEL.
  - id: extract
    run: sh -c 'sleep 2; echo 42 extracted'
    depends_on: [prepare]

  - id: validate
    run: sh -c 'sleep 1; echo validated'
    depends_on: [prepare]

  - id: publish
    run: echo published
    depends_on: [extract, validate]
```

Confira antes de publicar — isto não toca no banco:

```bash
brevis validate hello.yaml
```

A saída é a do binário, que imprime em inglês:

```text
  ok    hello                        dag  4 steps, 4 dependencies  (manual)
```

## 3. Executar agora, sem fila

O caminho mais curto para ver o grafo funcionando:

```bash
brevis run hello.yaml
```

```text
workflow hello (dag, 4 steps) in .
  ▶ prepare
    prepare | preparing
  ✓ prepare
  ▶ extract
  ▶ validate
    validate | validated
  ✓ validate
    extract | 42 extracted
  ✓ extract
  ▶ publish
    publish | published
  ✓ publish

workflow hello finished
```

`extrair` e `validar` começam juntos porque dependem do mesmo passo. O runner
percorre o grafo por níveis: tudo dentro de um nível roda em paralelo, e o
nível seguinte só começa quando o anterior fecha inteiro.

:::note `run` é local por natureza
`brevis run` só opera com `BREVIS_ENV=local` (vazio conta como local) e executa
os passos como processos na própria instância — mesmo os que declaram `image:`.
Quem cria pods é o `scheduler`.
:::

## 4. Publicar e operar

Para que o workflow exista no banco, apareça na interface e siga a agenda:

```bash
brevis publish hello.yaml
```

```text
  published  hello                      (manual)
```

Suba a interface e o scheduler em dois terminais:

```bash
brevis serve
```

```bash
brevis scheduler --interval 5s --concurrency 4
```

Abra `http://localhost:8080`, encontre `hello` na lista de workflows e clique em
▶. O grafo mostra cada passo mudando de estado ao vivo.

## O que aconteceu

| | |
|---|---|
| `migrate up` | criou as tabelas |
| `validate` | conferiu o YAML sem tocar no banco |
| `run` | executou na própria instância, sem fila |
| `publish` | gravou o workflow e a agenda |
| `serve` | subiu API e interface |
| `scheduler` | materializou a agenda e executou a fila |

## Próximos passos

- [Workflows](/pt/docs/workflows/index.md) — o formato completo do YAML
- [Parâmetros](/pt/docs/parameters/index.md) — o que muda entre dois disparos
- [Pod por passo](/pt/docs/pod-per-step/index.md) — como isso vira Kubernetes
