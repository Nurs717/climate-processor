#!/usr/bin/env bash
# Runs ON THE VM as root; the CD pipeline calls it over SSH with sudo.
# Usage: deploy.sh <full-image-reference>
#   e.g. deploy.sh us-central1-docker.pkg.dev/<project>/climate/climate-app:<git-sha>
set -euo pipefail

IMAGE="${1:?Usage: $0 <image-reference>}"
cd "$(dirname "$0")/.."

# Let Docker pull from Artifact Registry with the VM's own service account (it has the Reader role).
gcloud auth configure-docker "${IMAGE%%/*}" --quiet

# Remember the deployed image in .env, so a manual `docker compose up -d` starts the same version.
umask 077
{ grep -v '^CLIMATE_APP_IMAGE=' .env || true; echo "CLIMATE_APP_IMAGE=$IMAGE"; } > .env.new
mv .env.new .env

docker compose pull climate-app
docker compose up -d --remove-orphans
docker image prune -f >/dev/null

# Fail the deployment (and the pipeline) if the new version does not become healthy.
container="$(docker compose ps -q climate-app)"
for _ in $(seq 1 30); do
  status="$(docker inspect -f '{{.State.Health.Status}}' "$container")"
  if [ "$status" = "healthy" ]; then
    echo "climate-app is healthy: $IMAGE"
    exit 0
  fi
  sleep 3
done

echo "climate-app did not become healthy within 90s" >&2
docker compose logs --tail 50 climate-app >&2
exit 1
