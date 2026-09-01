#!/usr/bin/env bash
set -euo pipefail

scanner_version=1
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

pattern='-----BEGIN (RSA |EC |OPENSSH |ENCRYPTED )?PRIVATE KEY-----|AKIA[0-9A-Z]{16}|ASIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{82,}|(^|[^A-Za-z0-9_-])tc[A-Za-z0-9_-]{40,}|tcs1\.[A-Za-z0-9_-]{64,}|sk_live_[A-Za-z0-9]{20,}|sk-(proj|svcacct)-[A-Za-z0-9_-]{20,}|xox[baprs]-[A-Za-z0-9-]{20,}|AIza[0-9A-Za-z_-]{35}'
scan_paths=("$@")
if (( ${#scan_paths[@]} == 0 )); then
  scan_paths=(.)
fi

set +e
matches=$(LC_ALL=C grep \
  --recursive \
  --files-with-matches \
  --extended-regexp \
  --binary-files=without-match \
  --exclude-dir='.git' \
  --exclude-dir='.superpowers' \
  --exclude-dir='node_modules' \
  --exclude-dir='dist' \
  --exclude-dir='bin' \
  --exclude='coverage.out' \
  --exclude='*.png' \
  --exclude='*.jpg' \
  --exclude='*.jpeg' \
  --exclude='*.gif' \
  --exclude='*.webm' \
  -- "$pattern" "${scan_paths[@]}" 2>/dev/null | LC_ALL=C sort -u)
status=$?
set -e

case $status in
  0)
    echo "secret scan v${scanner_version}: high-confidence pattern found in:" >&2
    printf '%s\n' "$matches" >&2
    echo "secret scan v${scanner_version}: pattern-based rules only; no entropy-complete claim" >&2
    exit 1
    ;;
  1)
    echo "secret scan v${scanner_version}: ok (high-confidence pattern rules; pattern-based only)"
    ;;
  *)
    echo "secret scan v${scanner_version}: scanner execution failed" >&2
    exit "$status"
    ;;
esac
