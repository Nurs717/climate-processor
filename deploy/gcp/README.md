# Запуск в Google Cloud (e2-micro, Always Free)

Схема: ESP32 → `mqtts://<IP>:8883` (Mosquitto, TLS + логин/пароль) → Go-сервис → Telegram-бот.
На одной маленькой VM: брокер + сервис. Prometheus/Grafana не ставим (1 ГБ RAM), метрики доступны на `127.0.0.1:2112` самой VM.

> Бесплатный уровень: одна e2-micro в `us-central1`, `us-west1` или `us-east1`, до 30 ГБ стандартного диска.
> Перед созданием проверьте в калькуляторе цен Google, не тарифицируется ли внешний IPv4 отдельно.

## 1. Создать VM и открыть порт

Нужны аккаунт Google Cloud с биллингом и установленный `gcloud` (`gcloud auth login`, `gcloud config set project <ID>`).

```bash
gcloud compute instances create climate-vm \
  --zone=us-central1-a --machine-type=e2-micro \
  --image-family=ubuntu-2404-lts-amd64 --image-project=ubuntu-os-cloud \
  --boot-disk-size=30GB --boot-disk-type=pd-standard \
  --tags=mqtt

gcloud compute firewall-rules create allow-mqtts --allow=tcp:8883 --target-tags=mqtt

# внешний IP машины
gcloud compute instances describe climate-vm --zone=us-central1-a \
  --format='get(networkInterfaces[0].accessConfigs[0].natIP)'
```

Если IP после перезапуска VM изменится, достаточно перевыпустить сертификат и обновить адрес в прошивке.

## 2. Подготовить VM

```bash
gcloud compute ssh climate-vm --zone=us-central1-a
```

На VM (swap нужен, на 1 ГБ сборка Go иначе может не поместиться):

```bash
sudo fallocate -l 1G /swapfile && sudo chmod 600 /swapfile && sudo mkswap /swapfile && sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab

curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker $USER   # затем выйдите и зайдите по ssh снова
```

## 3. Залить проект

С ноутбука, из папки проекта:

```bash
tar --exclude=.idea --exclude='_backup_before_claude_*' --exclude=.git -czf /tmp/climate.tgz .
gcloud compute scp /tmp/climate.tgz climate-vm:~ --zone=us-central1-a
```

На VM:

```bash
mkdir -p ~/climate && tar -xzf ~/climate.tgz -C ~/climate
cd ~/climate/deploy/gcp
```

## 4. Сгенерировать сертификаты и пароли

```bash
bash ./scripts/gen-secrets.sh <внешний-IP-VM>
```

Скрипт один раз покажет логин и пароль для ESP32. Запишите их. Затем:

```bash
nano .env      # впишите TELEGRAM_BOT_TOKEN (новый токен от @BotFather)
```

Скачайте `certs/ca.crt` на ноутбук (он нужен прошивке), а `certs/ca.key` перенесите с VM в надёжное место и удалите с VM:

```bash
gcloud compute scp climate-vm:~/climate/deploy/gcp/certs/ca.crt . --zone=us-central1-a
```

## 5. Запустить

```bash
docker compose up -d --build
docker compose ps
docker compose logs -f climate-app
```

В логах должно быть `Successfully connected to the MQTT broker` и `Successfully subscribed`.
Напишите боту `/start`: он покажет ваш chat ID. Добавьте его в `TELEGRAM_ALLOWED_CHAT_IDS` в `.env` и перезапустите: `docker compose up -d`.

## 6. Проверка без железа

С ноутбука (нужен `mosquitto-clients`) отправьте тестовое показание:

```bash
mosquitto_pub -h <IP> -p 8883 --cafile ca.crt -u esp32 -P '<пароль ESP32>' \
  -t home/room/climate -m '{"temperature":21.5,"humidity":45,"pressure":101325}'
```

Затем `/status` в боте должен показать значения. Если сообщения нет 5 минут, бот сам пришлёт предупреждение
(и ещё одно, когда данные снова пойдут). Порог меняется переменной `STALE_AFTER`.

## 7. Прошивка ESP32

Нужно поменять в прошивке: адрес брокера на `mqtts://<IP>:8883`, добавить логин `esp32` и пароль, встроить `ca.crt`
для проверки сертификата, а Wi-Fi и пароли вынести из исходников в конфиг, который не попадает в git.
