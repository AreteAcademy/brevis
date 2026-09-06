#!/usr/bin/env bash
# Confere que os artefatos gerados em web/ estao atualizados.
#
# São DOIS geradores, e o portão só rodava um. O `templ generate` estava lá; o
# Tailwind não -- apesar de o comentário do portão dizer que ele cobria o
# `make generate`. Um app.css velho passava calado, e passou: a imagem 0.4.0
# saiu carimbada `-dirty` porque o CSS commitado estava desatualizado, e o
# `make generate` sujou a árvore no meio da publicação.
#
# As duas ferramentas são PINADAS. O templ sai do go.mod, que é onde a versão
# da biblioteca já vive -- instalar @latest podia gerar código para uma versão
# de runtime diferente da que o binário usa. O Tailwind sai da variável abaixo:
# `releases/latest` fazia dois desenvolvedores gerarem CSS diferente, e um
# portão que compara contra um gerador que muda sozinho nunca poderia ser
# reprodutível.
set -euo pipefail

TAILWIND_VERSAO="${TAILWIND_VERSAO:-v4.3.3}"

raiz="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$raiz"

versao_templ="$(go list -m -f '{{.Version}}' github.com/a-h/templ)"
echo "→ templ $versao_templ, tailwind $TAILWIND_VERSAO"
go install "github.com/a-h/templ/cmd/templ@$versao_templ"

mkdir -p bin
if [ ! -x bin/tailwindcss ] || ! ./bin/tailwindcss --help 2>&1 | head -1 | grep -q "${TAILWIND_VERSAO#v}"; then
  arch="$(uname -m | sed 's/x86_64/x64/;s/aarch64/arm64/')"
  # darwin -> macos: o ativo da release nao usa o nome do kernel.
  so="$(uname -s | tr 'A-Z' 'a-z' | sed 's/darwin/macos/')"
  curl -sSLf -o bin/tailwindcss \
    "https://github.com/tailwindlabs/tailwindcss/releases/download/${TAILWIND_VERSAO}/tailwindcss-${so}-${arch}"
  chmod +x bin/tailwindcss
fi

"$(go env GOPATH)/bin/templ" generate
./bin/tailwindcss -i web/assets/app.src.css -o web/assets/app.css --minify

if ! git diff --exit-code -- web/; then
  echo "::error::os artefatos gerados estão desatualizados: rode 'make generate' e commite web/"
  exit 1
fi
echo "✅ artefatos gerados em dia (templ e CSS)"
