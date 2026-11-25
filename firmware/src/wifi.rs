use esp_idf_svc::eventloop::EspSystemEventLoop;
use esp_idf_svc::hal::modem::WifiModemPeripheral;
use esp_idf_svc::nvs::EspDefaultNvsPartition;
use esp_idf_svc::sys; // Используем для низкоуровневых настроек чипа
use esp_idf_svc::wifi::{
    AuthMethod, BlockingWifi, ClientConfiguration, Configuration, EspWifi, PmfConfiguration,
};
use std::thread;
use std::time::Duration;

pub fn connect_wifi(
    modem: impl WifiModemPeripheral + 'static,
    sys_loop: EspSystemEventLoop,
    nvs: EspDefaultNvsPartition,
    ssid: &str,
    password: &str,
) -> anyhow::Result<BlockingWifi<EspWifi<'static>>> {
    let esp_wifi = EspWifi::new(modem, sys_loop.clone(), Some(nvs))?;
    let mut wifi = BlockingWifi::wrap(esp_wifi, sys_loop)?;

    log::info!("Настройка Wi-Fi конфигурации...");

    wifi.set_configuration(&Configuration::Client(ClientConfiguration {
        ssid: ssid.try_into().unwrap(),
        password: password.try_into().unwrap(),
        auth_method: if password.is_empty() {
            AuthMethod::None
        } else {
            AuthMethod::WPA2Personal
        },
        pmf_cfg: PmfConfiguration::Capable { required: false },
        ..Default::default()
    }))?;

    wifi.start()?;

    // === ХАКИ ДЛЯ СТАБИЛИЗАЦИИ РАДИОМОДУЛЯ SUPERMINI ===
    log::info!("Применяем патчи стабильности для чипа C3...");
    unsafe {
        // 1. Снижаем мощность передатчика до 10 dBm, чтобы не просаживать LDO платки
        sys::esp_wifi_set_max_tx_power(40);

        // 2. Жестко ВЫКЛЮЧАЕМ энергосбережение (0 соответсвует WIFI_PS_NONE в C-коде)
        // Теперь модем не будет засыпать во время хэндшейка
        sys::esp_wifi_set_ps(0);
    }
    // ==================================================

    log::info!("Wi-Fi запущен. Начинаем штурм сети: {}", ssid);

    loop {
        log::info!("Попытка подключения...");

        match wifi.connect() {
            Ok(_) => {
                log::info!("Аутентификация пройдена! Запрашиваем IP у роутера...");
                match wifi.wait_netif_up() {
                    Ok(_) => {
                        let ip_info = wifi.wifi().sta_netif().get_ip_info()?;
                        log::info!("----------------------------------------");
                        log::info!("УСПЕШНО ПОДКЛЮЧЕНО!");
                        log::info!("IP-адрес платы: {}", ip_info.ip);
                        log::info!("----------------------------------------");
                        break;
                    }
                    Err(e) => log::warn!("Роутер не выдал IP. Причина: {:?}", e),
                }
            }
            Err(e) => log::warn!("Сброс (auth->init) или Таймаут. Причина: {:?}", e),
        }

        log::info!("Остываем 2 секунды перед новой попыткой...");
        let _ = wifi.disconnect();
        thread::sleep(Duration::from_secs(2));
    }

    Ok(wifi)
}
