#!/usr/bin/env bash
set -euo pipefail

apps_root="${APP_SCHEDULED_DIGEST_APPS_ROOT:?APP_SCHEDULED_DIGEST_APPS_ROOT is required}"
record="${RUNNER_TEMP:?RUNNER_TEMP is required}/portal-ready-registry.id"
[[ -f "$record" ]]
registry_id=$(cat "$record")
[[ "$registry_id" =~ ^[a-f0-9]{64}$ ]]
port=$(docker container port "$registry_id" 5000/tcp)
port=${port##*:}
[[ "$port" =~ ^[0-9]+$ ]]
service="$apps_root/apps/scheduled-digest/service"
[[ -f "$service/Dockerfile" && -f "$service/go.mod" ]]

reference="localhost:$port/scheduled-digest-exact:fixture"
docker build \
  --file "$service/Dockerfile" \
  --tag "$reference" \
  "$apps_root"
for attempt in {1..20}; do
  if docker push "$reference"; then break; fi
  [[ "$attempt" != 20 ]] || exit 1
  sleep 0.2
done
digest=$(docker image inspect "$reference" --format '{{index .RepoDigests 0}}')
image_id=$(docker image inspect "$reference" --format '{{.Id}}')
[[ "$digest" =~ ^localhost:[0-9]+/scheduled-digest-exact@sha256:[a-f0-9]{64}$ ]]
[[ "$image_id" =~ ^sha256:[a-f0-9]{64}$ ]]
printf 'APP_SCHEDULED_DIGEST_NATIVE_IMAGE=%s\n' "$digest" >> "$GITHUB_ENV"
printf '%s\n%s\n' "$digest" "$image_id" > "$RUNNER_TEMP/scheduled-digest-exact-image.txt"
