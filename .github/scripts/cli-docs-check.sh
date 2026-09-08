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

fail=0

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
  [ -n "$cmds" ] || { echo "❌ $label: could not read the subcommands"; fail=1; return; }

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

check "brevis"     ./cmd/brevis      docs/COMMANDS.md site/content/pt/docs/07-cli.md site/content/en/docs/07-cli.md
check "brevis-sdk" ./cmd/brevis-sdk  docs/COMMANDS.md cmd/brevis-sdk/README.md

exit $fail
