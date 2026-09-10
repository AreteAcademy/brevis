#!/usr/bin/env bash
# The repository is in English -- comments and the messages a person reads.
#
# CONTRIBUTING.md has said so since the sweep of 2026-09-06, and until
# 2026-09-10 the only automated check was ui-language-check.sh, which covers
# web/**/*.templ and only user-visible text. Everything else was policed by
# reading, which found 19 Portuguese strings in production code -- including
# ":rotating_light: Falha no pipeline", which went to a customer's Slack, and
# two error messages in the PUBLIC SDK that consumers read.
#
# This covers what that one does not:
#
#   1. comment lines, repository-wide
#   2. strings inside fmt.Errorf, errors.New, http.Error, panic and slog calls
#      -- the prose a person reads when something breaks
#
# WHAT IS DELIBERATELY OUT, with the reason, because a check whose exclusions
# are unexplained is a check somebody deletes:
#
#   - CHANGELOG.md and CHANGELOG-motor.md. They record decisions made on a
#     date, and rewriting a record is not translating a project. New entries
#     are in English.
#   - site/. The website is translated for users on purpose: pt-BR, English
#     and Spanish.
#   - *_test.go. 471 test messages are still Portuguese. Nobody outside the
#     project reads them, so they were left for a sweep of their own rather
#     than mixed into a release. Removing this exclusion is the way to start
#     that sweep.
#   - *_templ.go. Generated; fix the .templ and regenerate.
#   - The on-disk and wire formats listed in CONTRIBUTING.md -- the graph
#     payload keys dag.js reads, postgres.Stage's tags, the migrations'
#     columns, the checkpoint depot's file names. They are data, not prose,
#     and renaming one is a migration rather than a translation.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

python3 - <<'PY'
import re, pathlib, sys

# Portuguese function words. Short ones that are also English (`no`, `da`, `a`)
# are absent on purpose: this fires on real leaks, not on arguments.
# Function words only. The nouns are deliberately absent: `etapas`, `saida`,
# `ambiente`, `origem`, `criado_em`, `duracao_ms` and `tentativa` are
# IDENTIFIERS in this repository -- column names, struct fields and payload
# keys that CONTRIBUTING.md keeps Portuguese on purpose -- so a comment
# explaining one of them is correct English prose about a Portuguese name. A
# detector that flagged those would be turned off within a week, which is the
# argument ui-language-check.sh already makes.
WORDS = """que nao não para uma por como sem isso vem foi são pelo mais
já está sendo ser tem dos das aos nas nos qual onde porque então aqui cada
mesmo assim muito quando entre sobre depois antes ainda apenas seu sua seus
suas fazer feito precisa deve pode nenhum nenhuma todos todas invalido
formulario instancia interrompido encerrado esperava ilegivel proveniencia
declaracao desistencia templatavel satisfaz publica"""
PT = re.compile(r"\b(" + "|".join(WORDS.split()) + r")\b", re.I)

# Code spans are stripped first: `task_runs.etapas` and `image:` are names, and
# a name is not prose.
CODE = re.compile(r"`[^`]*`")

# Where prose a person reads lives.
FALA = re.compile(r"fmt\.Errorf|errors\.New|http\.Error|panic\(|slog\.\w+|"
                  r"\.Info\(|\.Warn\(|\.Error\(|\.Debug\(")
STRING = re.compile(r'"((?:[^"\\]|\\.){6,})"')
COMENTARIO = re.compile(r"^\s*(//|#|--)\s*(.+)$")

FORA = ("/vendor/", "/node_modules/", "site/", "/.git/", "/testdata/",
        "_test.go", "_templ.go", "CHANGELOG.md", "CHANGELOG-motor.md", "NOTES.md")
EXTS = {".go", ".md", ".yml", ".yaml", ".sh", ".templ"}
# Makefile and Dockerfile carry operator-facing echoes and have no extension --
# three Portuguese ones lived in the Makefile until 2026-09-10 because every
# check keyed on a suffix.
SEM_EXTENSAO = {"Makefile", "Dockerfile"}

ruins = []
for p in sorted(pathlib.Path(".").rglob("*")):
    s = str(p)
    conhecido = p.suffix in EXTS or p.name in SEM_EXTENSAO
    if not p.is_file() or not conhecido or any(x in s for x in FORA):
        continue
    for n, linha in enumerate(p.read_text(encoding="utf-8", errors="replace").split("\n"), 1):
        c = COMENTARIO.match(linha)
        if c:
            m = PT.search(CODE.sub(" ", c.group(2)))
            if m:
                ruins.append((s, n, m.group(0), "comment", c.group(2)[:60]))
            continue
        if not FALA.search(linha):
            continue
        for raw in STRING.findall(linha):
            m = PT.search(CODE.sub(" ", raw))
            if m:
                ruins.append((s, n, m.group(0), "message", raw[:60]))

if ruins:
    for s, n, palavra, tipo, texto in ruins:
        print(f"::error file={s},line={n}::Portuguese in a {tipo}: {palavra!r} in {texto!r}")
    print(f"\n{len(ruins)} line(s). The repository is English -- see CONTRIBUTING.md.")
    print("If a word here is a false positive, remove it from WORDS in this")
    print("script and say why in the commit.")
    sys.exit(1)
print("✅ comments and messages are in English")
PY
