#!/bin/bash
# Installs xivlite and xivlite-wrap to a folder programs may run from (default ~/.local/share/xivlite)
# and creates ~/.config/xivlite/config from config.example when there is none.
# On DroidDeck, run it from the desktop's terminal; Android shared storage is mounted noexec.
set -eu
src=$(cd "$(dirname "$0")" && pwd)
dest=${1:-$HOME/.local/share/xivlite}
conf=${XDG_CONFIG_HOME:-$HOME/.config}/xivlite/config

[ -f "$src/xivlite" ] || { echo "install.sh: $src/xivlite is missing; run build.sh first" >&2; exit 1; }
mkdir -p "$dest"
install -m 755 "$src/xivlite" "$src/xivlite-wrap" "$dest/"
if [ ! -f "$conf" ]; then
  mkdir -p "$(dirname "$conf")"
  install -m 600 "$src/config.example" "$conf"
  echo "Created $conf - set username, password and game_path in it."
fi

cat <<EOF
Installed to $dest

Steam / DroidDeck: add game/ffxiv_dx11.exe as a game, then set its launch options to
    "$dest/xivlite-wrap" %command%
Check the setup:   "$dest/xivlite" check
EOF
