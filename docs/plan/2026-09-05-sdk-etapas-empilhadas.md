# Etapas empilháveis: `Transform` e `Reduce` em qualquer ordem, quantas vezes

**Escrito em** 2026-09-05 · **Base** `sdk/v0.46.0` · **Origem** contribuição de consumidor

Quinta rodada. Nasce de usar o `Reduce` que a
[rodada 4](2026-09-05-sdk-agregadores.md) pediu e a `v0.46.0` entregou — e de
descobrir que ele **não serve para o caso que o pediu**.

O motivo é pequeno e o conserto também. Mas ao escrevê-lo ficou claro que o
conserto certo é mais geral que o problema.

---

## 1. O que aconteceu ao tentar usar

O `Reduce` roda entre o `Transform` e o `Target`. No caso real, o que entra são
linhas cruas de um CSV e o que sai é o registro agregado:

```
entra:  <area>;<ano>;<indicador>;<unidade>;<n>;…      (uma de milhões)
sai:    {area, ano, total_a, total_b, total_c, lat, lng}
```

A identidade da linha — a chave e o `ingestion_id` — é derivada **do registro que
sai**. Ela não existe antes da agregação.

Mas os transformers rodam **antes** do `Reduce`. Então eles calculariam a
identidade de uma linha de CSV, que não é a linha que aterrissa. E o `Reduce`
tentaria *agregar* as colunas de identidade, o que não quer dizer nada.

A única saída hoje é montar a identidade dentro do `Fechar`:

```go
Fechar: func(grupos) ([]map[string]any, error) {
    ...
    r["chave"]        = md5(canonico(r))
    r["ingestion_id"] = uuid5(namespace, chave)   // reimplementando o SDK
}
```

E essa última linha é o problema: o `IngestionIDWith` existe exatamente para
ninguém reescrever isso. **Um recurso que obriga a contornar outro recurso não
está pronto.**

### Por que o consumidor não sentiu isso antes

Porque ele já agregava — dentro do `Records`, que roda **antes** do `Transform`:

```
Source(Records: agrega) → Transform(identidade) → Target
```

Funciona, e é o mesmo pipeline lógico. O `Records` virou um "reduce na fonte"
porque não havia outro lugar. **O SDK já suporta a forma; ela só não é
declarável.**

---

## 2. O conserto mínimo, e por que ele não basta

O mínimo é uma segunda fase:

```go
Transform:       []sdk.Transformer{ ... },   // sobre a linha crua
Reduce:          &sdk.Reduce{ ... },
TransformDepois: []sdk.Transformer{ ... },   // sobre o registro agregado
```

Resolve o caso. Mas deixa uma pergunta sem resposta boa: **por que duas fases, e
não três?**

Um pipeline de agregar por dia e depois por mês precisa de duas reduções. Um que
re-chaveia depois de agregar precisa de transform, reduce, transform, reduce. O
número dois não vem de nada — vem de ser o menor que resolve o caso que apareceu
primeiro.

---

## 3. A proposta: uma lista ordenada de etapas

```go
sdk.Run(sdk.Pipeline{
    Source: ...,

    Etapas: []sdk.Etapa{
        sdk.Transformar(filtrar, normalizar),
        sdk.Agregar(sdk.Reduce{Por: sdk.Agrupar("area", "ano"), Agg: ...}),
        sdk.Transformar(projetar),
        sdk.Agregar(sdk.Reduce{Por: sdk.Agrupar("ano"), Agg: ...}),
        sdk.Transformar(identidade...),   // por último, e a §5 diz por quê
    },

    Target: ...,
})
```

A ordem passa a ser **explícita na lista**, em vez de implícita na ordem dos
campos da struct. É a mudança que torna o resto possível.

### Compatibilidade

`Transform` e `Reduce` continuam existindo como atalho do caso comum, e
desaçucaram para `Etapas`:

```go
Transform: t,  Reduce: r     ≡     Etapas: []Etapa{Transformar(t...), Agregar(*r)}
```

Declarar `Etapas` junto com qualquer um dos dois é **erro** — duas descrições da
mesma coisa, e a que perde perderia em silêncio. É a mesma regra que o
`from.Many` já aplica entre `Sources` e `Discover`.

---

## 4. O que isso custa, e precisa estar escrito

**Cada `Agregar` é uma barreira.** A documentação do `Reduce` já diz que ele
drena a origem antes de a primeira linha ir ao destino. Com duas reduções, são
duas drenagens: nada flui além da primeira até ela terminar.

**O pipeline deixa de ser um fluxo depois da primeira redução.** Isso não é
regressão — é inerente a agregar —, mas com etapas empilhadas fica fácil de
esquecer. O teto de memória continua sendo o da maior etapa: `grupos × estado`,
nunca `registros`.

**Depurar fica mais difícil, e é aí que entra o pedido abaixo.**

### O `Result` precisa contar por etapa

Hoje o `Result` traz o total de linhas escritas. Com quatro etapas, "5.515
linhas" não diz nada sobre onde as outras foram.

Peço algo como:

```go
Result.Etapas = []EtapaResultado{
    {Tipo: "transform", Entraram: 8_120_433, Sairam: 2_034_112},
    {Tipo: "reduce",    Entraram: 2_034_112, Sairam:     5_515, Grupos: 5_515},
    {Tipo: "transform", Entraram:     5_515, Sairam:     5_515},
}
```

Sem isso, "por que sumiram 6 milhões de linhas?" vira bisecção manual. Com isso,
é uma linha de log.

---

## 5. Uma guarda que o SDK deveria ter

**Uma redução que recebe registros já com identidade é quase sempre um erro.**

Se `ingestion_id` (ou o que a instalação usar como coluna de identidade) chega a
um `Agregar`, o que vai acontecer é agregar a identidade — e o resultado é uma
linha cuja chave não descreve nada. Não dá erro; dá dado errado.

Peço que o `Reduce` **recuse** nomeando, como o SDK já faz em outros pontos:

> `Reduce` recebeu registros que já têm `ingestion_id`. A identidade descreve a
> linha que aterrissa, e agregar depois dela produz uma chave que não
> corresponde a nada. Mova a etapa de identidade para depois da última redução.

Isso transforma a regra da §3 — *identidade por último* — de convenção em
invariante verificado.

---

## 6. O que eu não peço, e por quê

**Não peço junções entre etapas**, nem `Filtro` como tipo de etapa, nem
expressões declarativas de agregação.

Filtro já é um transformer que devolve `SkipRecord`. Junção com uma tabela
pequena já é um mapa carregado no `Discover` e lido num transformer. Cada uma
dessas como etapa nova acrescenta vocabulário sem acrescentar capacidade.

E há um risco maior, que vale escrever: **empilhar etapas facilita fazer no
fetcher o que pertence ao destino.** A [rodada 3](2026-09-05-sdk-agregacao-e-dataframe.md)
concluiu que agregação normalmente é trabalho do armazém, e que o `Reduce` atende
uma minoria. Empilhamento não muda essa conclusão — só torna mais confortável
ignorá-la.

Se este pedido for aceito, a documentação de `Etapas` deveria começar dizendo
que **duas etapas já é muito**, e que um pipeline de quatro provavelmente está
resolvendo no lugar errado.

---

## 7. Resumo

| | |
|---|---|
| **defeito** | não há transform depois do `Reduce`, e a identidade se calcula sobre o registro agregado |
| **mínimo** | um `TransformDepois` |
| **melhor** | `Etapas []Etapa` — ordem explícita, quantas fases forem precisas |
| **compatível** | `Transform` e `Reduce` viram atalho; declarar os dois junto com `Etapas` é erro |
| **custo** | cada agregação é uma barreira; o fluxo acaba na primeira |
| **precisa junto** | `Result` contando entradas e saídas por etapa |
| **guarda** | recusar uma redução que receba registros já com identidade |
| **aviso na doc** | duas etapas já é muito; quatro provavelmente é o lugar errado |
