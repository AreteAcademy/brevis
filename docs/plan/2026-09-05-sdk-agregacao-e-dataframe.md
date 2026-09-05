# Um DataFrame no SDK? O que o caso de agregação pediu, e o que eu mediria antes

**Escrito em** 2026-09-05 · **Base** `sdk/v0.42.1` · **Origem** contribuição de consumidor

Terceira rodada do `zarv-data-pipeline`. Nasceu de uma pergunta concreta: um dos
fetchers portados **agrega** em vez de só mapear, e a pergunta foi se isso pede
um pandas dentro do SDK — usando o [gpandas](https://github.com/apoplexi24/gpandas)
como inspiração.

**Resposta curta: não um DataFrame. Um fold com estado limitado.** E antes disso,
uma pergunta que pode dispensar os dois.

O caminho até aqui está abaixo, com os números que o sustentam — inclusive **duas
hipóteses minhas que a medição derrubou**.

---

## 1. O caso que originou a pergunta

Um fetcher que lê estatísticas comunais francesas. Ele não busca e envelopa: ele
reduz.

```
entrada:   CSV de 40 MB gzipado, milhões de linhas
saída:     5.515 registros, um por (comuna, ano mais recente)
```

As operações, na ordem:

1. filtrar linhas (uma flag de divulgação, contagem > 0, indicador num mapa)
2. **agrupar por (comuna, ano) e somar** em três categorias
3. achar o **ano máximo** — uma redução global
4. ficar só com as linhas desse ano
5. **juntar** com um mapa de centroides vindo de outra origem
6. projetar num registro novo
7. ordenar

Escrevi isso à mão em ~120 linhas. É legível e está testado — inclusive contra
uma linha real do armazém. Mas é a primeira vez que um port precisa de algo além
de mapear registro a registro, e a pergunta é justa.

---

## 2. O que o gpandas é, medido

Não da página inicial — do código.

```go
type DataFrame struct {
    Columns     map[string]collection.Series
    ColumnOrder []string
    Index       []string  // rótulos de linha, como string
}

type Series interface {
    Len() int
    DType() reflect.Type
    At(i int) (any, error)
    IsNull(i int) bool
    NullCount() int
}
```

| | |
|---|---|
| armazenamento | colunar, `map[string]Series` |
| tipos | `reflect.Type` por coluna, mais colunas genéricas `TypeColumn[T comparable]` |
| nulos | máscara de bits por Series |
| operações | `GroupBy` + `Sum/Mean/Min/Max/Apply`, `Merge`, `Loc`/`iLoc`, `Pivot`, `Window`, `Corr`, `Describe` |
| IO | CSV, JSON, Parquet, Excel, SQL, BigQuery |
| maturidade | 48 estrelas, 6 forks, um mantenedor, criado em nov/2024, ativo |
| licença | o site diz Apache 2.0; a API do GitHub reporta **`NOASSERTION`** |

**O desenho é sério.** Colunar, tipado, com máscara de nulos e `GroupBy` que
aceita função de agregação arbitrária. Não é um brinquedo.

### A hipótese que eu tinha, e que caiu

Achei que a dependência inviabilizaria pelo tamanho. O `go.mod` do gpandas puxa
excelize, parquet-go, **go-echarts**, mssqldb, lib/pq, arrow — e até `gopter` e
`go-sqlmock`, que são de teste, como requires **diretos**.

Medi:

| binário | tamanho |
|---|---:|
| hello-world em Go | 1,5 MB |
| + `dataframe` com `GroupBy`, `Sum` e `Merge` | **6,8 MB** |
| um fetcher nosso, com o cliente do BigQuery | 29,3 MB |

**~5,3 MB.** O linker poda o resto porque excelize, parquet e echarts vivem no
pacote raiz, não em `dataframe`. Sobre um binário de 29 MB isso é 18% — não é
desqualificante, e eu estava errado.

Fica um ponto de dependência que não é sobre bytes: um mantenedor, licença
ambígua, e uma superfície enorme. Uma biblioteca que vai para todos os times
herda o risco de suporte de tudo que ela requer.

---

## 3. A pergunta que importa não é a dependência

É de arquitetura, e ela é anterior.

**O modelo de dados do SDK é um fluxo.** `Data.Records` é um
`iter.Seq2[Envelope, error]`. Os transformers são por registro. O `FlushEvery`
existe justamente para que uma leitura longa tenha memória limitada, e o
`from.Many` para ler de milhares de origens sem segurar o resultado.

**Um DataFrame é o oposto.** Ele existe para segurar o conjunto inteiro, em
colunas, e fazer operações de conjunto sobre ele.

Colocar um DataFrame no SDK não estende o modelo — **acrescenta um segundo
modelo, incompatível com o primeiro**. A partir daí toda operação precisa dizer
com qual dos dois ela fala, e o consumidor precisa saber em qual dos dois ele
está. É o tipo de dualidade que uma biblioteca paga para sempre.

E há uma contradição concreta: `FlushEvery` foi adicionado porque *"uma leitura
de milhares de origens não cabe necessariamente na memória"*. Um DataFrame
desfaz exatamente essa garantia.

---

## 4. O que a evidência do consumidor diz

Levantei os 19 fetchers em porte. Quantos **agregam**, e não apenas mapeiam?

| caso | o que faz | situação |
|---|---|---|
| estatísticas comunais | agrupa por (área, ano), soma 3 categorias, junta com centroides | **portado** — é o caso deste documento |
| crime por município | uma agregação por ano, sobre planilha | na fila |
| qualidade do ar | agrupa valores horários por dia | na fila |
| dados geoespaciais | agrupa por entidade — mas para carregar em tabelas separadas, não para reduzir | não migra |

**Dois ou três de dezenove.** E o resto do que o grep achou era nome de campo de log,
não agregação.

Isso não fecha a questão para um SDK genérico, mas fecha para o pedido: **eu não
tenho evidência suficiente para pedir um DataFrame.** Tenho para pedir bem menos.

---

## 5. O que o caso realmente pede

Olhando as sete operações do §1, só **duas** precisam de mais de um registro por
vez: o `max(ano)` e o agrupamento. As outras cinco são de fluxo.

E o agrupamento tem uma propriedade que muda tudo:

> **estado limitado sobre entrada ilimitada.**

A entrada são milhões de linhas de CSV. O estado é um acumulador por (comuna,
ano) — alguns milhares. O DataFrame guardaria os milhões; o fold guarda os
milhares.

É a diferença entre agregar e materializar, e ela é a razão pela qual bancos de
dados fazem `GROUP BY` em streaming sempre que a chave cabe na memória.

---

## 6. Proposta: um `Reduce`, não um pandas

Dimensionado à evidência, e dentro do modelo de fluxo que o SDK já tem.

```go
sdk.Run(sdk.Pipeline{
    Source:    ...,
    Transform: []sdk.Transformer{ /* filtros, por registro, como hoje */ },

    // Novo: um fold entre o Transform e o Target.
    Reduce: &sdk.Reduce{
        // A chave do grupo. Estado limitado por quantas chaves distintas houver.
        Chave: func(r map[string]any) (string, error) {
            return pycompat.Texto(r["insee"]) + "|" + pycompat.Texto(r["annee"])
        },

        // O acumulador de um grupo, criado na primeira ocorrência da chave.
        Iniciar: func() any { return &soma{} },

        // Acumula um registro. Nunca guarda o registro — só o que dele importa.
        Somar: func(acc any, r map[string]any) error {
            return acc.(*soma).adicionar(r)
        },

        // Roda UMA VEZ, quando o fluxo acaba, vendo todos os grupos. É onde
        // cabem as reduções globais (o max(ano)), a junção com um mapa
        // carregado no Discover, e a projeção final.
        Fechar: func(grupos iter.Seq2[string, any]) ([]map[string]any, error) {
            ...
        },
    },

    Target: ...,
})
```

### Por que esta forma, e não outra

**`Fechar` vê todos os grupos, não todos os registros.** É o que permite
`max(ano)` e a junção sem segurar a entrada. A garantia que o SDK dá — memória
proporcional ao estado, não à leitura — continua valendo, e o campo pode dizer
isso na documentação.

**Não é um DataFrame porque não precisa ser.** Não há `Loc`, `iLoc`, `Pivot`,
`Corr`. Se um consumidor quiser isso, ele importa uma biblioteca de dataframe no
`Fechar` — e paga por ela só quem usa.

**Compõe com o que já existe.** `from.Many` alimenta o `Reduce`; o `Reduce`
alimenta o `Target`; o `FlushEvery` continua valendo para a saída. Um modelo só.

**O que ele NÃO resolve, e deve estar escrito:** um agrupamento cuja quantidade
de chaves distintas seja da ordem da entrada. Aí o estado não é limitado, e a
resposta certa é o armazém — não um DataFrame na memória do pod.

---

## 7. A alternativa que pode vencer as duas

Antes de aceitar qualquer coisa acima, vale a pergunta que este consumidor
deveria ter feito primeiro:

**Por que agregar no fetcher?**

O pipeline que originou o caso tem um armazém e uma camada de transformação por
cima dele — uma arquitetura em três estágios, em que o primeiro recebe o cru, o
segundo tipa e o terceiro agrega. O fetcher deste caso **fura essa camada**: ele
reduz milhões de linhas para 5,5 mil *antes* de aterrissar, e o que chega ao
armazém já é o resultado.

O motivo original é legítimo — a fonte publica só o retrato mais recente, e
aterrissar tudo custaria uma tabela muito maior. Mas é uma **escolha**, não uma
necessidade, e ela troca flexibilidade por custo:

- o histórico bruto se perde: só existe o que a agregação decidiu manter;
- mudar a regra de categorias exige reprocessar a fonte, não o armazém;
- a agregação passa a ser código testado em Go, em vez de SQL versionado.

**Para um SDK genérico isso importa**: se a resposta certa para a maioria dos
casos é "aterrisse o cru e agregue no destino", então um `Reduce` no SDK atende
uma minoria — e deve ser documentado como o que é. A documentação do campo
deveria começar dizendo *quando não usá-lo*.

---

## 8. O que me faria mudar de ideia

Escrevo isto porque a evidência é de um consumidor só, e ela pode estar enviesada.

- **Se três ou mais times independentes** pedirem agregação no SDK, a amostra
  deixa de ser a minha.
- **Se aparecer um caso em que o estado NÃO é limitado** e ainda assim precisa
  ficar no fetcher — por exemplo uma junção entre duas fontes grandes que o
  destino não consegue fazer —, aí um DataFrame passa a resolver algo que o fold
  não resolve.
- **Se o `Reduce` virar um lugar onde todo mundo escreve o mesmo `Fechar`**, o
  padrão que emergir dali é que deve ser promovido — e talvez ele se pareça com
  um `GroupBy` tipado.

Nos três casos, a decisão sai de um número, não de uma preferência.

---

## 9. Se ainda assim for um DataFrame: o que copiar do gpandas

Registro para não se perder, porque parte do desenho dele é boa:

**Vale copiar**

- **Colunar com máscara de nulos.** `IsNull(i)` separado do valor evita o
  `*float64` por célula, e é o que permite uma coluna de 5 milhões de linhas
  caber sem 5 milhões de ponteiros.
- **Tipo por coluna, não por célula.** O `DType()` no lugar de `any` em cada
  posição é a diferença entre uma verificação e milhões.
- **`GroupBy` que aceita função de agregação arbitrária**
  (`aggregate(func(Series) (any, error))`), com `Sum`/`Mean`/`Min`/`Max` como
  atalhos por cima. A generalidade fica embaixo, a conveniência em cima.

**Não vale copiar**

- **`At(i) (any, error)` por célula.** Uma chamada de interface mais um `error`
  por posição, no caminho quente. O SDK acabou de descer de 30 para 18 alocações
  por registro; este é o sentido contrário.
- **`Index []string`.** Rótulo de linha como string é herança do pandas, e custa
  uma alocação por linha para algo que a maioria dos ETLs não usa.
- **IO no mesmo pacote do quadro.** É o que faz um `import` de DataFrame
  arrastar excelize e parquet. O SDK já separa driver por pacote e deve manter.
- **Bibliotecas de gráfico.** `go-echarts` num require direto de uma lib de
  dados é escopo demais.

---

## 10. Recomendação

1. **Não adotar o gpandas como dependência.** Não pelo tamanho — medi, são 5,3
   MB —, mas pela superfície, pelo mantenedor único e pela licença ambígua numa
   biblioteca que vai para todos os times.
2. **Não construir um DataFrame agora.** A evidência é de 2 ou 3 casos em 19, e
   ele acrescenta um segundo modelo de dados ao lado do fluxo.
3. **Considerar o `Reduce` do §6**, que cabe no modelo que já existe e resolve o
   caso medido com estado limitado.
4. **Documentar que agregação costuma pertencer ao destino.** Isso vale mais que
   qualquer das três anteriores, e não custa uma linha de código.

E se o item 3 for aceito, que a documentação dele comece dizendo quando **não**
usá-lo. Foi o que faltou no caso que originou tudo isto.
