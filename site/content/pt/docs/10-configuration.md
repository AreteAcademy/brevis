---
title: Configuração
description: Todas as variáveis de ambiente, com padrão, formato e o que acontece quando faltam.
group: Referência
order: 10
slug: configuration
---

Toda a configuração vem do **ambiente**, lida uma vez no boot. Nada consulta o
ambiente depois: um processo cuja configuração muda no meio da execução é um
processo cujo comportamento ninguém consegue reproduzir.

## Obrigatória

| variável | |
|---|---|
| `BREVIS_DATABASE_URL` | Postgres. Ausente = **erro de boot** nos cinco subcomandos que usam banco |

## Processo

| variável | padrão | |
|---|---|---|
| `BREVIS_ENV` | `production` | `local` usa log em texto e libera a interface sem senha; o resto, inclusive a variável não definida, exige credencial e loga JSON. Defina `BREVIS_ENV=local` para desenvolvimento |
| `BREVIS_HTTP_ADDR` | `:8080` | endereço de escuta |
| `BREVIS_LOG_LEVEL` | `info` | |
| `BREVIS_SHUTDOWN_TIMEOUT_SECONDS` | `15` | inteiro; valor não numérico é erro de boot |
| `BREVIS_DB_MAX_CONNS` | padrão do driver, `max(4, CPUs)` | limite do pool do Postgres de cada processo; valor não numérico ou negativo é erro de boot |
| `BREVIS_CONSOLE_STATEMENT_TIMEOUT_SECONDS` | `0` (desligado) | só o console (`brevis serve`): o Postgres aborta qualquer statement dele que rode por mais tempo, para que uma query de listagem descontrolada não segure uma conexão. `30` é o valor recomendado onde o pool aceita parâmetros de inicialização; fica desligado por padrão porque um PgBouncer em modo transaction recusa a conexão caso contrário. Enviado como parâmetro de inicialização da conexão, então um PgBouncer em modo transaction precisa de `ignore_startup_parameters = statement_timeout` |
| `BREVIS_RETENTION_TRIM_DAYS` | `0` (desligado) | limpeza agendada opcional, executada por `brevis scheduler`: esvazia o log, as fases e a saída dos runs mais antigos que este valor. Mesmo efeito de `brevis prune --trim-after`. `0` = o scheduler nunca limpa |
| `BREVIS_RETENTION_PURGE_DAYS` | `0` (nunca) | junto com o anterior, também apaga os runs mais antigos que este valor. Deve ser no mínimo o valor de trim |
| `BREVIS_RETENTION_INTERVAL_HOURS` | `24` | com que frequência a limpeza agendada roda (uma vez na subida, depois a cada N horas) |
| `BREVIS_BRAND_FILE` | `brand.yaml` | identidade visual; ausente = padrão |
| `BREVIS_UI_URL` | — | base do link da execução no alerta |
| `BREVIS_METRICS_ADDR` | — | porta das métricas Prometheus; vazio = não expõe. Ver [Observabilidade](/docs/observability/) |
| `BREVIS_SLACK_WEBHOOK` | — | destino do alerta de falha. Vazio = ninguém é avisado |

## Autenticação

| variável | |
|---|---|
| `BREVIS_AUTH_USER` | usuário da interface |
| `BREVIS_AUTH_PASSWORD_HASH` | hash `pbkdf2-sha256$…`, gerado por `brevis hash` |
| `BREVIS_AUTH_SECRET` | 32+ bytes para assinar a sessão (`openssl rand -base64 48`) |

As três vêm juntas ou nenhuma vem.

| variável | padrão | |
|---|---|---|
| `BREVIS_AUTH_CLIENT_IP_HEADER` | — | header que o seu proxy preenche com o endereço do cliente, p. ex. `X-Forwarded-For`. Vale a **última** entrada (a que o seu proxy acrescentou). Vazio = o endereço da conexão |

O login é limitado por endereço de cliente (IPv6 pelo seu /64): após três falhas o endereço fica bloqueado por 2 segundos, dobrando até 15 minutos. **Atrás de um ingress ou load balancer, defina a variável** (o chart do Helm faz isso quando `api.ingress.enabled` está ligado; sobrescreva com `auth.clientIPHeader`): sem ela todos os clientes compartilham o endereço do proxy, e uma pessoa chutando senhas bloquearia todo mundo. Nesse caso, fora de `BREVIS_ENV=local`, o bloqueio fica limitado a 30 segundos e um aviso é logado na subida. Defina a variável só se o header for escrito por um proxy em que você confia e o cliente não puder forjá-lo. Só uma verificação de senha roda por endereço de cada vez, então duas pessoas atrás do mesmo NAT que entrem no mesmo segundo podem ver um 429 por um instante.

:::danger Metade configurada é erro de boot
Quem preencheu o usuário acredita que fechou a porta. A interface dispara
pipeline: um `POST /workflows/<slug>/trigger` roda um `dbt build` que escreve no
warehouse. Aberta na internet, ela é um controle remoto do warehouse — e um
aviso no log não desfaz essa crença, porque ninguém lê o log de um processo que
funciona.
:::

Fora de `BREVIS_ENV=local`, subir sem credencial é recusado:

```
error: BREVIS_ENV=production requires a credential: set BREVIS_AUTH_USER,
BREVIS_AUTH_PASSWORD_HASH (generate one with `brevis hash`) and BREVIS_AUTH_SECRET
```

:::note Os nomes antigos continuam funcionando
Quatro variáveis foram renomeadas em 2026-09-10, quando os últimos nomes em
português saíram do lado do operador: `BREVIS_AUTH_USUARIO`,
`BREVIS_AUTH_SENHA_HASH`, `BREVIS_AUTH_SEGREDO` e
`BREVIS_POD_MANTER_EM_FALHA`. Uma instalação que usa o nome antigo continua
funcionando e registra um aviso nomeando o novo.

Adicione o nome novo, faça o deploy e só então remova o antigo — com os dois
definidos, o novo ganha. Os antigos saem numa versão maior.

No SDK, `BREVIS_SDK_LIMITE_INLINE` virou `BREVIS_SDK_INLINE_LIMIT` pela mesma
regra.
:::

## Ambiente das tasks

| variável | padrão | |
|---|---|---|
| `BREVIS_TASK_ENV` | vazio | o que cada passo recebe |

A task **não herda** o ambiente do orquestrador. A razão é concreta: o processo
carrega `BREVIS_DATABASE_URL` com usuário e senha do Postgres, e um workflow é
um comando arbitrário escrito por outra pessoa — herdar entregaria a credencial
do banco a todo passo de todo pipeline.

O que a task precisa é declarado:

```bash
BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE   # forwards these two from the process
BREVIS_TASK_ENV=STAGE=prod                # sets a literal
BREVIS_TASK_ENV='*'                       # forwards everything EXCEPT BREVIS_*
```

`PATH` e `HOME` entram sempre — sem `PATH` nenhum comando resolve, e o erro
seria um "not found" que não explica nada.

Um nome ausente **não** vira string vazia: `GOOGLE_PROJECT_ID=""` faria o dbt
falhar com uma mensagem pior que a de variável ausente.

## Kubernetes

Decisões da **instalação**, não do workflow.

| variável | padrão | formato |
|---|---|---|
| `BREVIS_PODS` | `auto` | `auto`, `on` ou `off`; inválido é erro de boot |
| `BREVIS_POD_NAMESPACE` | o do cluster | |
| `BREVIS_POD_SERVICE_ACCOUNT` | — | |
| `BREVIS_POD_PULL_SECRETS` | — | lista por vírgula |
| `BREVIS_POD_ENV_FROM_SECRETS` | — | lista por vírgula |
| `BREVIS_POD_ENV_FROM_CONFIGMAPS` | — | lista por vírgula |
| `BREVIS_POD_ALLOWED_SECRETS` | — | lista por vírgula |
| `BREVIS_POD_CREDENTIAL_PATH` | — | caminho montado no pod |
| `BREVIS_POD_CREDENTIAL_PVC` | — | PVC com as credenciais |
| `BREVIS_POD_NODE_SELECTOR` | — | `chave=valor,outra=valor` |
| `BREVIS_POD_TOLERATIONS` | — | `chave=valor:efeito,…`; só o operador `Equal` |
| `BREVIS_POD_KEEP_ON_FAILURE` | `false` | `true` mantém o pod para inspeção |

Em listas, vazios são ignorados: `a,,b` é erro de digitação, e um nome de secret
vazio faria o servidor recusar o pod inteiro.

## Testes

| variável | |
|---|---|
| `BREVIS_TEST_DATABASE_URL` | banco dos testes de integração; sem ela, eles pulam |

## Endpoints de saúde

| | |
|---|---|
| `GET /health` | liveness — **não** consulta o banco |
| `GET /ready` | readiness — consulta, e nomeia a dependência que falhou |

A separação é deliberada: liveness que depende de serviço externo faz o
Kubernetes **matar o pod** quando o banco oscila, em vez de apenas tirá-lo do
balanceador.

## Exemplo completo

```bash
# required
export BREVIS_DATABASE_URL='postgres://brevis:secret@db:5432/brevis?sslmode=require'

# production requires credentials
export BREVIS_ENV=production
export BREVIS_AUTH_USER=operator
export BREVIS_AUTH_PASSWORD_HASH="$(brevis hash < password.txt)"
export BREVIS_AUTH_SECRET="$(openssl rand -base64 48)"

# what the tasks need, and nothing more
export BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE,DBT_KEYFILE

# pods
export BREVIS_PODS=on
export BREVIS_POD_NAMESPACE=data
export BREVIS_POD_SERVICE_ACCOUNT=brevis-runner
export BREVIS_POD_ENV_FROM_SECRETS=bigquery-cred

# alerts
export BREVIS_SLACK_WEBHOOK='https://hooks.slack.com/services/...'
export BREVIS_UI_URL='https://brevis.example.com'
```
