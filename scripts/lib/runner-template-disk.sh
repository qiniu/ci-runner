# The tracked configs use one scalar per line and dotted keys at the root.
# Reject other TOML syntax so a nested or multiline value cannot shadow a key.
template_disk_mib() {
  local config="$1" key="$2" value
  value="$(awk -v key="$key" '
    /^[[:space:]]*(#|$)/ { next }
    /^[[:space:]]*[[:alnum:]_.-]+[[:space:]]*=[[:space:]]*("([^"\\]|\\.)*"|[0-9]+|true|false)[[:space:]]*(#.*)?$/ {
      name = $0
      sub(/^[[:space:]]*/, "", name)
      sub(/[[:space:]]*=.*/, "", name)
      if (name == key) {
        sub(/^[^=]*=[[:space:]]*/, "")
        sub(/[[:space:]]*(#.*)?$/, "")
        print
      }
      next
    }
    { exit 1 }
  ' "$config")" || {
    echo "expected flat single-line TOML in $config" >&2
    return 65
  }
  if [[ ! "$value" =~ ^[1-9][0-9]{0,9}$ ]] || ((value > 2147483647)); then
    echo "template config has no valid $key: $config" >&2
    return 65
  fi
  printf '%s\n' "$value"
}
