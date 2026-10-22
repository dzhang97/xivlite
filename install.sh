#!/bin/bash
# Installs xivlite and xivlite-wrap to a folder programs may run from (default ~/.local/share/xivlite)
# and, when there is no config yet, runs `xivlite setup` to ask for the account and write one.
# On DroidDeck, run it from the desktop's terminal; Android shared storage is mounted noexec.
set -eu
src=$(cd "$(dirname "$0")" && pwd)
dest=${1:-$HOME/.local/share/xivlite}
conf=${XDG_CONFIG_HOME:-$HOME/.config}/xivlite/config

[ -f "$src/xivlite" ] || { echo "install.sh: $src/xivlite is missing; run build.sh first" >&2; exit 1; }
mkdir -p "$dest"
install -m 755 "$src/xivlite" "$src/xivlite-wrap" "$dest/"
echo "Installed to $dest"
if [ ! -f "$conf" ] && [ -t 0 ]; then
  echo
  "$dest/xivlite" setup || echo "Setup did not finish; run it again with: \"$dest/xivlite\" setup"
fi

cat <<EOF

Steam / DroidDeck: add game/ffxiv_dx11.exe as a game, then set its launch options to
    "$dest/xivlite-wrap" %command%
Change the account, password or game folder:  "$dest/xivlite" setup
Try the login without starting the game:      "$dest/xivlite" login
EOF
