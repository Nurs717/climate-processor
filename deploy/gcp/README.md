# Running on Google Cloud (e2-micro, Always Free)

Layout: ESP32 → `mqtts://<IP>:8883` (Mosquitto, TLS + username/password) → Go service → Telegram bot.
One small VM runs the broker and the service. Prometheus/Grafana are not deployed (1 GB RAM); metrics are available on `127.0.0.1:2112` of the VM itself.

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
  --boot-disk-size=30GB --boot-disk-type=pd-standard --tags=mqtt

# MQTT over TLS for the sensor; SSH only from Google's IAP range
gcloud compute firewall-rules create allow-mqtts --allow=tcp:8883 --target-tags=mqtt
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
# it may read the images and three secrets, nothing else
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

Nothing secret is committed. The CA, the broker certificate and the MQTT password file are created on the VM (`certs/`, `mosquitto/passwd`); the three application secrets move to Secret Manager in step 5. On the VM, once:

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

The Telegram token, the MQTT password of the service and the admin chat IDs live in Secret Manager. `deploy.sh` copies them into `.env` on every deploy. The VM runs as its own service account, which can read exactly these three secrets and the images, and nothing else.

If Terraform has already created the secrets (`infra/secrets.tf`), `gcloud secrets create` below would fail: add the values with `gcloud secrets versions add <name> --data-file=-` instead.

```bash
gcloud services enable secretmanager.googleapis.com

# Copy the three values from the VM's .env into Secret Manager. They travel through a pipe and never appear on screen.
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

## Who may use the bot

Admins are set in the `climate-telegram-admin-chat-ids` secret (Secret Manager). Everybody else sends `/start` to the bot and an admin
approves or declines the request with a button. Approved users are stored in `/data/access.json` (the `climate-data` volume),
so they survive deployments. Admins manage the list in the bot with `/users` and `/remove <chat id>`, no SSH needed.

To make yourself the first admin, send `/start` to the bot while the variable is empty: it replies with your chat ID.
Add it as a new version of that secret and run the CD workflow again. (Older setups used `TELEGRAM_ALLOWED_CHAT_IDS`; it is no longer read.)

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
