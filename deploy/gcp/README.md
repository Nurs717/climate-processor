# Running on Google Cloud (e2-micro, Always Free)

Layout: ESP32 → `mqtts://<IP>:8883` (Mosquitto, TLS + username/password) → Go service → Telegram bot and REST API (behind Caddy, HTTPS).
One small VM runs the broker, the service and Caddy. Prometheus/Grafana are not deployed (1 GB RAM); metrics are available on `127.0.0.1:2112` of the VM itself.

> This page is about the cloud deployment. To run everything locally with one command (no cloud needed), see the [Quick start in the root README](../../README.md#quick-start-local-one-command).

> The cloud resources (VM, network, service accounts and permissions, registry, secret containers, Workload Identity) are also defined as code in [`infra/`](../../infra/README.md). The `gcloud` commands below are the same setup done by hand: a reference, not needed when you use Terraform. Terraform does not set up the VM software (Docker, swap, certificates), the secret values or the GitHub Variables.

> Free tier: one e2-micro in `us-central1`, `us-west1` or `us-east1`, up to 30 GB of standard disk.
> Google may bill the external IPv4 address separately: look for the "External IP" line in the billing report and set a budget alert.

## How deployment works

```
git push to main → CI (tests, lint, vulncheck) → CD workflow:
  1. builds the Docker image and pushes it to Artifact Registry (tag = git commit SHA)
  2. opens an SSH session to the VM through an IAP tunnel (port 22 is closed to the internet)
  3. ships this folder to /opt/climate and runs scripts/deploy.sh there
  4. deploy.sh writes .env from Secret Manager, pulls the image, restarts the service and waits until it is healthy
```

- GitHub authenticates to Google with **Workload Identity Federation**: no JSON keys anywhere. Only runs of this repository on `main` can use the `github-deployer` service account.
- Everything the pipeline needs is configured as GitHub **Variables** (not secrets): `GCP_PROJECT_ID`, `GCP_REGION`, `GCP_ZONE`, `GCP_VM_NAME`, `GCP_WIF_PROVIDER`, `GCP_DEPLOYER_SA`.
- On the VM the stack lives in `/opt/climate` (owned by root). The pipeline replaces the files that are in git and never touches `certs/` or `mosquitto/passwd`. `.env` is rebuilt by `deploy.sh` from Secret Manager.
- Rollback: re-run the CD workflow of an older commit in the Actions tab.

## One-time setup

Run these in Cloud Shell (or any shell with `gcloud`). Replace the placeholders.

### 1. VM and network

```bash
gcloud compute instances create climate-vm \
  --zone=us-central1-a --machine-type=e2-micro \
  --image-family=ubuntu-2404-lts-amd64 --image-project=ubuntu-os-cloud \
  --boot-disk-size=30GB --boot-disk-type=pd-standard --tags=mqtt,web

# MQTT over TLS for the sensor; HTTP(S) for Caddy; SSH only from Google's IAP range
gcloud compute firewall-rules create allow-mqtts --allow=tcp:8883 --target-tags=mqtt
gcloud compute firewall-rules create allow-web --allow=tcp:80,tcp:443 --target-tags=web
gcloud compute firewall-rules create allow-iap-ssh --network=default --direction=INGRESS \
  --action=ALLOW --rules=tcp:22 --source-ranges=35.235.240.0/20
```

Reserve a static address for the VM so the broker IP (it is inside the TLS certificate) never changes. From now on SSH always needs the tunnel flag:

```bash
gcloud compute ssh climate-vm --zone=us-central1-a --tunnel-through-iap
```

On the VM (swap helps a 1 GB machine; Docker is needed):

```bash
sudo fallocate -l 1G /swapfile && sudo chmod 600 /swapfile && sudo mkswap /swapfile && sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
curl -fsSL https://get.docker.com | sudo sh
```

### 2. Image registry

```bash
gcloud services enable artifactregistry.googleapis.com iamcredentials.googleapis.com sts.googleapis.com iap.googleapis.com oslogin.googleapis.com
gcloud artifacts repositories create climate --repository-format=docker --location=us-central1

# The VM runs as its own least-privilege service account (attached to the VM in step 5):
# it may read the images and four secrets, nothing else
PROJECT_ID=$(gcloud config get-value project)
gcloud iam service-accounts create climate-vm --display-name="climate-vm runtime"
VM_SA="climate-vm@${PROJECT_ID}.iam.gserviceaccount.com"
gcloud artifacts repositories add-iam-policy-binding climate --location=us-central1 \
  --member="serviceAccount:$VM_SA" --role=roles/artifactregistry.reader
```

Keep the registry small with a cleanup policy (keep the 5 newest versions, delete the rest), see `gcloud artifacts repositories set-cleanup-policies`.

### 3. GitHub → Google access (no keys)

```bash
PROJECT_ID=$(gcloud config get-value project)
PROJECT_NUMBER=$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)')
GH_REPO="<owner>/<repo>"

gcloud iam service-accounts create github-deployer --display-name="GitHub Actions deployer"
gcloud iam workload-identity-pools create github --location=global
gcloud iam workload-identity-pools providers create-oidc github-oidc \
  --location=global --workload-identity-pool=github \
  --issuer-uri="https://token.actions.githubusercontent.com" \
  --attribute-mapping="google.subject=assertion.sub,attribute.repository=assertion.repository,attribute.ref=assertion.ref" \
  --attribute-condition="assertion.repository == '${GH_REPO}' && assertion.ref == 'refs/heads/main'"

DEPLOYER="serviceAccount:github-deployer@${PROJECT_ID}.iam.gserviceaccount.com"
gcloud iam service-accounts add-iam-policy-binding "${DEPLOYER#serviceAccount:}" \
  --role=roles/iam.workloadIdentityUser \
  --member="principalSet://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/github/attribute.repository/${GH_REPO}"

# Least privilege for the deployer: push images, tunnel and log in to this one VM
gcloud artifacts repositories add-iam-policy-binding climate --location=us-central1 --member="$DEPLOYER" --role=roles/artifactregistry.writer
gcloud compute instances add-iam-policy-binding climate-vm --zone=us-central1-a --member="$DEPLOYER" --role=roles/compute.osAdminLogin
gcloud iam service-accounts add-iam-policy-binding "$VM_SA" --member="$DEPLOYER" --role=roles/iam.serviceAccountUser
gcloud projects add-iam-policy-binding "$PROJECT_ID" --member="$DEPLOYER" --role=roles/iap.tunnelResourceAccessor
gcloud projects add-iam-policy-binding "$PROJECT_ID" --member="$DEPLOYER" --role=roles/compute.viewer
```

Then set the six GitHub Variables listed above (`gh variable set NAME --repo <owner>/<repo> --body VALUE`). `GCP_WIF_PROVIDER` is `projects/<PROJECT_NUMBER>/locations/global/workloadIdentityPools/github/providers/github-oidc`.

### 4. Secrets on the VM

Nothing secret is committed. The CA, the broker certificate and the MQTT password file are created on the VM (`certs/`, `mosquitto/passwd`); the application secrets move to Secret Manager in step 5. On the VM, once:

```bash
git clone https://github.com/<owner>/<repo>.git /tmp/climate-src
sudo mkdir -p /opt/climate && sudo cp -r /tmp/climate-src/deploy/gcp/. /opt/climate/ && rm -rf /tmp/climate-src
sudo bash /opt/climate/scripts/gen-secrets.sh <VM-external-IP>
sudo nano /opt/climate/.env      # set TELEGRAM_BOT_TOKEN (from @BotFather) and TELEGRAM_ADMIN_CHAT_IDS
```

The script prints the ESP32 username and password once: save them in a password manager. Then **move the CA private key off the VM** (it is only needed to issue a new broker certificate) and keep `ca.crt` for the firmware:

```bash
sudo cp /opt/climate/certs/ca.crt /opt/climate/certs/ca.key ~ && sudo chown $USER ~/ca.crt ~/ca.key
# download both (for example with `gcloud compute scp --tunnel-through-iap`), then on the VM:
sudo shred -u /opt/climate/certs/ca.key && rm ~/ca.key ~/ca.crt
```

### 5. Secret Manager and the VM's service account

The Telegram token, the MQTT password of the service, the admin chat IDs and the client secret of the Telegram sign-in live in Secret Manager. `deploy.sh` copies them into `.env` on every deploy. The VM runs as its own service account, which can read exactly these four secrets and the images, and nothing else.

If Terraform has already created the secrets (`infra/secrets.tf`), `gcloud secrets create` below would fail: add the values with `gcloud secrets versions add <name> --data-file=-` instead.

```bash
gcloud services enable secretmanager.googleapis.com

# Copy the three values from the VM's .env into Secret Manager. They travel through a pipe and never appear on screen.
# The fourth secret, climate-telegram-oidc-client-secret, is not in the VM's .env: see "Owner sign-in (Telegram)".
for pair in climate-mqtt-password:MQTT_PASSWORD climate-telegram-bot-token:TELEGRAM_BOT_TOKEN climate-telegram-admin-chat-ids:TELEGRAM_ADMIN_CHAT_IDS; do
  secret=${pair%%:*}; key=${pair##*:}
  gcloud compute ssh climate-vm --zone=us-central1-a --tunnel-through-iap --quiet \
    --command "sudo grep '^${key}=' /opt/climate/.env | cut -d= -f2- | tr -d '\n'" \
    | gcloud secrets create "$secret" --data-file=- --replication-policy=automatic
  gcloud secrets add-iam-policy-binding "$secret" \
    --member="serviceAccount:$VM_SA" --role=roles/secretmanager.secretAccessor
done

# A VM can change its service account only while it is stopped (the reserved static IP stays attached)
gcloud compute instances stop climate-vm --zone=us-central1-a
gcloud compute instances set-service-account climate-vm --zone=us-central1-a \
  --service-account="$VM_SA" --scopes=cloud-platform
gcloud compute instances start climate-vm --zone=us-central1-a
```

To rotate a value, add a new version and run the CD workflow again (Actions tab):

```bash
gcloud secrets versions add climate-telegram-bot-token --data-file=-   # paste the value, then Ctrl+D (not Enter)
```

The first push to `main` after this deploys the stack.

## Day-to-day

```bash
cd /opt/climate
sudo docker compose ps
sudo docker compose logs -f climate-app
```

After editing `mosquitto/acl` or `mosquitto/passwd` run `sudo docker compose up -d` and then `sudo docker compose restart mosquitto`.
File ownership needs no manual work: the one-shot `mosquitto-init` service copies the config, ACL, password file and keys into a volume owned by uid 1883 (the broker user) on every `docker compose up`.

The broker certificate is valid for 825 days from the day `gen-secrets.sh` ran. To renew it you need the CA key you stored off the VM.

## Public HTTPS (Caddy)

Caddy listens on ports 80 and 443 (the `allow-web` firewall rule), gets a Let's Encrypt certificate by itself, renews it,
redirects HTTP to HTTPS and proxies everything to the API port of the service (`climate-app:8080`).
`/metrics` and `/healthz` are on another port that is never published to the internet.

The public name is `SITE_ADDRESS` in `docker-compose.yml`. It starts as `34-28-22-93.sslip.io`: sslip.io is a free DNS service
that answers with the IP address written in the name, so a valid certificate needs no purchased domain.
To switch to your own domain, create an `A` record that points to the VM's static IP, change `SITE_ADDRESS` and push:
the pipeline redeploys and Caddy obtains a new certificate. Change `PUBLIC_URL` of `climate-app` (same file) together with it, and register the new
Redirect URI in @BotFather (see "Owner sign-in").

Certificates and the ACME account live in the `caddy-data` volume and survive deployments. Do not delete the volume:
Let's Encrypt allows only a few new certificates per host name per week.

The service finds the real client address in the `X-Forwarded-For` header that Caddy sets, but believes that header only when
the request comes from a proxy listed in `TRUSTED_PROXIES` (the private Docker networks, set in `docker-compose.yml`).
Every client address gets its own request budget: `API_RATE_PER_MIN` requests per minute (default 60) with a burst of
`API_RATE_BURST` (default 20). A history query (`/readings`, `/stats`) costs 5 requests, `/latest` costs 1.
A client over the budget gets `429 Too Many Requests` with a `Retry-After` header (seconds). `API_RATE_PER_MIN=0` switches all limits off.

The signed-in owner has a higher budget of their own: `API_OWNER_RATE_PER_MIN` (default 600) with a burst of `API_OWNER_RATE_BURST`
(default 100), the same prices per request. It is counted per user and not per address, so it does not depend on the network the owner
is on, and a busy demo client on the same address cannot use it up. `API_OWNER_RATE_PER_MIN=0` gives the owner the budget of everybody.
A request that carries a session token also takes one token per request from the owner budget of its address, before the token is
checked: a flood of made-up tokens is refused before it reaches the database.

The files of the web app (the page, its scripts, styles and icons) have a limit of their own, counted per client address: `WEB_RATE_PER_MIN`
(default 600) with a burst of `WEB_RATE_BURST` (default 120), one request per file. Opening the page takes about a dozen files, so a few
reloads must not use up the budget of the API, and a client that has used up the API budget can still load the page. `WEB_RATE_PER_MIN=0`
switches this limit off; `API_RATE_PER_MIN=0` switches it off together with all the others.

The deploy script waits only for the service to become healthy, not for Caddy. Check HTTPS after a deployment:

```bash
curl -s https://<SITE_ADDRESS>/api/v1/latest             # from anywhere
cd /opt/climate && sudo docker compose logs caddy         # on the VM, look for "certificate obtained successfully"
```

If it does not work: the DNS name must resolve to the VM's IP, the `allow-web` rule must exist and the VM must have the `web` tag.

## Who may use the bot

Admins are set in the `climate-telegram-admin-chat-ids` secret (Secret Manager). Everybody else sends `/start` to the bot and an admin
approves or declines the request with a button. Approved users are stored in `/data/access.json` (the `climate-data` volume),
so they survive deployments. Admins manage the list in the bot with `/users` and `/remove <chat id>`, no SSH needed.

To make yourself the first admin, send `/start` to the bot while the variable is empty: it replies with your chat ID.
Add it as a new version of that secret and run the CD workflow again. (Older setups used `TELEGRAM_ALLOWED_CHAT_IDS`; it is no longer read.)

## Owner sign-in (Telegram)

Everybody can read the data without signing in: an anonymous client has the read-only `demo` role. The owner signs in with
Telegram to get higher limits and, later, the private features. The service uses "Log In With Telegram" (OpenID Connect):
Telegram confirms who the user is, and the service lets in only the admins from `TELEGRAM_ADMIN_CHAT_IDS`.
A sign-in is an opaque session token in an `HttpOnly` cookie (or an `Authorization: Bearer` header for apps). The database keeps only
its hash, so a leaked database cannot be used to sign in, and a session can be ended at any time: by signing out or by removing the admin.

| Setting | Where | Meaning |
|---|---|---|
| `PUBLIC_URL` | `docker-compose.yml` | the address of the service as the browser sees it, no path; `https` only (plain `http` only for `localhost`) |
| `TELEGRAM_OIDC_CLIENT_ID` | `docker-compose.yml` | the bot id from @BotFather; not a secret |
| `TELEGRAM_OIDC_CLIENT_SECRET` | Secret Manager `climate-telegram-oidc-client-secret` | the client secret; never in git |
| `SESSION_TTL` | `docker-compose.yml` | how long a sign-in lasts (default and current value: `720h`, 30 days) |

The sign-in is on only when all of these and `DB_PATH` (the sessions live in the database) are set. Otherwise the service logs why it is off
and keeps working with everybody anonymous.

Setup, once:

1. In @BotFather open the mini app (the **Open** button), choose the bot, **Login Widget**, then **Switch to OpenID Connect Login**.
2. Add the Redirect URI `https://<SITE_ADDRESS>/api/v1/auth/telegram/callback`. It must match character for character
   the `redirect_uri` that the service writes to its log at start ("owner sign-in is on").
3. Create the secret container and give the VM access to it with Terraform (`infra/`):
   ```bash
   cd infra && terraform plan -out=tfplan   # 2 to add (the secret and the VM's access to it), 0 to change, 0 to destroy
   terraform apply tfplan && rm tfplan
   ```
4. Add the Client Secret from the same BotFather screen as the secret's value. It travels through a pipe, never through a file or a chat:
   ```bash
   gcloud secrets versions add climate-telegram-oidc-client-secret --data-file=-   # paste the value, then Ctrl+D (not Enter)
   ```
   If the secret was ever exposed, revoke it in BotFather and add the new one the same way.
5. Push, or run the CD workflow. The log of `climate-app` must contain `owner sign-in is on`.

Do the steps in this order: `deploy.sh` refuses to restart anything when a secret has no value, so the container and its value must exist
before the deploy that reads them.

Check:

```bash
curl -s https://<SITE_ADDRESS>/api/v1/auth/me      # {"role":"demo","sign_in_enabled":true}
```

Then open `https://<SITE_ADDRESS>/api/v1/auth/telegram/login` in a browser and confirm in Telegram. You land on `/`, which is a JSON 404 until the
web app exists; that is expected. Open `/api/v1/auth/me`: it must now say `"role":"owner"`.
An account that is not an admin gets `403`, and the service logs a warning with its Telegram id.
To sign out, send `POST /api/v1/auth/logout` (for example `fetch('/api/v1/auth/logout', {method: 'POST'})` in the browser console).

## Check without hardware

From a machine with `mosquitto-clients`, publish a test reading:

```bash
mosquitto_pub -h <IP> -p 8883 --cafile ca.crt -u esp32 -P '<ESP32 password>' \
  -t home/room/climate -m '{"temperature":21.5,"humidity":45,"pressure":101325}'
```

Then `/status` in the bot should show the values. If no message arrives for 5 minutes, the bot sends a warning
(and one more when data resumes). The threshold is set by `STALE_AFTER`.

## ESP32 firmware

The firmware lives in [`firmware/`](../../firmware/README.md). It needs the broker address `mqtts://<IP>:8883`, the username `esp32` and its password,
the CA certificate `ca.crt` for certificate verification, and the Wi-Fi settings in a `cfg.toml` that is not committed to git.
