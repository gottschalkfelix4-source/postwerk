#!/bin/bash
# Installs the Postwerk templates into Unraid's user template folder.
# Usage (Unraid terminal):
#   curl -fsSL https://raw.githubusercontent.com/gottschalkfelix4-source/postwerk/main/unraid/install.sh | bash
set -euo pipefail

BASE="https://raw.githubusercontent.com/gottschalkfelix4-source/postwerk/main/unraid"
DEST="/boot/config/plugins/dockerMan/templates-user"

if [ ! -d /boot/config ]; then
  echo "Dieses Skript ist für Unraid gedacht (/boot/config nicht gefunden)." >&2
  exit 1
fi

mkdir -p "$DEST"
for t in postwerk postwerk-clamav; do
  curl -fsSL "$BASE/$t.xml" -o "$DEST/my-$t.xml.tmp"
  mv "$DEST/my-$t.xml.tmp" "$DEST/my-$t.xml"
  echo "Template installiert: $DEST/my-$t.xml"
done

mkdir -p /mnt/user/appdata/postwerk
echo
echo "Fertig. Weiter in der Unraid-Oberfläche:"
echo "  Docker → Container hinzufügen → Vorlage: \"Postwerk\" (optional zuerst \"Postwerk-ClamAV\")"
echo "Anleitung: https://github.com/gottschalkfelix4-source/postwerk/blob/main/docs/UNRAID.md"
