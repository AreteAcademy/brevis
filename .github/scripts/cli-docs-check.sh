#!/usr/bin/env bash
# Every subcommand the binaries have is named in both CLI references.
#
# The two documents -- docs/COMMANDS.md for contributors and the website's
# 07-cli.md for users -- agreed because both were written from the same `--help`
# output. Nothing kept them agreeing, and the failure is silent: a subcommand
# added and documented in one place looks complete from either side.
#
# It compares NAMES only -- of subcommands, and of their FLAGS. Whether two
# documents describe a command well is a judgement call; whether one has stopped
# mentioning something that exists is not, and that is the half worth
# automating.
#
# The flags half was added with --max-attempts and --retry-backoff, and the
# reason is what those two fixed: the scheduler's retry policy existed only in
# Go source, so the only way to find out that three attempts landed inside three
# seconds was to read the dispatcher. Documenting a flag and then letting the
# document drift puts it back where it was.
set -euo pipefail
cd "$(dirname "$0")/../.."

# THREE MODULES, THREE `go` DIRECTIVES, AND THE JOB IS PINNED TO ONE.
#
# This script runs `go run .` in the engine (go 1.27.0), the SDK CLI (1.24)
# and brevis-sql (1.26.0). `actions/setup-go` installs the version a job's
# `go-version-file` names and sets GOTOOLCHAIN=local, which forbids switching
# -- so in brevis-sql's release and the gateway's, where Go is 1.26.0, the
# ENGINE does not build and this reported "could not read the subcommands"
# about a Go version.
#
# It cost both of them on 2026-10-10. brevis-sql stopped before publishing
# anything; the gateway stopped after its images were pushed and before its
# Release existed.
#
# `auto` is Go's own default and reads each module's own directive, which is
# the only answer for a script that drives three of them.
export GOTOOLCHAIN=auto

fail=0

# AND WHEN IT CANNOT READ ONE, IT SAYS WHY.
#
# Both callers printed "could not read the subcommands" and stopped, so a Go
# version nobody could switch to read as a binary with no commands. A check
# that cannot RUN has to name what stopped it, or the next person reads the
# wrong file.
#
# One function because there are two callers: a diagnostic written twice is a
# diagnostic that gets improved once.
unreadable() { # <label> <dir>
  echo "❌ $1: could not read the subcommands, and this is what it said:"
  ( cd "$2" && go run . --help 2>&1 >/dev/null ) | head -5 | sed 's/^/    /'
  fail=1
}

flags() { # <dir> <subcommand> -> one --name per line
  ( cd "$1" && go run . "$2" --help 2>/dev/null ) \
    | awk '/^(Flags|Global Flags):/{f=1;next} /^$/{f=0} f{print}' \
    | grep -oE '\-\-[a-z0-9-]+' | sort -u \
    | grep -vE '^--(help|config)$' || true
}

subcommands() { # <dir> -> one name per line
  ( cd "$1" && go run . --help 2>/dev/null ) \
    | awk '/^Available Commands:/{f=1;next} /^$/{f=0} f{print $1}' \
    | grep -vE '^(help|completion)$' || true
}

check() { # <label> <dir> <doc>...
  local label=$1 dir=$2; shift 2
  local cmds; cmds=$(subcommands "$dir")
  [ -n "$cmds" ] || { unreadable "$label" "$dir"; return; }

  # A MISSING FILE IS A FAILURE, not a skip.
  #
  # It was `[ -f "$doc" ] || continue`, and the site's CLI page was renamed
  # from 07-cli.md to 08-cli.md. Half of this check then passed by doing
  # nothing, for weeks, and `brevis gateway` reached a release documented
  # nowhere a user looks. A check that cannot find what it checks has not
  # passed -- it has not run.
  for doc in "$@"; do
    [ -f "$doc" ] || { echo "❌ $label: $doc does not exist; this check was silently skipping it"; fail=1; }
  done

  for cmd in $cmds; do
    for doc in "$@"; do
      [ -f "$doc" ] || continue
      # The FULL invocation, not the bare name. It searched for "$cmd" alone
      # until `report` was added, and passed instantly: every one of these
      # documents already contained the word "report" in a sentence about
      # something else. A check that cannot fail for a common word is worse
      # than no check, because it reads as coverage.
      if ! grep -qF "$label $cmd" "$doc"; then
        echo "❌ $doc never mentions \`$label $cmd\`"
        fail=1
      fi
    done
  done

  # The flags, against the CONTRIBUTOR reference only. The website's CLI page
  # is a tour and does not carry every flag; COMMANDS.md is the table that
  # claims to.
  for cmd in $cmds; do
    for flag in $(flags "$dir" "$cmd"); do
      if ! grep -qF -- "$flag" docs/COMMANDS.md; then
        echo "❌ docs/COMMANDS.md never mentions \`$flag\` of \`$label $cmd\`"
        fail=1
      fi
    done
  done
  echo "✅ $label: $(echo "$cmds" | tr '\n' ' ')"
}

check "brevis"     ./cmd/brevis      docs/COMMANDS.md site/content/pt/docs/08-cli.md site/content/en/docs/08-cli.md
check "brevis-sdk" ./cmd/brevis-sdk  docs/COMMANDS.md cmd/brevis-sdk/README.md

# THE THIRD BINARY, which this never knew about.
#
# `brevis-sql` was a module nobody taught the gates: `image-pins-check.sh` had
# the same hole and was extended the same way. The cost was measurable here
# too -- `serve` shipped in the binary and was documented NOWHERE, which is
# precisely the failure the check above was repaired to catch for `brevis
# gateway`. A gate that covers two of three binaries is not a gate for the
# third.
#
# It parses a DIFFERENT HELP, because this binary is not cobra: its usage is
# a plain block, two spaces and a name. Reading it at all depends on `--help`
# exiting 0, which it did not until the commit that added this.
# THE FIRST BLOCK ONLY. The usage ends with a paragraph that is also indented
# two spaces, so matching the indent alone read `serve reads BREVIS_...` and
# `one.` as commands -- and a check that invents commands fails for reasons
# nobody can fix, which is its own way of being useless.
sql_subcommands() {
  ( cd sql/cmd/brevis-sql && go run . --help 2>/dev/null ) \
    | awk '/^$/ {b++; next} b==1 && /^  [a-z]/ {print $1}'
}

sql_flags() {
  ( cd sql/cmd/brevis-sql && go run . --help 2>/dev/null ) \
    | grep -oE '^\s+--[a-z0-9-]+' | tr -d ' ' | sort -u
}

check_sql() {
  local label="brevis-sql" docs=("$@")
  local cmds; cmds=$(sql_subcommands)
  [ -n "$cmds" ] || { unreadable "$label" sql/cmd/brevis-sql; return; }

  for doc in "${docs[@]}"; do
    [ -f "$doc" ] || { echo "❌ $label: $doc does not exist"; fail=1; continue; }
    for cmd in $cmds; do
      if ! grep -qF "$label $cmd" "$doc"; then
        echo "❌ $doc never mentions \`$label $cmd\`"
        fail=1
      fi
    done
  done

  # The flags, against the contributor reference only -- the site's page is a
  # tour, as it is for `brevis`.
  for flag in $(sql_flags); do
    if ! grep -qF -- "$flag" docs/COMMANDS.md; then
      echo "❌ docs/COMMANDS.md never mentions \`$flag\` of \`$label\`"
      fail=1
    fi
  done
  echo "✅ $label: $(echo "$cmds" | tr '\n' ' ')"
}

check_sql docs/COMMANDS.md site/content/en/docs/21-sql.md site/content/pt/docs/21-sql.md

exit $fail
