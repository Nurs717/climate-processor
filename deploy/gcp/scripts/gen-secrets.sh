#!/usr/bin/env bash
# Generates a private CA, the broker's TLS certificate and the MQTT users/passwords.
# Run it ON THE VM (needs openssl and docker), from anywhere:
#   sudo bash ./scripts/gen-secrets.sh <vm-public-ip>   (in /opt/climate, see README.md)
set -euo pipefail

HOST_IP="${1:?Usage: $0 <vm-public-ip>}"
cd "$(dirname "$0")/.."

if [ -e certs/ca.crt ] || [ -e .env ] || [ -e mosquitto/passwd ]; then
  echo "certs/, .env or mosquitto/passwd already exist. Remove them first if you really want to regenerate." >&2
  exit 1
fi

mkdir -p certs mosquitto
umask 077

# 1. Private CA (10 years). Its certificate (ca.crt) is what the ESP32 firmware and the Go service trust.
openssl genrsa -out certs/ca.key 4096
openssl req -x509 -new -key certs/ca.key -sha256 -days 3650 \
  -subj "/CN=climate-private-ca" -out certs/ca.crt

# 2. Broker certificate, valid for the fixed name mqtt.climate.local and for the VM's IP.
openssl genrsa -out certs/server.key 2048
openssl req -new -key certs/server.key -subj "/CN=mqtt.climate.local" -out certs/server.csr
printf 'subjectAltName=DNS:mqtt.climate.local,IP:%s\nextendedKeyUsage=serverAuth\n' "$HOST_IP" > certs/server.ext
openssl x509 -req -in certs/server.csr -CA certs/ca.crt -CAkey certs/ca.key -CAcreateserial \
  -out certs/server.crt -days 825 -sha256 -extfile certs/server.ext
rm -f certs/server.csr certs/server.ext

# Public certificates must be readable by the (non-root) Go container.
chmod 644 certs/ca.crt certs/server.crt

# 3. MQTT users: esp32 (publishes) and climate (the Go service reads).
ESP_PASSWORD="$(openssl rand -hex 16)"
APP_PASSWORD="$(openssl rand -hex 16)"
docker run --rm -v "$PWD/mosquitto:/work" eclipse-mosquitto:2 sh -c \
  "mosquitto_passwd -b -c /work/passwd esp32 '$ESP_PASSWORD' && mosquitto_passwd -b /work/passwd climate '$APP_PASSWORD'"

cat > .env <<EOF
MQTT_PASSWORD=$APP_PASSWORD
TELEGRAM_BOT_TOKEN=
TELEGRAM_ADMIN_CHAT_IDS=
EOF

cat <<EOF

Done.
  * ESP32 credentials (shown ONCE, put them into the firmware config):
      user: esp32
      password: $ESP_PASSWORD
  * Copy certs/ca.crt to your laptop - the firmware embeds it to verify the broker.
  * Edit .env and set TELEGRAM_BOT_TOKEN. The stack is started by the CD pipeline (push to main).
  * Move certs/ca.key somewhere safe (e.g. your laptop) and delete it from the VM:
    it is only needed to issue a new broker certificate (the current one is valid ~2 years).
EOF
