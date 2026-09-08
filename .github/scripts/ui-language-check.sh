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
resultado resultados erro erros aviso avisos carregando enviar cancelar confirmar"""
PT = re.compile(r"\b(" + "|".join(WORDS.split()) + r")\b", re.I)

# Where visible text hides in a .templ file. The third pattern is the one the
# first version of this check MISSED: `@th("Origem", "")` is a component
# argument, not text between tags, and two of the five leaks were exactly that.
PATTERNS = [
    re.compile(r">([^<>{}]+)<"),                                   # between tags
    re.compile(r'(?:placeholder|title|aria-label|alt)="([^"]+)"'), # attributes
    re.compile(r'@\w+\(\s*"([^"]+)"'),                             # component arguments
    re.compile(r'\{\s*"([^"]+)"\s*\}'),                            # templ literals
]

bad = []
for path in sorted(pathlib.Path("web").rglob("*.templ")):
    for n, line in enumerate(path.read_text().split("\n"), 1):
        stripped = line.strip()
        if stripped.startswith("//"):
            continue  # comments are checked by the reader, not by this
        for pat in PATTERNS:
            for text in pat.findall(line):
                m = PT.search(text)
                if m and text.strip():
                    bad.append((path, n, m.group(0), text.strip()[:60]))

if bad:
    for path, n, word, text in bad:
        print(f"::error file={path},line={n}::Portuguese in the interface: {word!r} in {text!r}")
    print(f"\n{len(bad)} string(s). The interface is English -- see CONTRIBUTING.md.")
    print("If a word here is a false positive, remove it from WORDS in this script")
    print("and say why in the commit.")
    sys.exit(1)
print("✅ the interface is in English")
PY
