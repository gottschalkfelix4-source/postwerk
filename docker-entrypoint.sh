#!/bin/sh
# Starts Postwerk as an unprivileged user. PUID/PGID select the owner of /data
# (Unraid: 99/100 = nobody:users).
set -e

PUID="${PUID:-1000}"
PGID="${PGID:-1000}"

if [ "$(id -u)" = "0" ]; then
  mkdir -p /data
  if [ "$(stat -c '%u:%g' /data)" != "$PUID:$PGID" ]; then
    chown -R "$PUID:$PGID" /data
  fi
  exec su-exec "$PUID:$PGID" /usr/local/bin/postwerk "$@"
fi

exec /usr/local/bin/postwerk "$@"
