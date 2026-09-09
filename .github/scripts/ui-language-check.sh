#!/usr/bin/env bash
# The interface is in English.
#
# CONTRIBUTING.md has said so since the sweep, and nothing checked it -- so
# Portuguese kept arriving one string at a time. Five were found by LOOKING at
# the rendered screens, which is not a method: "Em andamento", "em curso",
# "Buscar workflow", and the column headers "Origem" and "Criado", all shipped
# and all read by whoever operates an installation.
#
# It covers web/**/*.templ only, and only USER-VISIBLE text. The rest of the
# repository keeps its deliberately Portuguese identifiers -- `criado_em`,
# `saida`, `disparar`, `Transicionar` -- each documented where it lives. A
# detector that flagged those would be turned off within a week, and a check
# that is turned off is worse than one that never existed.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

python3 - <<'PY'
import re, pathlib, sys

# Words that exist ONLY in Portuguese, or whose English spelling differs. Short
# ones that are also English words (`no`, `da`, `é`) are deliberately absent:
# this list is meant to fire on real leaks, not to be argued with.
WORDS = r"""origem criado criada atualizado atualizada duracao duração situacao situação
nenhum nenhuma nenhuns todos todas buscar salvar excluir apagar voltar proximo próximo
anterior passos etapas execucao execução execucoes falhou falha sucesso tentativa
tentativas agendado agendada andamento curso hoje ontem amanha amanhã nome acao ação
acoes ações usuario usuário senha entrar sair painel fila filas disparar disparo
resultado resultados erro erros aviso avisos carregando enviar cancelar confirmar
fechar remover filtro filtros limpar abrir editar adicionar
de"""

# `de` is on that list despite being two letters. In an English interface a bare
# "de" is Portuguese, and it shipped: the workflows screen read "de 5" for as
# long as that screen existed, because the pattern that should have caught it
# skipped any text holding an interpolation. If this ever fires on "de facto",
# that is the day to reconsider it.
PT = re.compile(r"\b(" + "|".join(WORDS.split()) + r")\b", re.I)

# Where visible text hides in a .templ file. Two of these were added AFTER a
# leak got past the check, and each is a shape the previous version could not
# see:
#
#   `@th("Origem", "")`            a component argument, not text between tags
#   `>of { fmt.Sprint(total) }<`   text MIXED with interpolation. The first
#                                  pattern excluded `{` and `}`, so a span
#                                  holding both a word and a value was skipped
#                                  entirely -- and "de 5" shipped on the
#                                  workflows screen.
#
# The interpolations are stripped before the words are checked: `fmt.Sprint` is
# Go, not prose, and matching inside it would flag identifiers forever.
PATTERNS = [
    re.compile(r">([^<>]+)<"),                                    # between tags
    re.compile(r'(?:placeholder|title|aria-label|alt)="([^"]+)"'), # attributes
    re.compile(r'@\w+\(\s*"([^"]+)"'),                             # component arguments
    re.compile(r'\{\s*"([^"]+)"\s*\}'),                            # templ literals
]

INTERPOLATION = re.compile(r"\{[^{}]*\}")

bad = []
for path in sorted(pathlib.Path("web").rglob("*.templ")):
    for n, line in enumerate(path.read_text().split("\n"), 1):
        stripped = line.strip()
        if stripped.startswith("//"):
            continue  # comments are checked by the reader, not by this
        for pat in PATTERNS:
            for raw in pat.findall(line):
                text = INTERPOLATION.sub(" ", raw)
                m = PT.search(text)
                if m and text.strip():
                    bad.append((path, n, m.group(0), raw.strip()[:60]))

if bad:
    for path, n, word, text in bad:
        print(f"::error file={path},line={n}::Portuguese in the interface: {word!r} in {text!r}")
    print(f"\n{len(bad)} string(s). The interface is English -- see CONTRIBUTING.md.")
    print("If a word here is a false positive, remove it from WORDS in this script")
    print("and say why in the commit.")
    sys.exit(1)
print("✅ the interface is in English")
PY
