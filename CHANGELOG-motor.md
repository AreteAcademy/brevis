# Changelog — o motor

Versões do motor, publicado como imagem Docker (`daniel3843/brevis`). O SDK tem
o seu próprio, em [`CHANGELOG.md`](CHANGELOG.md): são dois artefatos com
públicos diferentes — um é um módulo Go que alguém importa, o outro é uma
imagem que alguém opera — e por isso duas listas.

A tag do motor é `vX.Y.Z`, sem prefixo; a do SDK leva `sdk/`.

---

## [0.4.0] — 2026-09-05

**A primeira imagem publicada.** Até aqui o motor só existia em código: não
havia tag `v*`, e portanto nenhuma imagem — pelo motivo do parágrafo seguinte.

### Corrigido: o build da imagem estava quebrado

O `Dockerfile` compilava com `golang:1.25` e o `go.mod` exige `go 1.27.0`. A
imagem oficial do Go fixa `GOTOOLCHAIN=local`, então ela **não** baixa a
toolchain que falta: o build morria no `go mod download` com

```
go: go.mod requires go >= 1.27.0 (running go 1.25.14; GOTOOLCHAIN=local)
```

Era isto que impedia qualquer release. Só aparece quando alguém de fato tenta
publicar, porque nenhum outro portão da CI usa o Dockerfile.

### Mudou de forma incompatível: `BRAVIS_` virou `BREVIS_`

**Toda** variável de ambiente trocou de prefixo:

```
BRAVIS_DATABASE_URL  ->  BREVIS_DATABASE_URL
BRAVIS_HTTP_ADDR     ->  BREVIS_HTTP_ADDR
BRAVIS_ENV           ->  BREVIS_ENV
BRAVIS_LOG_LEVEL     ->  BREVIS_LOG_LEVEL
BRAVIS_BRAND_FILE    ->  BREVIS_BRAND_FILE
BRAVIS_TASK_ENV      ->  BREVIS_TASK_ENV
```

Quem sobe a partir de um deploy anterior precisa renomeá-las **antes**: sem
`BREVIS_DATABASE_URL` o processo não acha o banco.

### Antes de subir: rode as migrations

A `00007` acrescenta duas colunas a `task_runs` (`etapas`, `sdk_versao`). O
código novo faz `SELECT` delas, então subir a imagem sem migrar deixa a tela de
execução com erro.

### Adicionado: as etapas do SDK na tela

Um passo do SDK era uma caixa cinza que virava verde. Entre "começou" e
"acabou" havia quarenta minutos em que a tela não distinguia "baixando a página
300 de 4.803" de "travado no handshake do Redshift".

Agora ele aparece como um grupo, com as etapas dentro — `check`, `extract`,
`transform`, `load` — cada uma com estado, duração e o número que produziu. E um
selo `SDK v0.45.0` dizendo com que versão foi construído.

O transporte é o log que o motor já acompanha ao vivo: nenhuma porta nova,
nenhuma permissão nova. Como quem reconhece a marca é o runner, o executor
**local** mostra o mesmo.

Um passo que não é do SDK continua exatamente como era.

### Adicionado: `env:` e `secrets:` por passo

Um passo pode declarar as variáveis de que precisa, e um segredo do cluster é
montado por nome:

```yaml
nodes:
  - id: fetch_occurrences
    run: ./fetch
    env:
      JANELA_DIAS: "7"
    secrets:
      - GABRIEL_SESSION_COOKIE
```

**Quem decide o que pode ser montado é a instalação**, não o YAML:
`BREVIS_POD_ALLOWED_SECRETS` lista os segredos permitidos. Sem ela, nenhum
segredo é montado — um workflow não deve conseguir alcançar um segredo só por
citá-lo.

### Adicionado: a credencial rotacionada sobrevive ao pod

Volumes por passo, para que uma credencial renovada durante a execução não morra
com o container que a renovou.
