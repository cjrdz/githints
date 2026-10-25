#!/usr/bin/env bash
set -euo pipefail

# source ./commented-out.sh is a comment, not an import.
source "$(dirname "$0")/../lib/log.sh"
. "$(dirname "${BASH_SOURCE[0]}")/../lib/env.sh"
. ./checks.sh
[ -f /etc/os-release ] && . /etc/os-release
source "$CONFIG_DIR/settings.sh" # built from a variable: not followed

usage() {
  echo "usage: $0 <env>"
  echo "source ./not-an-import.sh"
}

function deploy {
  local env=$1
  if [ $# -lt 1 ]; then usage; fi
  log "deploying ${#env} chars to $env"
  cat <<EOF
it's a here-document
EOF
}

function rollback() {
  die "not implemented"
}

deploy "$@"
