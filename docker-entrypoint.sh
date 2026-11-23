#!/bin/sh
# Start as root: seed the default config on first run, fix bind-mount ownership,
# then drop privileges to PUID/PGID via gosu.
set -e
PUID="${PUID:-10001}"
PGID="${PGID:-10001}"

mkdir -p /app/config /app/data

# Do not seed config.example.yaml here: it intentionally contains no deployment secret.
# With no config file the binary creates config.yaml itself with a fresh random secret_key.

# Docker does not chown bind mounts; self-heal here so the non-root process can read/write config/data.
chown -R "$PUID:$PGID" /app/config /app/data 2>/dev/null || true

# The image runs the portal; arguments are not passed to it. The one other program it starts is the application
# task worker, named as the command (docker-compose.yml, service app-task-worker). Anything else still starts
# the portal, as before.
case "${1:-}" in
/app/app-task-worker) ;;
*) set -- /app/report-portal ;;
esac
exec gosu "$PUID:$PGID" "$@"
