#!/usr/bin/env bash
set -eu
if [ "$#" -ne 1 ] || ! command -v node >/dev/null 2>&1; then
  printf '%s\n' TEAMAI_PUBLIC_INPUT
  exit 1
fi
script_dir="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
exec node "$script_dir/check-teamai-public.mjs" "$1"
