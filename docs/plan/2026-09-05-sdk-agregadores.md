# Agregadores: o conjunto completo, e a linha que ele não cruza

**Escrito em** 2026-09-05 · **Base** `sdk/v0.42.1` · **Origem** contribuição de consumidor

Quarta rodada. Continua o estudo de
[`2026-09-05-sdk-agregacao-e-dataframe.md`](2026-09-05-sdk-agregacao-e-dataframe.md),
que recomendou **não** construir um DataFrame e sugeriu um fold no lugar.

Isto é o desenho desse fold, com o conjunto de agregadores fechado — e, mais
importante, com **o critério que decide o que entra e o que não entra**.

---

## 1. O critério

> **Entra o agregador que precisa de memória constante por grupo. Não entra o
> que precisa guardar as linhas.**

Não é uma preferência: é a única regra que preserva a promessa do SDK. O modelo
dele é um fluxo — `iter.Seq2`, transformers por registro, `FlushEvery` para que
uma leitura longa não cresça em memória. Um agregador que guarda linhas desfaz
isso em silêncio, e o sintoma aparece como um pod morto por OOM às 5h da manhã.

Com a regra, o custo é previsível e dizível: **memória = número de grupos ×
estado do agregador**, e a entrada não aparece nessa conta.

---

## 2. Os agregadores

Todos com estado O(1) por grupo. A coluna de estado é o que cada um guarda.

| agregador | estado por grupo | o que faz |
|---|---|---|
| `Conta()` | 1 inteiro | linhas do grupo |
| `ContaDe(campo)` | 1 inteiro | linhas em que o campo não é nulo |
| `Soma(campo)` | 1 número | soma |
| `Media(campo)` | soma + contagem | média |
| `Min(campo)` / `Max(campo)` | 1 valor | menor / maior |
| `Primeiro(campo)` / `Ultimo(campo)` | 1 valor | primeiro / último visto |
| `MinPor(valor, chave)` / `MaxPor(valor, chave)` | 1 par | o `valor` da linha em que `chave` é mínima / máxima |
| `Variancia(campo)` / `Desvio(campo)` | 3 números | Welford — estável numericamente, uma passada |
| `Algum(campo)` / `Todos(campo)` | 1 booleano | qualquer / todos verdadeiros |
| `Amplitude(campo)` | 2 valores | `Max − Min` |

`MinPor`/`MaxPor` merece destaque porque é o que normalmente falta e faz o
consumidor escrever um fold à mão: *"o nome da linha em que o ano é o maior"*.
Sem ele, a saída é guardar as linhas — exatamente o que a regra proíbe.

### E um para o que não couber

```go
sdk.Personalizado(sdk.Acumulador{
    Iniciar: func() any { return &meuEstado{} },
    Somar:   func(acc any, r map[string]any) error { ... },
    Valor:   func(acc any) (any, error) { ... },
})
```

A generalidade fica embaixo, a conveniência em cima — e os dez de cima são
implementados com ela, o que garante que a porta funciona.

---

## 3. O que fica de fora, e por quê

Isto é metade da proposta. Um SDK que aceita tudo e estoura depois é pior que um
que recusa na montagem.

| pedido | estado que exigiria | o que o SDK deve fazer |
|---|---|---|
| `Mediana`, `Quantil`, `Percentil` | todas as linhas do grupo | **recusar**, nomeando |
| `Distintos` (exato) | um conjunto por grupo | **recusar**, nomeando |
| `Moda` | um mapa de frequências | **recusar**, nomeando |
| `Coletar` / `Lista` | todas as linhas | **recusar**, nomeando |

A mensagem de recusa deve dizer as duas saídas, porque elas existem:

> `sdk.Mediana` não existe: ela precisa de todas as linhas do grupo, e este
> agregador roda em memória constante. Duas saídas: calcule no destino, com SQL,
> ou use `sdk.Personalizado` — e assuma o custo de memória explicitamente.

Vale considerar **aproximações** num segundo momento — `DistintosAprox` por
HyperLogLog é O(1) de verdade. Mas aproximação precisa dizer o erro na
assinatura, e isso é uma decisão à parte. Não peço agora.

---

## 4. A forma

```go
sdk.Run(sdk.Pipeline{
    Source: ...,

    // Os transformers seguem por registro, como hoje.
    Transform: []sdk.Transformer{ ... },

    Reduce: &sdk.Reduce{
        Por: sdk.Agrupar("regiao", "ano"),

        Agg: map[string]sdk.Agregador{
            "linhas":     sdk.Conta(),
            "total":      sdk.Soma("valor"),
            "media":      sdk.Media("valor"),
            "maior":      sdk.Max("valor"),
            "nome_final": sdk.MaxPor("nome", "ano"),
        },
    },

    Target: ...,
})
```

O `Reduce` fica **entre** o `Transform` e o `Target`: os transformers preparam a
linha, o fold reduz, o destino recebe o resultado. Um modelo só.

### `Fechar`, para a fase global

Alguns casos precisam de uma passada sobre os **grupos** depois que o fluxo
acaba — uma redução global (*"só o ano máximo interessa"*), uma junção com uma
tabela pequena carregada antes, ou uma projeção final.

```go
Fechar: func(grupos iter.Seq2[sdk.Grupo, map[string]any]) ([]map[string]any, error)
```

Ele vê **os grupos, não os registros**. É o que permite a fase global sem
desfazer a garantia: a memória continua proporcional ao número de grupos.

### Ordem de saída determinística

O `Reduce` deve emitir ordenado pela chave do grupo. A identidade da linha vem
do conteúdo, então a ordem não muda o resultado — mas um `-sample` que devolve
linhas diferentes a cada execução atrapalha quem está depurando, e ordenar um
mapa de milhares de chaves não custa nada perto do que já foi lido.

---

## 5. Como provar que a promessa vale

O teste que importa não é "a soma está certa" — é **a memória não crescer com a
entrada**. Sugestão:

```
para N em (10 mil, 100 mil, 1 milhão) registros, com 100 grupos fixos:
    medir alocações e pico de heap
    afirmar que ficam dentro de uma faixa constante
```

Se o teste passar para 10 mil e para 1 milhão com o mesmo teto, a promessa está
no código e não só na documentação. E ele pega a regressão mais provável desta
feature: alguém adicionar um agregador que guarda linhas sem perceber.

O mesmo teste, com o número de **grupos** crescendo, deve mostrar crescimento
linear — que é o custo esperado e documentado.

---

## 6. Dois pedidos vizinhos, do mesmo caso

Não são agregação, mas vieram do mesmo fetcher e cabem nesta rodada.

### 6.1 O `JSONCanonico` não tem a saída que o `Texto` tem

A `v0.40.0` fez o `pycompat.Texto` recusar um `float64` — eu pedi isso, e está
certo: num valor **decodificado** não há como saber se a origem via inteiro ou
decimal. O time foi além e criou `TextoAceitandoFloat64` para quando a origem era
mesmo decimal.

Mas quem agrega **computa** valores — uma média, um arredondamento. Aí não há
ambiguidade: é decimal por definição, nunca houve literal. E o registro computado
é serializado pelo `JSONCanonico`, que **só chama o `Texto` estrito**.

A saída existe para o escalar e não para o composto, que é justamente onde caem
os registros construídos pelo próprio consumidor.

O contorno funciona e é frágil: entregar um `json.Number` cujo literal tenha
ponto. **Sem o ponto**, o `Texto` lê `"48"` como inteiro e escreve `48`, onde a
referência escreve `48.0` — ou seja, o consumidor reimplementa metade da
formatação de decimais para alimentar a formatação de decimais.

**Peço** um `JSONCanonicoAceitandoFloat64`, ou um parâmetro de renderização no
`JSONCanonico`, simétrico ao que o `Texto` já tem.

### 6.2 CSV com delimitador e compressão

A fonte deste caso é um CSV **gzipado** com `;`. O `from.HTTP` tem
`Format: FormatCSV`, mas não tem opção de delimitador nem de descompressão —
então o consumidor cai no `Records`, recebe os bytes crus e faz os dois à mão.

Funciona, e mantém a requisição dentro do pipeline. Mas `;` é o padrão de fato em
boa parte da Europa, e `.csv.gz` é como quase todo portal de dados abertos
publica arquivo grande.

**Peço** `Delimitador rune` e descompressão por `Content-Type` /
extensão — o `from.Files` já faz gzip pela extensão, então a regra existe no SDK
e só não alcança o HTTP.

---

## 7. O que eu não peço, e por quê

**Não peço `SomaSe(pred, campo)`** nem agregadores condicionais.

Parece necessário — o caso real soma um valor em uma de três colunas conforme a
categoria da linha. Mas isso se resolve **antes** do fold: um transformer projeta
a linha nas três colunas (com zero nas que não se aplicam), e aí é `Soma` puro.

É a mesma escolha que o pandas faz, e ela vale a pena registrar: **cada condição
que entra no agregador é uma mini-linguagem de predicado dentro do SDK.** O
transformer já é o lugar de decidir por linha, e ele já existe.

Os dez agregadores da §2, com um transformer antes, cobriram o caso completo que
motivou este documento.

---

## 8. Resumo

| | |
|---|---|
| **critério** | memória constante por grupo; o resto recusa nomeando |
| **entra** | Conta, ContaDe, Soma, Media, Min, Max, Primeiro, Ultimo, MinPor, MaxPor, Variancia, Desvio, Algum, Todos, Amplitude — e `Personalizado` |
| **não entra** | Mediana, Quantil, Distintos exato, Moda, Coletar |
| **forma** | `Reduce` entre `Transform` e `Target`, com `Fechar` opcional sobre os grupos |
| **prova** | teste que fixa o teto de memória com a entrada crescendo 100× |
| **vizinhos** | `JSONCanonico` aceitando decimal computado; CSV com delimitador e gzip |
