# Firmware: ESP32-C3 + BME280

[![Firmware CI](https://github.com/Nurs717/climate-processor/actions/workflows/firmware-ci.yml/badge.svg)](https://github.com/Nurs717/climate-processor/actions/workflows/firmware-ci.yml)

Rust firmware (ESP-IDF `std`) for an ESP32-C3 SuperMini board with a BME280 sensor. It reads temperature, humidity and pressure every 5 seconds and publishes them to an MQTT broker over TLS. The Go service in the repository root consumes the messages.

## What it does

- Connects to Wi-Fi and reconnects when the connection drops.
- Synchronizes the clock with SNTP, which is needed to validate the broker certificate.
- Publishes `{"temperature": 21.50, "humidity": 45.20, "pressure": 101325.00}` (°C, %, Pa) to `home/room/climate`.
- Verifies the broker against a private CA and authenticates with a username and password.
- Never sends made-up values: a failed or incomplete reading is skipped.
- Keeps retrying a sensor that does not answer, and restarts itself after 10 minutes without a successful publish.

## Wiring

| BME280 | ESP32-C3 SuperMini |
|---|---|
| VCC | 3V3 |
| GND | GND |
| SDA | GPIO 6 |
| SCL | GPIO 7 |

The I2C bus runs at 100 kHz.

## Configuration

Settings are compiled into the firmware, nothing is hard-coded in the source.

1. Copy the template and fill it in (the file is git-ignored):

   ```bash
   cp cfg.toml.example cfg.toml
   ```

   | Key | Meaning |
   |---|---|
   | `wifi_ssid`, `wifi_password` | Wi-Fi network (empty password = open network) |
   | `mqtt_url` | `mqtts://<broker-ip>:8883` |
   | `mqtt_username`, `mqtt_password` | Broker credentials of the sensor |

2. Put the public certificate of the private CA that signed the broker certificate to `certs/ca.crt` (also git-ignored). It is created by [`deploy/gcp/scripts/gen-secrets.sh`](../deploy/gcp/scripts/gen-secrets.sh), see [`deploy/gcp/README.md`](../deploy/gcp/README.md). The broker certificate must contain the broker IP in its subject alternative names, because the board connects by IP address (the script does this).

If a changed `cfg.toml` is not picked up by the next build, run `touch src/main.rs`.

## Build and flash

You need Rust installed with `rustup`. The nightly toolchain is pinned in `rust-toolchain.toml`; install it once with `rustup toolchain install`. You also need the linker proxy and the flashing tool:

```bash
cargo install ldproxy cargo-espflash
```

The first build downloads ESP-IDF v5.5.3 and its toolchain into `.embuild/` (several minutes and a few GB of disk).

```bash
cargo build --release
cargo espflash flash --release --monitor --partition-table partitions.csv
```

`--monitor` opens the serial log after flashing (`Ctrl+C` exits, `Ctrl+R` resets the board).

On Linux your user must be in the `dialout` group to access the serial port.

## What the log looks like

```
Starting the IoT node...
Wi-Fi connected, IP address: 192.168.0.150
Time is synchronized
MQTT: connected to the broker
Published: {"temperature": 24.10, "humidity": 40.00, "pressure": 97500.00}
```

When the access point disappears, the board logs `Wi-Fi connection lost, reconnecting...`, skips samples while MQTT is down and resumes publishing by itself.

## Troubleshooting

| Symptom | Cause |
|---|---|
| ``setting `wifi_ssid` is empty`` | `cfg.toml` is missing; create it and run `touch src/main.rs` |
| `No server verification option set` / TLS errors | `certs/ca.crt` is missing or is not the real CA |
| `time synchronization timed out` | the board has no internet access or NTP is blocked |
| `BME280 did not answer` | check the wiring and the 3.3 V supply |
| `espflash::no_serial` | check the USB cable; hold BOOT while plugging in; check the `dialout` group |

## Notes

- The ESP32-C3 SuperMini radio needs two workarounds (see `src/wifi.rs`): lower transmit power and no power saving. WPA3 is disabled in `sdkconfig.defaults` because it broke the WPA2 handshake on some access points.
- The Rust nightly is pinned on purpose: ESP-IDF support in the standard library breaks on some nightlies. CI pins the Ubuntu image too, because a newer `libclang` on `ubuntu-latest` broke the `esp-idf-sys` bindings.

## CI

`.github/workflows/firmware-ci.yml` runs `build`, `fmt` and `clippy` when files in `firmware/` change. The real CA certificate is not in git, so CI builds with the placeholder `certs/ca.crt.example`. The Go pipeline ignores changes in this directory.
