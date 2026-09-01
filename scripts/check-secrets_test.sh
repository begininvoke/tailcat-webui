#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
scanner="$repo_root/scripts/check-secrets.sh"
fixture_root=$(mktemp -d)
trap 'rm -rf -- "$fixture_root"' EXIT

synthetic_secret="ghp_$(printf 'A%.0s' {1..36})"
printf '%s\n' "$synthetic_secret" > "$fixture_root/synthetic-secret.txt"

set +e
output=$("$scanner" "$fixture_root" 2>&1)
status=$?
set -e

if [[ $status -eq 0 ]]; then
  echo "secret scan regression: synthetic secret was accepted" >&2
  exit 1
fi
if [[ $output == *"$synthetic_secret"* ]]; then
  echo "secret scan regression: scanner printed a secret value" >&2
  exit 1
fi
if [[ $output != *"synthetic-secret.txt"* ]]; then
  echo "secret scan regression: scanner did not identify the fixture file" >&2
  exit 1
fi

echo "secret scan regression: ok"
