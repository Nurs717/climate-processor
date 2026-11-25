# The runtime VM: Mosquitto (MQTTS) + the Go service, started by deploy.sh.
# Imported from the live instance; the static IP and the service account are
# referenced from their own resources instead of being hard-coded.
resource "google_compute_instance" "climate_vm" {
  name         = "climate-vm"
  machine_type = "e2-micro" # Free Tier eligible in us-central1
  zone         = var.zone
  tags         = ["mqtt"] # matches the allow-mqtts firewall rule

  boot_disk {
    auto_delete = true
    device_name = "persistent-disk-0"
    source      = "https://www.googleapis.com/compute/v1/projects/${var.project_id}/zones/${var.zone}/disks/climate-vm"

    initialize_params {
      image = "https://www.googleapis.com/compute/v1/projects/ubuntu-os-cloud/global/images/ubuntu-2404-noble-amd64-v20261105"
      size  = 30
      type  = "pd-standard"
    }
  }

  network_interface {
    network    = "https://www.googleapis.com/compute/v1/projects/${var.project_id}/global/networks/default"
    subnetwork = "https://www.googleapis.com/compute/v1/projects/${var.project_id}/regions/${var.region}/subnetworks/default"

    access_config {
      # The reserved address is pinned in the broker certificate SAN and in the
      # firmware, so it must never change.
      nat_ip = google_compute_address.climate_ip.address
    }
  }

  service_account {
    email  = google_service_account.climate_vm.email
    scopes = ["https://www.googleapis.com/auth/cloud-platform"] # access is limited by IAM roles
  }

  shielded_instance_config {
    enable_integrity_monitoring = true
    enable_secure_boot          = false
    enable_vtpm                 = true
  }

  lifecycle {
    # Losing this VM means losing the broker certificates and the mosquitto
    # password file that live on its disk.
    prevent_destroy = true

    # Ubuntu image bumps would otherwise force a replacement of the boot disk.
    ignore_changes = [boot_disk[0].initialize_params]
  }
}
