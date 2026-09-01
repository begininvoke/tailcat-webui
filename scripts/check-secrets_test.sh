#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
scanner="$repo_root/scripts/check-secrets.sh"
fixture_root=$(mktemp -d)
trap 'rm -rf -- "$fixture_root"' EXIT

positive_root="$fixture_root/positive"
negative_root="$fixture_root/negative"
mkdir -p "$positive_root" "$negative_root"

synthetic_github_token="ghp_$(printf 'A%.0s' {1..36})"
synthetic_tailcat_token="tc$(printf 'T%.0s' {1..40})"
synthetic_pkcs8_header='-----BEGIN ENCRYPTED PRIVATE'' KEY-----'
printf '%s\n' "$synthetic_github_token" > "$positive_root/github-token.txt"
printf '%s\n' "$synthetic_tailcat_token" > "$positive_root/tailcat-token.txt"
printf '%s\n' "$synthetic_pkcs8_header" > "$positive_root/encrypted-pkcs8.txt"

set +e
positive_output=$("$scanner" "$positive_root" 2>&1)
positive_status=$?
set -e

if [[ $positive_status -eq 0 ]]; then
  echo "secret scan regression: synthetic secrets were accepted" >&2
  exit 1
fi
for synthetic_secret in "$synthetic_github_token" "$synthetic_tailcat_token" "$synthetic_pkcs8_header"; do
  if [[ $positive_output == *"$synthetic_secret"* ]]; then
    echo "secret scan regression: scanner printed a secret value" >&2
    exit 1
  fi
done
for fixture_name in github-token.txt tailcat-token.txt encrypted-pkcs8.txt; do
  if [[ $positive_output != *"$fixture_name"* ]]; then
    echo "secret scan regression: scanner did not identify $fixture_name" >&2
    exit 1
  fi
done

short_tailcat_token="tc$(printf 'N%.0s' {1..39})"
invalid_tailcat_token="tc$(printf 'N%.0s' {1..20}).$(printf 'N%.0s' {1..20})"
embedded_tailcat_token="prefix_tc$(printf 'N%.0s' {1..40})"
near_pkcs8_header='-----BEGIN ENCRYPTED PUBLIC KEY-----'
printf '%s\n' 'ordinary documentation without credentials' > "$negative_root/clean.txt"
printf '%s\n' "$short_tailcat_token" "$invalid_tailcat_token" "$embedded_tailcat_token" "$near_pkcs8_header" > "$negative_root/near-misses.txt"

set +e
negative_output=$("$scanner" "$negative_root" 2>&1)
negative_status=$?
set -e

if [[ $negative_status -ne 0 ]]; then
  echo "secret scan regression: clean or near-miss input was rejected" >&2
  exit 1
fi
if [[ $negative_output != *"secret scan v1: ok"* ]]; then
  echo "secret scan regression: clean scan did not report success" >&2
  exit 1
fi

echo "secret scan regression: ok"
