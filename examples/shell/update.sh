#!/usr/bin/env bash
# Run wml-update and stream its NDJSON events.
# Usage: ./update.sh updater.json 1.4.2 [path/to/wml-update]
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "usage: $0 updater.json 1.4.2 [path/to/wml-update]" >&2
  exit 2
fi

config=$1
current=$2
binary=${3:-${WML_UPDATE:-wml-update}}

# Spawn directly (no shell string) and keep stdout as an event stream.
# Disable pipefail so we can capture the updater's own exit status from
# PIPESTATUS after the display loop.
set +e
"$binary" --config "$config" --json update --current-version "$current" |
while IFS= read -r line; do
  printf '%s\n' "$line"
done
status=${PIPESTATUS[0]}
set -e
exit "$status"
