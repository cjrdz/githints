check_tools() {
  for tool in kubectl helm; do
    command -v "$tool" >/dev/null || { echo "missing $tool" >&2; return 1; }
  done
}
