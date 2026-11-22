#!/bin/sh
# Starts Postwerk as an unprivileged user. PUID/PGID select the owner of /data
# (Unraid: 99/100 = nobody:users).
set -e

PUID="${PUID:-1000}"
PGID="${PGID:-1000}"

# New files (database, keys, mail, backups) are not readable for other host
# users; the group keeps read access for Unraid shares.
umask 027

if [ "$(id -u)" = "0" ]; then
  mkdir -p /data
  if [ "$(stat -c '%u:%g' /data)" != "$PUID:$PGID" ]; then
    chown -R "$PUID:$PGID" /data
  fi
  # Files created by older versions (e.g. the database) were world-readable.
  find /data -maxdepth 1 -type f -perm -o=r -exec chmod o-rwx {} +
  exec su-exec "$PUID:$PGID" /usr/local/bin/postwerk "$@"
fi

exec /usr/local/bin/postwerk "$@"
