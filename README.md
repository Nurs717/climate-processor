# climate-processor
[![CI](https://github.com/Nurs717/climate-processor/actions/workflows/ci.yml/badge.svg)](https://github.com/Nurs717/climate-processor/actions/workflows/ci.yml) [![Firmware CI](https://github.com/Nurs717/climate-processor/actions/workflows/firmware-ci.yml/badge.svg)](https://github.com/Nurs717/climate-processor/actions/workflows/firmware-ci.yml) [![Terraform CI](https://github.com/Nurs717/climate-processor/actions/workflows/terraform-ci.yml/badge.svg)](https://github.com/Nurs717/climate-processor/actions/workflows/terraform-ci.yml)

A small Go service that turns room climate readings from an ESP32 + BME280 sensor into Prometheus metrics and answers questions about them in a Telegram bot.

```mermaid
flowchart LR
    S["ESP32-C3 + BME280<br/>(Rust firmware)"] -- "MQTT (TLS)<br/>home/room/climate" --> B[(Mosquitto)]
    B --> G["climate-processor (Go)"]
    G -- "/metrics" --> P[Prometheus] --> D[Grafana]
    G <-- "/status, alerts" --> T[Telegram bot]
```

The sensor publishes `{"temperature": 21.5, "humidity": 45.2, "pressure": 101325}` (°C, %, Pa) every few seconds. The service validates each message, updates the metrics and keeps the latest reading for the bot. The firmware that produces these readings is in [`firmware/`](firmware/README.md).

## Features

- **Reliable MQTT client**: subscribes on every (re)connect, retries until the broker is reachable, optional TLS with a private CA and username/password.
- **Input validation**: missing fields and values outside the BME280 range are rejected and counted in `climate_messages_rejected_total{reason}`.
- **Stale-data detection**: if no reading arrives for `STALE_AFTER`, `/status` says so and the bot sends one "sensor is silent" alert, and one more when data resumes.
- **Telegram bot** with admin-approved access, `/status` with the current reading and alerts when the sensor goes silent (see [Telegram bot](#telegram-bot)).
- **Prometheus metrics** and a `/healthz` endpoint on one HTTP port.
- **Graceful shutdown** on SIGINT/SIGTERM, HTTP server timeouts, small Alpine-based Docker image that runs as a non-root user and has a healthcheck.

## Running in production

The service runs on a single Google Cloud VM (`e2-micro`, Always Free tier) next to a TLS-only
Mosquitto broker; the sensor connects to it over the internet.

```mermaid
flowchart LR
    S["ESP32-C3 sensor"] -- "MQTTS :8883" --> V
    subgraph V["GCP VM (e2-micro)"]
        B[(Mosquitto)] --> G["climate-processor"]
    end
    G --> T[Telegram bot]
    GH["GitHub Actions"] -- "build, push, deploy<br/>(Workload Identity, IAP SSH)" --> V
```

- **Delivery**: every push to `main` runs CI (tests, lint, vulncheck). On success, CD builds the image,
  pushes it to Artifact Registry and deploys it over an SSH session tunnelled through IAP (port 22 is
  closed to the internet). There are no long-lived keys: GitHub authenticates to Google with
  Workload Identity Federation, and only runs on `main` of this repository are accepted.
- **Secrets**: the Telegram token, the MQTT password and the admin IDs live in Secret Manager. The VM
  runs under its own service account that can read only those three secrets and the images.
- **Infrastructure as code**: the VM, network, IAM, image registry and secret containers are described
  in Terraform ([`infra/`](infra/README.md)). The state is in a versioned GCS bucket, and a separate CI
  workflow checks formatting and validity on every change.
- **Observability**: Prometheus metrics, a provisioned Grafana dashboard (a local stack, or pointed at the
  cloud VM through an IAP tunnel with `make tunnel` and `make cloud-up`), and a Telegram alert when the
  sensor goes silent.

Details: [`deploy/gcp/README.md`](deploy/gcp/README.md) and [`infra/README.md`](infra/README.md).

## Project layout

```
cmd/app/            entry point: wiring, HTTP server, graceful shutdown
internal/config/    environment-based configuration
internal/domain/    payload and reading types
internal/handler/   MQTT subscriber and Telegram bot
internal/metrics/   Prometheus metrics
internal/service/   validation, state, staleness watcher
firmware/           ESP32-C3 + BME280 firmware in Rust (MQTT over TLS), with its own CI
deploy/local/       one-command local stack: broker, service, Prometheus, Grafana
deploy/gcp/         cloud deployment: Mosquitto (TLS, ACL) + service on a small VM, CI/CD setup
infra/              Terraform: the core GCP resources (state in a GCS bucket), with its own CI
Makefile            everyday commands (run `make` to list them)
```

## Quick start (local, one command)

Requires Docker and `make`.

```bash
make up        # broker + service + Prometheus + Grafana
make send      # publish a test reading (make send T=25 H=60)
make metrics   # see the room_* metrics
make down
```

Prometheus: http://localhost:9090, Grafana: http://localhost:3000 (admin/admin, Prometheus is preconfigured).
To try the Telegram bot, copy `deploy/local/.env.example` to `deploy/local/.env`, fill it in and run `make up` again.
Message the bot `/start`: while no admin is configured it shows your chat ID. Put it into `TELEGRAM_ADMIN_CHAT_IDS` and restart; from then on you are the admin and everybody else asks for access with `/start`.

Without Docker you need an MQTT broker on `127.0.0.1:1883`; then `make run` starts the service and `make test` runs the tests. Run `make` to list all commands.

This is the local setup. Running it in the cloud with automatic deployment from GitHub Actions is described in [`deploy/gcp/README.md`](deploy/gcp/README.md).

## Telegram bot

| Command | Who | What it does |
|---|---|---|
| `/start` | everyone | Approved users get a greeting. A new person sends an access request to the admins and waits for the decision. |
| `/status` | approved users | Current temperature, humidity, pressure (mmHg) and the time of the last reading in the configured timezone. |
| `/users` | admins, private chat | Lists approved users, each with a button to remove access. |
| `/remove <chat id>` | admins, private chat | Removes a user by chat ID. |

**Access model.** Admins are the chat IDs in `TELEGRAM_ADMIN_CHAT_IDS`; they always have access. When
a new person sends `/start`, every admin gets a message with **Allow / Deny** buttons and the person is
told that the request was sent. Approved users are stored in `ACCESS_FILE`, so they survive restarts
and deployments.

**Safeguards.** Admin commands work only in a private chat with an admin, so they are ignored in groups
and strangers get no hint that they exist. The buttons are checked on the server by the user ID of
whoever pressed them: a forwarded message or a forged button payload gives a stranger nothing.

**Alerts.** If no reading arrives for `STALE_AFTER`, everyone with access gets one "sensor is silent"
message, and one more when data resumes.

First run: while no admin is configured, `/start` replies with your chat ID. Put it into
`TELEGRAM_ADMIN_CHAT_IDS` and restart.

## Configuration

| Variable | Default | Description |
|---|---|---|
| `MQTT_BROKER_URL` | `tcp://127.0.0.1:1883` | Broker URL (`tcp://`, `ssl://`) |
| `MQTT_CLIENT_ID` | `go_climate_worker` | MQTT client ID |
| `MQTT_TOPIC` | `home/room/climate` | Topic with sensor readings |
| `MQTT_USERNAME`, `MQTT_PASSWORD` | empty | Broker credentials |
| `MQTT_CA_FILE` | empty | PEM file of the CA that signed the broker certificate |
| `MQTT_TLS_SERVER_NAME` | empty | Name expected in the broker certificate |
| `METRICS_PORT` | `:2112` | Listen address for `/metrics` and `/healthz` |
| `TELEGRAM_BOT_TOKEN` | empty | Bot token from @BotFather (bot disabled if empty) |
| `TELEGRAM_ADMIN_CHAT_IDS` | empty | Comma-separated chat IDs of the administrators (always have access, approve and remove users) |
| `ACCESS_FILE` | empty | JSON file with the users approved in the bot (empty = kept in memory only). The Docker setups use `/data/access.json` on a volume |
| `TIMEZONE` | `Asia/Almaty` | IANA timezone for displayed time |
| `STALE_AFTER` | `5m` | Silence duration after which data is considered stale |

## Metrics

| Metric | Meaning |
|---|---|
| `room_temperature_celsius` | Temperature |
| `room_humidity_percent` | Relative humidity |
| `room_pressure_pascal` | Pressure |
| `room_last_update_timestamp_seconds` | Unix time of the last accepted reading |
| `climate_messages_rejected_total{reason}` | Rejected messages: `parse`, `missing_field`, `out_of_range` |

Example alert expression: `time() - room_last_update_timestamp_seconds > 300`.

## Docker

```bash
docker build -t climate-processor .
```

Deploying to the cloud (a small Google Cloud VM with Mosquitto over TLS, password file and per-user ACLs, plus this service, updated automatically by GitHub Actions) is described in [`deploy/gcp/README.md`](deploy/gcp/README.md). This README covers local development.

## Security notes

- The broker is meant to be exposed only over TLS (port 8883) with per-device credentials; the ACL lets the sensor publish and the service only subscribe.
- Secrets (`.env`, private keys, the password file) are git-ignored; never commit them. A bot token that was ever exposed should be revoked in @BotFather.

## License

MIT, see [LICENSE](LICENSE).
