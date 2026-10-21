#!/usr/bin/env bash
set -euo pipefail
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
if ! command -v node >/dev/null 2>&1; then
  printf '%s\n' 'TEAMAI_HAOWORK_ASSETS_INVALID: runtime unavailable' >&2
  exit 1
fi
exec node "$root/validate-assets.mjs"
