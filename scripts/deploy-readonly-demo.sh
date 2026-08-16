#!/usr/bin/env bash
set -euo pipefail

release_dir="${HAOWORK_DEMO_RELEASE_DIR:-$HOME/haowork-demo}"
unit_dir="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"

test -x "$release_dir/haowork-demo"
mkdir -p "$unit_dir"
install -m 0644 "$release_dir/haowork-demo.service" "$unit_dir/haowork-demo.service"
systemctl --user daemon-reload
systemctl --user enable --now haowork-demo.service
curl --fail --silent --show-error http://127.0.0.1:4175/healthz >/dev/null
printf 'Haowork read-only demo is healthy on 127.0.0.1:4175\n'
