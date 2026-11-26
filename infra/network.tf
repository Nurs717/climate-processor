# The broker IP is inside the TLS certificate and in the firmware, so it must never change.
resource "google_compute_address" "climate_ip" {
  name   = "climate-ip"
  region = var.region

  lifecycle {
    prevent_destroy = true
  }

  depends_on = [google_project_service.enabled]
}

# MQTT over TLS for the sensor.
resource "google_compute_firewall" "allow_mqtts" {
  name          = "allow-mqtts"
  network       = "default"
  direction     = "INGRESS"
  source_ranges = ["0.0.0.0/0"]
  target_tags   = ["mqtt"]

  allow {
    protocol = "tcp"
    ports    = ["8883"]
  }
}

# HTTPS for the Caddy reverse proxy in front of the API. 443 serves the app; 80 answers the Let's Encrypt
# HTTP-01 challenge and redirects browsers to HTTPS. UDP 443 (HTTP/3) stays closed: browsers fall back to HTTP/2.
resource "google_compute_firewall" "allow_web" {
  name          = "allow-web"
  network       = "default"
  direction     = "INGRESS"
  source_ranges = ["0.0.0.0/0"]
  target_tags   = ["web"]

  allow {
    protocol = "tcp"
    ports    = ["80", "443"]
  }
}

# SSH only from Google's IAP range, so port 22 is closed to the internet.
resource "google_compute_firewall" "allow_iap_ssh" {
  name          = "allow-iap-ssh"
  network       = "default"
  direction     = "INGRESS"
  source_ranges = ["35.235.240.0/20"]

  allow {
    protocol = "tcp"
    ports    = ["22"]
  }
}
