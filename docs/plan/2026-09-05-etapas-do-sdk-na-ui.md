# As etapas do SDK na tela: Extract, Transform e Load ao vivo

Data: 2026-09-05
Estado: executado (ver a seção final)
Alvo: MVP (2 a 3 semanas)

## 1. O pedido

> Ao configurar o SDK, o cliente ativando Extract, Transform e Load deve ver na
> UI, no React Flow, os 3 blocos, e cada um deles deve apresentar em qual etapa
> está rodando. A ferramenta precisa casar 100% com o nosso SDK.

Hoje um passo do SDK é **uma caixa cinza que vira verde**. Entre "começou" e
"acabou" há 40 minutos em que a tela não distingue "baixando a página 300 de
4.803" de "travado no handshake do Redshift".

## 2. O que já existe — e é mais do que parece

Este é o achado que define o custo do plano: **o transporte já está pronto e já
está correndo**.

| Peça | Onde | Estado |
|---|---|---|
| Log do pod ao vivo, enquanto o container roda | `kubernetes/executor.go:216` `seguirLogs` → `Logs(ctx, nome, true)` | ✅ |
| O runner vê **toda** linha, uma a uma | `application/execution/runner.go:370` | ✅ |
| A UI busca estado a cada 2s e para sozinha no fim | `web/assets/dag.js:235` | ✅ |
| Estado por nó, por tentativa | `task_runs (run_id, node_id, attempt)` | ✅ |
| O layout sai do servidor, não do navegador | `internal/api/graph.go` | ✅ |
| O pipeline já tem as três fases, nomeadas, em sequência | `sdk/pipeline.go` `runPipeline` | ✅ |

Não é preciso callback HTTP, porta nova, autenticação nova, nem RBAC novo. O
cano existe: **é o stdout**. O que falta é o SDK falar nele de um jeito que o
motor reconheça.

E como o reconhecimento fica no runner (que é agnóstico de executor), o executor
**local** ganha a mesma coisa de graça — ele também emite `EventLog`.

## 3. O protocolo

Uma linha por transição, em stdout, com prefixo que não colide com saída humana:

```
@brevis:stage {"etapa":"extract","estado":"running","em":"2026-09-05T14:02:11Z"}
@brevis:stage {"etapa":"extract","estado":"done","em":"...","linhas":48213,"paginas":300}
```

Decisões:

- **Uma linha, não um bloco.** O `copiar` do executor já quebra por linha e tem
  buffer de 1 MB. Um evento que não cabe numa linha não chega.
- **O runner consome a linha reconhecida** — ela não entra na janela de log. O
  usuário vê etapas na tela, não JSON no console.
- **Etapas conhecidas, não texto livre:** `check`, `extract`, `transform`,
  `load`. Uma etapa desconhecida é ignorada, não inventa bloco na tela.
- **Teto de 50 transições por tentativa.** O stream de log vira escrita em banco;
  sem teto, um pipeline em laço derruba o Postgres pelo caminho do log.

O SDK emite isso de dentro do `runPipeline`, que já tem exatamente essas fases.
Fora do motor (rodando na mão) não emite: `RunContext.FromEngine()` já sabe.

## 4. Onde o estado mora

Coluna `stages JSONB NOT NULL DEFAULT '[]'` em `task_runs`.

Já é chaveada por `(run_id, node_id, attempt)`, então as etapas ficam por
tentativa **sem tabela nova, sem FK nova, sem join novo**. O `DISTINCT ON` que a
UI já usa continua devolvendo a tentativa mais recente — com as etapas dela
junto.

Uma tabela `task_stages` seria mais pura e não compra nada aqui: são no máximo
quatro registros de ~60 bytes por tentativa, sempre lidos junto com a linha pai,
nunca consultados sozinhos.

## 5. As invariantes da tela

**I1 — Etapa é informativo, nunca autoridade.**
A verdade sobre um passo é o `exit_code`. Log pode se perder (pod despejado,
rotação, buffer). Um nó **sem** etapas renderiza exatamente como hoje. O que não
pode acontecer nunca é etapa faltando fazer um nó com sucesso parecer travado.

**I2 — Etapa em aberto quando o nó terminou é etapa cancelada.**
Se o passo falhou com o `extract` em `running`, a tela mostra o extract
**interrompido**, não "rodando para sempre". A regra é do lado que sabe: se o
`task_run` é terminal, toda etapa `running` vira `aborted`.

**I3 — SDK velho contra motor novo: funciona.** Nenhuma etapa chega, a tela é a
de hoje.
**Motor velho contra SDK novo: funciona.** As linhas `@brevis:stage` aparecem no
log como texto. Feio, inofensivo, e some quando o motor sobe.

## 6. O desenho na tela — e a armadilha do layout

O `graph.go` protege uma invariante explícita:

> nós na mesma coluna são os que rodam juntos de fato, e não um palpite de um
> algoritmo de layout no navegador

**Três nós irmãos para Extract/Transform/Load quebrariam isso**: eles são
*sequenciais dentro de um passo*, e desenhá-los na mesma coluna diria que rodam
em paralelo. Seria uma mentira na tela — pior que não mostrar.

Duas saídas honestas:

### 6.A — Faixa de três blocos dentro do card (recomendada para o MVP)

O card do passo ganha, embaixo do rótulo, três blocos em sequência com o nome e
o estado de cada um:

```
┌─────────────────────────────┐
│ ingest_users                │
│ ./fetch-users               │
│ ┌──────┐┌──────┐┌──────┐    │
│ │EXTR ●││TRANS ││LOAD  │    │  ● = correndo (pulsa)
│ └──────┘└──────┘└──────┘    │
│ 300 páginas · 48.213 linhas │
└─────────────────────────────┘
```

- São literalmente três blocos, cada um dizendo em que etapa está.
- **Zero mudança no layout do servidor.** As constantes `alturaNo = 110` e
  `larguraNivel = 260` continuam valendo (o card cresce ~26px; ajusta-se a
  constante uma vez).
- Legível com 20 nós SDK na tela.

### 6.B — Nós aninhados de verdade (React Flow `parentNode`)

O passo vira um nó-grupo e as três etapas viram filhos com `extent: 'parent'`.
As arestas do DAG continuam entre passos; a progressão fica dentro da caixa.

- Preserva a invariante (o grupo ocupa uma coluna só).
- **Mas quebra a matemática do layout**: `desloc = -(len(ids)-1) * alturaNo / 2`
  assume altura fixa. Um grupo é ~3x mais alto e passa por cima dos vizinhos. É
  preciso altura por nó no servidor.
- Com 20 passos SDK viram 60 caixas na tela.

**Decidido: 6.B, os nós aninhados.** (2026-09-05)

Eu tinha recomendado 6.A. A escolha foi 6.B, e ela é sustentável com três
ajustes que tiram dela justamente o que a fazia cara:

**As etapas empilham na VERTICAL, como linhas dentro do grupo — não como três
caixas lado a lado.** Três caixas horizontais dentro de um card de 190px viram
três selos ilegíveis, e alargar o grupo obrigaria a largura do nível a virar
dinâmica também. Em linha, cada etapa cabe com nome, estado, duração e o número
que ela produziu, que é o que serve às três da manhã. Um grupo com quatro linhas
tem ~190px de altura: vinte passos SDK cabem na tela, e o problema das "60
caixas" desaparece.

E não vira mentira: as três linhas estão visivelmente DENTRO de um contêiner, e
o que diz "roda em paralelo" na tela é ocupar a mesma coluna do grafo — o grupo
ocupa uma coluna só.

**A altura passa a ser por nó, no servidor.** Era o custo real do 6.B, e ele é
inevitável: `desloc = -(len(ids)-1) * alturaNo / 2` assume altura fixa, e um
grupo expandido passaria por cima do vizinho. Vira soma das alturas da coluna,
centrada.

**Um clique no cabeçalho recolhe.** É a válvula de escape para um DAG grande, e
o estado é do cliente — não vai para o servidor nem para o banco.


## 7. O que cada etapa mostra

Não basta o estado; o número é o que faz a tela valer às três da manhã.

| Etapa | Enquanto corre | Ao terminar |
|---|---|---|
| `check` | "conferindo destino" | esquema conferido |
| `extract` | páginas e linhas, subindo | linhas, páginas, tentativas HTTP |
| `transform` | — (é por registro, não tem progresso próprio) | linhas que saíram |
| `load` | linhas gravadas / leva atual | linhas, estratégia, bytes, **objetos** |

O `extract` reemite a cada N páginas (ou a cada 5s, o que vier antes), com teto
de 50 do §3. Os contadores já existem em `core.Stats` — nada a inventar.

## 8. Fases, dentro da janela do MVP

| # | Entrega | Onde | Prova |
|---|---|---|---|
| 1 | Emissão das etapas | `sdk/pipeline.go`, `sdk/stage.go` | pipeline falso emite as 4 na ordem; fora do motor não emite nada |
| 2 | Reconhecimento + consumo da linha | `runner.go` | linha reconhecida vira etapa **e some** da janela de log; linha parecida-mas-não-igual continua log |
| 3 | Persistência | migration + `taskruns.go` | etapas por tentativa; teto de 50 respeitado; I2 (running → aborted) |
| 4 | API | `graph.go` | nó com etapas devolve `stages`; nó sem etapas devolve o JSON de hoje, byte a byte |
| 5 | Tela | `dag.js`, `app.src.css` | a faixa, com os três estados e o pulso |
| 6 | Ponta a ponta | executor local | `go run` de um pipeline de exemplo pinta as três etapas |

**Semana 1:** fases 1–3 (SDK e motor; sem tela, mas já dá para conferir no
banco).
**Semana 2:** fases 4–5 (a tela).
**Semana 3:** fase 6, folga, e a costura com o checkpoint (§9).

A ordem é deliberada: cada fase é útil sozinha, e se a semana 3 sumir, o que
existe já funciona.

## 9. A costura com o plano do checkpoint

`2026-09-05-sdk-retomada-por-checkpoint.md` produz exatamente o evento que esta
tela precisa mostrar:

```
tentativa 2:  [EXTRACT ⟲ reaproveitado]  [TRANSFORM ●]  [LOAD]
```

Uma quinta etapa não; o `extract` ganha o estado `reused`. É a demonstração mais
convincente que o produto tem: a tela mostrando, sozinha, que a segunda
tentativa não gastou a quota do fornecedor.

Os dois planos são independentes e podem ser atacados em paralelo. Se apenas um
couber, **este** é o que aparece na demo.

## 10. O que não entra

- **WebSocket / SSE.** O polling de 2s já resolve, já para sozinho no terminal,
  e já está testado. Trocar transporte para ganhar 2s de latência numa tarefa de
  40 minutos é gastar semana de MVP no lugar errado.
- **Etapas para passos que não são SDK** (dbt, script solto). Sem emissor, sem
  etapa. Um dia o dbt pode emitir o mesmo marcador — o protocolo não é do SDK,
  é do stdout.
- **Etapa definida no YAML.** A tela deve refletir o que o código *faz*, não o
  que o YAML *declarou*. Declaração e execução divergem, e quando divergem é
  justamente a tela que precisa contar a verdade.

---

## 11. O selo de "construído com o SDK" (2026-09-05)

Pedido junto com a decisão do §6: o nó precisa dizer que foi construído com o
SDK.

### O que o selo diz

`SDK v0.44.1` — o nome e a **versão**, não só o nome.

A versão é o que transforma um adorno em informação: com ela a tela responde
"por que este passo se comporta diferente do vizinho" sem ninguém abrir o
Dockerfile. Um selo que dissesse apenas "SDK" seria verdadeiro e inútil.

### Como ele é sabido

**Observado, não declarado.** O SDK se anuncia numa linha, ao arrancar:

```
@brevis:{"tipo":"sdk","versao":"v0.44.1","pipeline":"fetcher"}
```

A versão sai do `runtime/debug.ReadBuildInfo()` — o próprio binário sabe com que
versão do módulo foi compilado. Ninguém digita, ninguém mantém em sincronia, e
não há como o selo mentir: se ele aparece, o SDK rodou; a versão que ele mostra é
a que está no binário.

O mesmo motivo do checkpoint: um campo que alguém tivesse de manter em sincronia
é um campo que um dia fica errado — e um selo errado é pior que selo nenhum,
porque ele é justamente o que se olha para descartar hipóteses.

### O que ele NÃO faz

**Não aparece num nó que ainda não rodou.** Antes de a primeira linha chegar,
não se sabe — e inventar a partir do YAML seria declarar em vez de observar,
que é o que o §10 recusa.

O caminho para cobrir isso existe e fica anotado: o motor já guarda as
tentativas por nó, então um nó que se anunciou como SDK em alguma execução
passada poderia mostrar o selo em cinza ("da última vez, era"). Precisa de uma
consulta a mais e não muda nada do que aparece durante a execução, que é o que
o MVP precisa.

---

## 12. O que a execução mudou (2026-09-05)

Executado, fases 1 a 6. As doze reversões mordem. Três coisas saíram diferentes.

### 12.1 O `transform` não podia ter relógio

Estava no §7 como "sem progresso próprio", mas a implementação ingênua ainda
lhe daria uma duração — e o primeiro teste a pegou reportando `ms: 0`. Virou
regra explícita no código (`semRelogio`): um número ausente é melhor que um
número errado, e um `transform: 40min` ao lado de `extract: 40min` manda alguém
procurar o gargalo no lugar errado.

Junto, uma armadilha maior: cronometrar as três **chamadas** diria "extract:
3ms" numa extração de quarenta minutos, porque a cadeia é preguiçosa. O extract
termina quando o **fluxo se esgota**, e há teste com origem lenta que morde
exatamente isso.

### 12.2 Uma guarda que não podia falhar

`marcarEtapas` conferia `ehDoSDK()` antes de gravar — mas ele só é chamado
depois de uma marca ter sido reconhecida. A reversão não mordeu, e estava certa:
a verificação era inalcançável. Removida, com o motivo no comentário.

### 12.3 Um teste que conferia o layout consigo mesmo

O teste de sobreposição lia a altura **declarada** pelo nó. A reversão que
quebrava o cálculo quebrava as duas juntas, e ele passava. Reescrito para medir
a altura pelo que o nó **desenha** — a última etapa dentro dele. Aí morde.

### 12.4 Sem animação na etapa que corre

A etapa em `running` ganha o mesmo anel que um card em execução, em vez de
pulsar. Duas gramáticas para "está acontecendo agora" na mesma tela é uma a
mais, e `@keyframes` exigiria rodar o build do Tailwind, que a §15 mantém fora
do caminho.
