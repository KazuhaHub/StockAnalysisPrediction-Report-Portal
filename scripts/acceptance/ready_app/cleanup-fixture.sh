#!/usr/bin/env bash
set -euo pipefail
record="$RUNNER_TEMP/portal-ready-registry.id"
if [[ ! -f "$record" ]]; then exit 0; fi
registry_id=$(cat "$record")
[[ "$registry_id" =~ ^[a-f0-9]{64}$ ]]
expected="ready-registry-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"
observed=$(docker container inspect "$registry_id" --format '{{index .Config.Labels "org.portal.fixture"}}')
[[ "$observed" == "$expected" ]]
docker container rm --force "$registry_id"
if docker container inspect "$registry_id" >/dev/null 2>&1; then exit 1; fi
rm "$record"
