# Retomada por checkpoint: não repetir o extract quando o resto falha

Data: 2026-09-05
Estado: proposta

## 1. O pedido

> Vamos supor que estamos usando extract, transform e load pelo SDK, e no
> transform falha. Ao invés de realizar o extract novamente, devemos conseguir
> usar o que foi salvo no bucket, caso o nosso retry tenha passado 3 vezes ou o
> limite setado pela config.

O ativo a proteger é a **quota do fornecedor**. Um extract que gastou 4.803
requisições e uma janela de 40 minutos não pode ser repetido porque uma coluna
do destino mudou de tipo.

## 2. O fato que derruba a premissa

Hoje não existe "o que foi salvo no bucket" no meio de um pipeline. O
`runPipeline` é **uma passada única e preguiçosa**:

```go
data, err := Extract(ctx, p.Source)   // devolve um iterador, não dados
data = Transform(data, p.Transform...) // embrulha o iterador
res, err := loadWith(ctx, data, ...)   // é AQUI que a origem é lida
```

`Extract` devolve `iter.Seq2[Envelope, error]`. Quem puxa a sequência é o Load.
Logo:

- O transform não roda "depois" do extract. Roda **entremeado** com ele: o
  registro 500 é transformado enquanto o registro 501 ainda está vindo pela rede.
- Quando o transform falha, o extract **não terminou**. Não há extração
  completa em lugar nenhum — nem em memória, nem em bucket.

Isso não é um defeito: é o que segura o consumo de memória em 4,8 mil origens.
Mas significa que a retomada **não é de graça**. Alguém tem que materializar.

## 3. Três desenhos, e qual eu recomendo

### 3.1 Dois nós no DAG — já funciona hoje, custo zero

O motor já tenta cada nó de novo separadamente. Se o extract e o load são dois
nós, um load que falha é tentado de novo **sem tocar no extract**, porque o
extract é outro nó que já teve sucesso.

```yaml
nodes:
  - id: extract_users
    run: ./fetch-users            # to.Files{Path: "gs://landing/users/${BREVIS_RUN_ID}/"}
  - id: load_users
    run: ./load-users             # from.Files{Path: "gs://landing/users/${BREVIS_RUN_ID}/*.ndjson"}
    retries: 3
edges: [{from: extract_users, to: load_users}]
```

O `BREVIS_RUN_ID` é o token compartilhado (§9 de
`2026-09-05-contexto-entre-passos.md`) e a `v0.43.0` fechou a ergonomia disso:
`Result.Objects` devolve o caminho completo do que o `to.Files` escreveu, e ele
volta direto num `from.Files`.

**Ganha de brinde:** dois nós na tela em vez de um, retry independente, e o
extract fica visível como um passo que teve sucesso.

**Custa:** dois binários (ou um binário com dois modos), mais YAML.

### 3.2 Checkpoint dentro do SDK — materializar e reler

Para quem quer **um pod só**:

```
attempt 0:  Extract → grava NDJSON no checkpoint → marca completo
                    → relê do checkpoint → Transform → Load
attempt N:  checkpoint completo? → pula o Extract, lê de lá
                                 → Transform → Load
```

**Ganha:** a garantia que o pedido descreve, de verdade — na tentativa 2 o
fornecedor não é tocado.

**Custa:** deixa de ser passada única. O extract inteiro tem que pousar antes
de a primeira linha ser carregada. Para 4,8 mil origens isso é uma escrita e
uma leitura a mais do volume inteiro, **em toda execução**, para socorrer a
execução rara que falha.

### 3.3 Tee — gravar em paralelo ao load

Escrever no checkpoint *enquanto* transmite para o Load. Mantém o streaming,
custa uma escrita e nenhuma leitura.

**Não serve para o caso pedido.** Se o transform falha no registro 500 de 1000,
o checkpoint tem 500 registros: incompleto, e um checkpoint incompleto **não
pode ser retomado** (ver §4). A tentativa 2 refaz o extract do mesmo jeito. O
tee só ajuda quando a falha vem *depois* de o extract acabar.

### Recomendação

**3.1 como resposta padrão, 3.2 como opção declarada.**

O 3.1 já existe, já está testado, e dá observabilidade melhor. O 3.2 vale
construir porque muitos fetchers são um binário só e partir em dois é atrito
real — mas ele entra **desligado**, e a documentação diz que a partição em dois
nós é preferível quando ela é possível.

## 4. As invariantes da retomada

**I1 — Um checkpoint parcial nunca é retomado.**
Retomar de um checkpoint incompleto carrega metade dos dados em silêncio, que é
o pior jeito de falhar. A completude é marcada por um objeto `_completo`
escrito **por último**, contendo a contagem de registros. Sem ele, ou com
contagem que não bate, o checkpoint é descartado e o extract refeito.

**I2 — Um checkpoint só serve à run que o escreveu.**
O caminho leva o `BREVIS_RUN_ID`, que é constante entre tentativas e único
entre runs. Não há reaproveitamento entre execuções: uma run de hoje jamais lê
o extract de ontem. Isso é o que dispensa política de validade.

**I3 — O `ingestion_id` de uma retomada é idêntico ao da primeira tentativa.**
Isto já é verdade por construção e é a garantia que o pedido chama de "carregar
o mesmo dado": o id é UUID v5 de `provider|entity|source_key|record_ts`, tudo
derivado do payload. Mesmo payload, mesmo id. **Vai virar teste**, porque é uma
garantia que alguém precisa poder conferir.

**I4 — O `ingestion_loaded_at` de uma retomada é diferente, e está certo.**
Ele diz quando a linha foi escrita, e ela está sendo escrita agora. Fica
documentado para ninguém tratar a divergência como defeito.

**I5 — Falhar ao gravar o checkpoint não derruba a execução.**
O checkpoint é uma apólice, não o produto. Se o bucket recusa a escrita, o
pipeline continua e **avisa** — mesma decisão do §13 do `SDK_DECISOES.md`
("um aviso vale mais que um armazém"). O que não pode acontecer é a execução
morrer por causa do seguro.

## 5. A forma

```go
sdk.Run(sdk.Pipeline{
    Source: /* ... */,
    Checkpoint: sdk.Checkpoint{
        At: "gs://brevis-landing/_checkpoint",  // vazio = desligado
    },
    Target: /* ... */,
})
```

Um campo, um caminho. O resto é derivado:

- caminho efetivo: `{At}/{provider}/{entity}/{run_id}/`
- desligado fora do motor (sem `BREVIS_RUN_ID` não há chave estável), e isso é
  dito no log em vez de silenciosamente ignorado
- o `Store` sai do mesmo lugar que os drivers já usam (`store/gcs`, `store/s3`)

**Sem `MaxTentativas` no SDK.** O limite de tentativas é do motor, que é quem
cria o pod de novo. Um segundo contador no SDK seria uma configuração que não
governa nada — e um campo público que não faz nada é exatamente o que este
projeto não aceita.

## 6. O que o Result passa a dizer

```go
type Result struct {
    // ...
    CheckpointReused bool   // esta execução leu do checkpoint em vez da origem
    CheckpointPath   string // onde ele está
    CheckpointError  string // por que não foi possível gravar, se não foi
}
```

E no log: `checkpoint=reaproveitado` na tentativa 2. Sem isso ninguém sabe se a
apólice funcionou — e uma economia de quota que não aparece em lugar nenhum é
indistinguível de não ter economizado.

## 7. Fases

| # | Entrega | Teste que a prova |
|---|---|---|
| 1 | `sdk.Checkpoint`, escrita + `_completo`, leitura na tentativa > 0 | tentativa 0 grava e a 1 lê sem tocar na origem: origem falsa que **falha** se chamada duas vezes |
| 2 | I1: parcial nunca retomado | escreve metade, mata, retoma → refaz o extract; e o `_completo` com contagem errada também é recusado |
| 3 | I3: mesmo `ingestion_id` | carrega duas vezes (origem, depois checkpoint) e compara os ids byte a byte |
| 4 | I5: falha de escrita avisa e segue | store que recusa toda escrita → pipeline termina com sucesso e `CheckpointError` preenchido |
| 5 | `Result` + log + README | o teste do log já existe (`pipeline_log_test.go`); ganha o caso `checkpoint=reaproveitado` |
| 6 | Documentar 3.1 como caminho preferido | exemplo de dois nós no README, com `Objects` → `from.Files` |

Cada correção é conferida **revertendo-a** para provar que o teste morde.

## 8. O que fica de fora, e por quê

- **Retomada no meio do extract** (continuar da página 37). Exige que a origem
  seja ordenada e retomável, o que a maioria não é, e um erro aqui perde dados
  em silêncio. Um extract incompleto é refeito inteiro.
- **Reaproveitamento entre runs.** Ver I2. Abriria a porta para carregar dado
  velho achando que é novo.
- **Política de expiração.** O bucket resolve isso com lifecycle rule; o SDK
  escrevendo TTL seria uma segunda fonte da verdade.
