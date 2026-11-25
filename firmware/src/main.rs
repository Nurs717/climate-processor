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

/// MQTT topic the Go service subscribes to.
const MQTT_TOPIC: &str = "home/room/climate";
/// MQTT client id, it must be unique among the clients of one broker.
const MQTT_CLIENT_ID: &str = "esp32_c3_supermini";
/// Pause between two measurements.
const SAMPLE_INTERVAL: Duration = Duration::from_secs(5);
/// If nothing was published for this long, the board restarts itself.
const MAX_SILENCE: Duration = Duration::from_secs(10 * 60);
/// Pause between two attempts to initialize the sensor.
const SENSOR_RETRY_PAUSE: Duration = Duration::from_secs(5);

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

    log::info!("Starting the IoT node...");

    // Fail fast, before touching any hardware, if the build has no settings.
    let app_config = CONFIG;
    let wifi_ssid = require("wifi_ssid", app_config.wifi_ssid)?;
    let mqtt_url = require("mqtt_url", app_config.mqtt_url)?;

    let peripherals = Peripherals::take()?;
    let sys_loop = EspSystemEventLoop::take()?;
    let nvs = EspDefaultNvsPartition::take()?;

    // 1. Connect to Wi-Fi.
    let mut wifi_driver = wifi::connect_wifi(
        peripherals.modem,
        sys_loop,
        nvs,
        wifi_ssid,
        app_config.wifi_password,
    )?;

    // 2. Synchronize the clock (needed for TLS). Keep the handle alive: dropping it stops SNTP.
    let _sntp = sync_time()?;

    // 3. MQTT client over TLS.
    let connected = Arc::new(AtomicBool::new(false));
    let connected_in_cb = connected.clone();

    let mqtt_config = MqttClientConfiguration {
        client_id: Some(MQTT_CLIENT_ID),
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

    // 4. The sensor on the I2C bus: GPIO 6 is SDA, GPIO 7 is SCL.
    log::info!("Initializing the I2C bus...");
    let i2c_config = I2cConfig::new().baudrate(100_u32.kHz().into()); // 100 kHz is slower but more reliable
    let i2c = I2cDriver::new(
        peripherals.i2c0,
        peripherals.pins.gpio6, // SDA wire
        peripherals.pins.gpio7, // SCL wire
        &i2c_config,
    )?;

    let mut bme280 = Bme280::new(i2c, Delay::new_default());

    // Retry until the sensor answers: a loose wire must not leave the board dead.
    log::info!("Initializing the BME280 sensor...");
    loop {
        if let Err(e) = bme280.init() {
            log::error!("BME280 did not answer: {e:?}");
            log::error!(
                "Check: 1) SDA -> GPIO 6, SCL -> GPIO 7. 2) Sensor power: VCC (3.3V) and GND."
            );
            thread::sleep(SENSOR_RETRY_PAUSE);
            continue;
        }

        let sampling = Configuration::default()
            .with_temperature_oversampling(Oversampling::Oversample1)
            .with_humidity_oversampling(Oversampling::Oversample1)
            .with_pressure_oversampling(Oversampling::Oversample1)
            .with_sensor_mode(SensorMode::Normal);

        match bme280.set_sampling_configuration(sampling) {
            Ok(_) => break,
            Err(e) => {
                log::error!("Cannot configure the BME280: {e:?}");
                thread::sleep(SENSOR_RETRY_PAUSE);
            }
        }
    }

    log::info!("Sensor ready, starting the measurement loop");

    let mut last_publish = Instant::now();
    loop {
        thread::sleep(SAMPLE_INTERVAL);

        // The last line of defence: if the board has been silent for too long, reboot it.
        if last_publish.elapsed() > MAX_SILENCE {
            log::error!(
                "Nothing was published for {} seconds, restarting",
                MAX_SILENCE.as_secs()
            );
            unsafe { esp_idf_svc::sys::esp_restart() };
        }

        if !wifi::ensure_connected(&mut wifi_driver) {
            continue;
        }

        if !connected.load(Ordering::Relaxed) {
            log::warn!("MQTT is not connected yet, skipping this sample");
            continue;
        }

        let sample = match bme280.read_sample() {
            Ok(sample) => sample,
            Err(e) => {
                log::warn!("Cannot read the sensor: {e:?}");
                continue;
            }
        };

        // Never send a made-up value: a missing measurement skips the whole sample.
        let (Some(temperature), Some(humidity), Some(pressure)) =
            (sample.temperature, sample.humidity, sample.pressure)
        else {
            log::warn!("The sensor returned an incomplete sample, skipping it");
            continue;
        };

        let payload = format!(
            "{{\"temperature\": {:.2}, \"humidity\": {:.2}, \"pressure\": {:.2}}}",
            temperature, humidity, pressure
        );

        match mqtt_client.publish(MQTT_TOPIC, QoS::AtMostOnce, false, payload.as_bytes()) {
            Ok(_) => {
                last_publish = Instant::now();
                log::info!("Published: {payload}");
            }
            Err(e) => log::warn!("MQTT publish failed: {e:?}"),
        }
    }
}
