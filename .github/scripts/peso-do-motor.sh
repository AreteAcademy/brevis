#!/usr/bin/env bash
# Prova que o motor continua leve.
#
# A API e o scheduler sao o MESMO binario (`brevis serve` e `brevis scheduler`),
# e ele nao compila uma linha do SDK. Isso e o desenho, nao um acidente: quem
# opera o Brevis sobe um processo que orquestra, e os drivers de dados -- pgx do
# lado do fetcher, BigQuery, S3, MySQL -- vivem nos pods das TAREFAS, que sao
# outras imagens.
#
# O go.mod da raiz tem `replace .../sdk => ./sdk`, e hoje ele e inerte porque
# nada importa o SDK. E uma arma carregada: no dia em que alguem importar UM
# pacote do SDK para reaproveitar um tipo, a arvore de drivers vem junto e a
# imagem que hoje tem 7 MB passa a carregar o SDK do BigQuery. Sem este teste,
# ninguem perceberia -- o build continua verde, so a imagem engorda.
set -euo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

deps="$(go list -deps ./cmd/brevis)"
total="$(echo "$deps" | wc -l | tr -d ' ')"
falhou=0

# O SDK inteiro, e o mundo que ele traz.
for proibido in \
  "AreteAcademy/brevis/sdk" \
  "cloud.google.com/go/bigquery" \
  "cloud.google.com/go/storage" \
  "aws/aws-sdk-go" \
  "go-sql-driver/mysql"
do
  n="$(echo "$deps" | grep -c "$proibido" || true)"
  if [ "$n" != "0" ]; then
    echo "❌ o motor compila $n pacote(s) de $proibido"
    falhou=1
  fi
done

# O teto do total. Ele nao existe para ser exato -- existe para que crescer
# exija uma decisao consciente, em vez de acontecer.
TETO="${TETO_DE_PACOTES:-330}"
if [ "$total" -gt "$TETO" ]; then
  echo "❌ o motor compila $total pacotes, acima do teto de $TETO."
  echo "   Se o crescimento e deliberado, suba o teto neste script e diga por que."
  falhou=1
fi

if [ "$falhou" = "0" ]; then
  echo "✅ motor: $total pacotes (teto $TETO), nenhum driver de dados"
fi
exit $falhou
