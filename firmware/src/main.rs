pub mod wifi;

use bme280_rs::{Bme280, Configuration, Oversampling, SensorMode};
use esp_idf_svc::eventloop::EspSystemEventLoop;
use esp_idf_svc::hal::delay::Delay;
use esp_idf_svc::hal::i2c::*;
use esp_idf_svc::hal::peripherals::Peripherals;
use esp_idf_svc::hal::units::FromValueType;
use esp_idf_svc::mqtt::client::{EspMqttClient, EventPayload, MqttClientConfiguration, QoS};
use esp_idf_svc::nvs::EspDefaultNvsPartition;
use esp_idf_svc::sntp::{EspSntp, SyncStatus};
use esp_idf_svc::tls::X509;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::thread;
use std::time::{Duration, Instant};

/// Build-time settings. The values come from `cfg.toml` (git-ignored, see `cfg.toml.example`)
/// and are compiled into the firmware, so no secret lives in the source code.
#[toml_cfg::toml_config]
pub struct Config {
    #[default("")]
    wifi_ssid: &'static str,
    #[default("")]
    wifi_password: &'static str,
    #[default("")]
    mqtt_url: &'static str,
    #[default("")]
    mqtt_username: &'static str,
    #[default("")]
    mqtt_password: &'static str,
}

/// Returns the setting or fails with a clear message, instead of a confusing error later on.
fn require(name: &str, value: &'static str) -> anyhow::Result<&'static str> {
    if value.is_empty() {
        anyhow::bail!("setting `{name}` is empty: create cfg.toml from cfg.toml.example (section [temp_with_wifi])");
    }
    Ok(value)
}

/// Optional settings: an empty string means "not set".
fn non_empty(value: &'static str) -> Option<&'static str> {
    if value.is_empty() {
        None
    } else {
        Some(value)
    }
}

/// Private CA that signed the broker certificate (see certs/ca.crt.example).
/// The trailing NUL is required: mbedTLS reads a PEM as a C string.
static BROKER_CA: &str = concat!(include_str!("../certs/ca.crt"), "\0");

/// TLS checks the certificate validity dates, and right after a boot the clock still shows 1970.
/// So the time must be synchronized before the first connection to the broker.
fn sync_time() -> anyhow::Result<EspSntp<'static>> {
    let sntp = EspSntp::new_default()?;
    log::info!("Waiting for time synchronization (SNTP)...");

    let deadline = Instant::now() + Duration::from_secs(30);
    while !matches!(sntp.get_sync_status(), SyncStatus::Completed) {
        if Instant::now() > deadline {
            anyhow::bail!("time synchronization timed out after 30 seconds");
        }
        thread::sleep(Duration::from_millis(200));
    }

    log::info!("Time is synchronized");
    Ok(sntp)
}

fn main() -> anyhow::Result<()> {
    esp_idf_svc::sys::link_patches();
    esp_idf_svc::log::EspLogger::initialize_default();

    log::info!("Старт IoT Ноды...");

    // Fail fast, before touching any hardware, if the build has no settings.
    let app_config = CONFIG;
    let wifi_ssid = require("wifi_ssid", app_config.wifi_ssid)?;
    let mqtt_url = require("mqtt_url", app_config.mqtt_url)?;

    let peripherals = Peripherals::take()?;
    let sys_loop = EspSystemEventLoop::take()?;
    let nvs = EspDefaultNvsPartition::take()?;

    // 1. Подключаемся к Wi-Fi
    let _wifi = wifi::connect_wifi(
        peripherals.modem,
        sys_loop,
        nvs,
        wifi_ssid,
        app_config.wifi_password,
    )?;

    // 2. Synchronize the clock (needed for TLS). Keep the handle alive: dropping it stops SNTP.
    let _sntp = sync_time()?;

    // 3. MQTT client over TLS
    let connected = Arc::new(AtomicBool::new(false));
    let connected_in_cb = connected.clone();

    let mqtt_config = MqttClientConfiguration {
        client_id: Some("esp32_c3_supermini"),
        username: non_empty(app_config.mqtt_username),
        password: non_empty(app_config.mqtt_password),
        // Verify the broker against our private CA only; nothing else is trusted.
        server_certificate: Some(X509::pem_until_nul(BROKER_CA.as_bytes())),
        ..Default::default()
    };

    let mut mqtt_client =
        EspMqttClient::new_cb(mqtt_url, &mqtt_config, move |event| match event.payload() {
            EventPayload::Connected(_) => {
                connected_in_cb.store(true, Ordering::Relaxed);
                log::info!("MQTT: connected to the broker");
            }
            EventPayload::Disconnected => {
                connected_in_cb.store(false, Ordering::Relaxed);
                log::warn!("MQTT: disconnected, the client will retry by itself");
            }
            EventPayload::Error(e) => log::warn!("MQTT: error: {e:?}"),
            other => log::debug!("MQTT: event: {other:?}"),
        })?;

    // 4. Инициализация I2C на чистых пинах GPIO 6 и GPIO 7
    log::info!("Инициализация шины I2C...");
    let i2c_config = I2cConfig::new().baudrate(100_u32.kHz().into()); // Снизим частоту до 100 kHz для надежности
    let i2c = I2cDriver::new(
        peripherals.i2c0,
        peripherals.pins.gpio6, // Сюда подключай провод SDA
        peripherals.pins.gpio7, // Сюда подключай провод SCL
        &i2c_config,
    )?;

    // Обернем инициализацию датчика в безопасный блок, чтобы плата не крашилась,
    // а мы могли понять, в чем дело.
    let mut bme280 = Bme280::new(i2c, Delay::new_default());

    log::info!("Калибровка датчика BME280...");
    if let Err(e) = bme280.init() {
        log::error!(
            "!!! Датчик BME280 не ответил по базовому адресу: {:?} !!!",
            e
        );
        log::error!(
            "Проверь: 1) SDA -> GPIO 6, SCL -> GPIO 7. 2) Питание датчика на VCC (3.3V) и GND."
        );
        return Err(e.into());
    }

    bme280.set_sampling_configuration(
        Configuration::default()
            .with_temperature_oversampling(Oversampling::Oversample1)
            .with_humidity_oversampling(Oversampling::Oversample1)
            .with_pressure_oversampling(Oversampling::Oversample1)
            .with_sensor_mode(SensorMode::Normal),
    )?;

    log::info!("Датчик готов. Начинаем цикл отправки...");

    loop {
        if !connected.load(Ordering::Relaxed) {
            log::warn!("MQTT is not connected yet, skipping this sample");
            thread::sleep(Duration::from_secs(5));
            continue;
        }

        if let Ok(data) = bme280.read_sample() {
            let temp = data.temperature.unwrap_or(0.0);
            let hum = data.humidity.unwrap_or(0.0);
            let press = data.pressure.unwrap_or(0.0);

            let json_payload = format!(
                "{{\"temperature\": {:.2}, \"humidity\": {:.2}, \"pressure\": {:.2}}}",
                temp, hum, press
            );

            match mqtt_client.publish(
                "home/room/climate",
                QoS::AtMostOnce,
                false,
                json_payload.as_bytes(),
            ) {
                Ok(_) => log::info!("[MQTT СТРИМ] Отправлено: {}", json_payload),
                Err(e) => log::warn!("Ошибка отправки в MQTT: {:?}", e),
            }
        } else {
            log::warn!("Не удалось прочитать данные с датчика (Плата не прислала данные).");
        }

        thread::sleep(Duration::from_secs(5));
    }
}
