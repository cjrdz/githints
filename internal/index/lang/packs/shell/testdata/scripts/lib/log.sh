# Logging helpers, sourced by the scripts in bin/.
source "${BASH_SOURCE%/*}/colors.bash"

log() { printf '%s\n' "$*"; }

die () {
  log "$*" >&2
  exit 1
}

lib::trace() {
  [[ -n "${TRACE:-}" ]] && log "trace: $*"
}
