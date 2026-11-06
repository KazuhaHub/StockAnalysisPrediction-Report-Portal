#!/usr/bin/env bash
set -euo pipefail
fixture_root="$RUNNER_TEMP/portal-ready-image"
mkdir -m 700 "$fixture_root"
GOWORK=off GOMAXPROCS=2 CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$fixture_root/ready-app" ./scripts/acceptance/ready_app
cp scripts/acceptance/ready_app/Dockerfile "$fixture_root/Dockerfile"
fixture_owner="ready-registry-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"
docker pull registry:3
registry_image=$(docker image inspect registry:3 --format '{{.Id}}')
registry_id=$(docker run -d --label "org.portal.fixture=$fixture_owner" -p 127.0.0.1::5000 --read-only --tmpfs /var/lib/registry:rw,size=67108864,nodev,nosuid,noexec --cap-drop ALL --security-opt no-new-privileges --memory 128m --pids-limit 64 "$registry_image")
printf '%s\n' "$registry_id" > "$RUNNER_TEMP/portal-ready-registry.id"
port=$(docker container port "$registry_id" 5000/tcp)
port=${port##*:}
reference="localhost:$port/portal-ready-fixture:fixture"
docker build --network none -t "$reference" "$fixture_root"
for attempt in {1..20}; do
  if docker push "$reference"; then break; fi
  if [[ "$attempt" == 20 ]]; then exit 1; fi
  sleep 0.2
done
digest=$(docker image inspect "$reference" --format '{{index .RepoDigests 0}}')
[[ "$digest" =~ ^localhost:[0-9]+/portal-ready-fixture@sha256:[a-f0-9]{64}$ ]]
printf 'APP_READY_NATIVE_IMAGE=%s\n' "$digest" >> "$GITHUB_ENV"
