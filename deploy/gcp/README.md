# Running on Google Cloud (e2-micro, Always Free)

Layout: ESP32 → `mqtts://<IP>:8883` (Mosquitto, TLS + username/password) → Go service → Telegram bot.
One small VM runs the broker and the service. Prometheus/Grafana are not deployed (1 GB RAM); metrics are available on `127.0.0.1:2112` of the VM itself.

> Free tier: one e2-micro in `us-central1`, `us-west1` or `us-east1`, up to 30 GB of standard disk.
> Check Google's pricing calculator first: the external IPv4 address may be billed separately.

## 1. Create the VM and open the port

Requires a Google Cloud account with billing and `gcloud` (`gcloud auth login`, `gcloud config set project <ID>`).

```bash
gcloud compute instances create climate-vm \
  --zone=us-central1-a --machine-type=e2-micro \
  --image-family=ubuntu-2404-lts-amd64 --image-project=ubuntu-os-cloud \
  --boot-disk-size=30GB --boot-disk-type=pd-standard \
  --tags=mqtt

gcloud compute firewall-rules create allow-mqtts --allow=tcp:8883 --target-tags=mqtt

# external IP of the VM
gcloud compute instances describe climate-vm --zone=us-central1-a \
  --format='get(networkInterfaces[0].accessConfigs[0].natIP)'
```

If the IP changes (for example after a VM restart without a reserved address), re-issue the certificate and update the address in the firmware. Reserving a static address avoids this.

## 2. Prepare the VM

```bash
gcloud compute ssh climate-vm --zone=us-central1-a
```

On the VM (swap is needed: building Go on 1 GB of RAM can run out of memory otherwise):

```bash
sudo fallocate -l 1G /swapfile && sudo chmod 600 /swapfile && sudo mkswap /swapfile && sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab

curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker $USER   # then log out and ssh in again
```

## 3. Get the project

On the VM:

```bash
git clone https://github.com/Nurs717/climate-processor.git
cd climate-processor/deploy/gcp
```

Updating later is `git pull` followed by `docker compose up -d --build`.

## 4. Generate certificates and passwords

```bash
bash ./scripts/gen-secrets.sh <VM-external-IP>
```

The script prints the ESP32 username and password once. Write them down. Then:

```bash
nano .env      # set TELEGRAM_BOT_TOKEN (a token from @BotFather)
```

Download `certs/ca.crt` to your laptop (the firmware needs it), then move `certs/ca.key` off the VM to a safe place and delete it from the VM:

```bash
gcloud compute scp climate-vm:~/climate-processor/deploy/gcp/certs/ca.crt . --zone=us-central1-a
```

## 5. Start

```bash
docker compose up -d --build
docker compose ps
docker compose logs -f climate-app
```

The logs should contain `Successfully connected to the MQTT broker` and `Successfully subscribed`.

File ownership needs no manual work: the broker runs as uid 1883 inside its container, and the one-shot `mosquitto-init` service in `docker-compose.yml` copies the config, password file, ACL and keys into a volume with the right owner and modes on every `docker compose up`. After changing `mosquitto/acl` or `mosquitto/passwd`, run `docker compose up -d` and then `docker compose restart mosquitto`.

Message the bot `/start`: it shows your chat ID. Add it to `TELEGRAM_ALLOWED_CHAT_IDS` in `.env` and apply with `docker compose up -d`.

## 6. Check without hardware

From your laptop (needs `mosquitto-clients`) publish a test reading:

```bash
mosquitto_pub -h <IP> -p 8883 --cafile ca.crt -u esp32 -P '<ESP32 password>' \
  -t home/room/climate -m '{"temperature":21.5,"humidity":45,"pressure":101325}'
```

`/status` in the bot should now show the values. If no message arrives for 5 minutes, the bot sends a warning
(and one more when data resumes). The threshold is set by `STALE_AFTER`.

## 7. ESP32 firmware

The firmware needs: broker address `mqtts://<IP>:8883`, username `esp32` and its password, the embedded `ca.crt`
for certificate verification, and Wi-Fi/MQTT secrets moved out of the sources into a config file that is not committed to git.
