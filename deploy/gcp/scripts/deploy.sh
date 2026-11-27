#!/usr/bin/env bash
# Runs ON THE VM as root; the CD pipeline calls it over SSH with sudo.
# Usage: deploy.sh <full-image-reference>
#   e.g. deploy.sh us-central1-docker.pkg.dev/<project>/climate/climate-app:<git-sha>
set -euo pipefail

IMAGE="${1:?Usage: $0 <image-reference>}"
cd "$(dirname "$0")/.."

# Let Docker pull from Artifact Registry with the VM's own service account (it has the Reader role).
gcloud auth configure-docker "${IMAGE%%/*}" --quiet

# .env is rebuilt on every deploy: the image tag (so a manual `docker compose up -d` starts the same version)
# plus the application secrets from Secret Manager. The VM's service account may read exactly these four
# secrets. To rotate one, add a new secret version and deploy again; nothing is edited on the VM by hand.
SECRETS=(
  MQTT_PASSWORD=climate-mqtt-password
  TELEGRAM_BOT_TOKEN=climate-telegram-bot-token
  TELEGRAM_ADMIN_CHAT_IDS=climate-telegram-admin-chat-ids
  TELEGRAM_OIDC_CLIENT_SECRET=climate-telegram-oidc-client-secret
)

umask 077
trap 'rm -f .env.new' EXIT
{
  echo "CLIMATE_APP_IMAGE=$IMAGE"
  for entry in "${SECRETS[@]}"; do
    secret="${entry#*=}"
    value="$(gcloud secrets versions access latest --secret="$secret")"
    # Stop before anything is restarted if a secret is missing or empty.
    [ -n "$value" ] || { echo "secret $secret is empty" >&2; exit 1; }
    echo "${entry%%=*}=$value"
  done
} > .env.new
mv .env.new .env

docker compose pull climate-app
docker compose up -d --remove-orphans
# Remove every image that no container uses, which includes the previous version of climate-app: each deploy
# leaves one tagged image behind, and plain -f removes only untagged ones. A rollback pulls the old image again.
docker image prune -af >/dev/null

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
