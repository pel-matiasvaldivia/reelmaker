#!/bin/sh
# Genera la version de la app para publicar como artifact de claude.ai.
#
# claude.ai envuelve la pagina en su propio <head>, asi que publica solo el
# tramo entre los marcadores artifact:start y artifact:end de app/index.html.
#
#   ./tools/make-artifact.sh > reel-refurb.html
set -eu

raiz=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
fuente="$raiz/app/index.html"

if ! grep -q "artifact:start" "$fuente"; then
  echo "no encontre el marcador artifact:start en $fuente" >&2
  exit 1
fi

sed -n '/<!-- artifact:start -->/,/<!-- artifact:end -->/p' "$fuente" \
  | sed '1d;$d'
