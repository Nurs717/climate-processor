use esp_idf_svc::eventloop::EspSystemEventLoop;
use esp_idf_svc::hal::modem::WifiModemPeripheral;
use esp_idf_svc::nvs::EspDefaultNvsPartition;
use esp_idf_svc::sys; // low-level radio settings
use esp_idf_svc::wifi::{
    AuthMethod, BlockingWifi, ClientConfiguration, Configuration, EspWifi, PmfConfiguration,
};
use std::thread;
use std::time::Duration;

/// Pause between two connection attempts, so a broken access point is not hammered.
const RETRY_PAUSE: Duration = Duration::from_secs(2);

/// The Wi-Fi driver in its blocking flavour.
pub type Wifi = BlockingWifi<EspWifi<'static>>;

/// Starts Wi-Fi and blocks until the board is connected and has an IP address.
pub fn connect_wifi(
    modem: impl WifiModemPeripheral + 'static,
    sys_loop: EspSystemEventLoop,
    nvs: EspDefaultNvsPartition,
    ssid: &str,
    password: &str,
) -> anyhow::Result<Wifi> {
    let esp_wifi = EspWifi::new(modem, sys_loop.clone(), Some(nvs))?;
    let mut wifi = BlockingWifi::wrap(esp_wifi, sys_loop)?;

    log::info!("Configuring Wi-Fi...");

    wifi.set_configuration(&Configuration::Client(ClientConfiguration {
        ssid: ssid
            .try_into()
            .map_err(|_| anyhow::anyhow!("Wi-Fi name is too long (32 bytes at most)"))?,
        password: password
            .try_into()
            .map_err(|_| anyhow::anyhow!("Wi-Fi password is too long (64 bytes at most)"))?,
        auth_method: if password.is_empty() {
            AuthMethod::None
        } else {
            AuthMethod::WPA2Personal
        },
        pmf_cfg: PmfConfiguration::Capable { required: false },
        ..Default::default()
    }))?;

    wifi.start()?;

    // Stability workarounds for the radio of the ESP32-C3 SuperMini board.
    log::info!("Applying radio stability settings...");
    unsafe {
        // 1. Lower the transmit power to 10 dBm (the unit is 0.25 dBm), so the board's LDO does not sag.
        sys::esp_wifi_set_max_tx_power(40);

        // 2. Switch power saving off (0 is WIFI_PS_NONE in the C code),
        //    so the modem does not fall asleep in the middle of a handshake.
        sys::esp_wifi_set_ps(0);
    }

    log::info!("Wi-Fi started, joining the network: {ssid}");

    while let Err(e) = connect(&mut wifi) {
        log::warn!("Wi-Fi connection failed: {e:?}");
        let _ = wifi.disconnect();
        thread::sleep(RETRY_PAUSE);
    }

    Ok(wifi)
}

/// Makes sure the board is connected and tries to reconnect once if it is not.
/// Returns whether the board is connected afterwards.
pub fn ensure_connected(wifi: &mut Wifi) -> bool {
    match wifi.is_connected() {
        Ok(true) => return true,
        Ok(false) => log::warn!("Wi-Fi connection lost, reconnecting..."),
        Err(e) => log::warn!("Cannot read the Wi-Fi state: {e:?}"),
    }

    match connect(wifi) {
        Ok(()) => true,
        Err(e) => {
            log::warn!("Wi-Fi reconnection failed: {e:?}");
            let _ = wifi.disconnect();
            false
        }
    }
}

/// One connection attempt: join the access point and wait for an IP address.
fn connect(wifi: &mut Wifi) -> anyhow::Result<()> {
    log::info!("Connecting to the access point...");
    wifi.connect()?;

    log::info!("Authenticated, waiting for an IP address from the router...");
    wifi.wait_netif_up()?;

    let ip_info = wifi.wifi().sta_netif().get_ip_info()?;
    log::info!("Wi-Fi connected, IP address: {}", ip_info.ip);
    Ok(())
}
