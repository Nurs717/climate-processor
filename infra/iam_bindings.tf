locals {
  vm_member       = "serviceAccount:${google_service_account.climate_vm.email}"
  deployer_member = "serviceAccount:${google_service_account.github_deployer.email}"

  # Project-level roles of the deployer: look up the VM and open an IAP tunnel to it.
  deployer_project_roles = toset([
    "roles/compute.viewer",
    "roles/iap.tunnelResourceAccessor",
  ])
}

# google_*_iam_member is additive: it never touches bindings it does not describe.
resource "google_project_iam_member" "deployer" {
  for_each = local.deployer_project_roles
  project  = var.project_id
  role     = each.value
  member   = local.deployer_member
}

# The VM may read exactly the application secrets and nothing else.
resource "google_secret_manager_secret_iam_member" "vm_accessor" {
  for_each  = google_secret_manager_secret.app
  secret_id = each.value.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = local.vm_member
}

# The VM pulls images, CI pushes them.
resource "google_artifact_registry_repository_iam_member" "vm_reader" {
  location   = google_artifact_registry_repository.climate.location
  repository = google_artifact_registry_repository.climate.name
  role       = "roles/artifactregistry.reader"
  member     = local.vm_member
}

resource "google_artifact_registry_repository_iam_member" "deployer_writer" {
  location   = google_artifact_registry_repository.climate.location
  repository = google_artifact_registry_repository.climate.name
  role       = "roles/artifactregistry.writer"
  member     = local.deployer_member
}

# deploy.sh runs on the VM over SSH, so the deployer must be allowed to act as the VM's account.
resource "google_service_account_iam_member" "deployer_acts_as_vm" {
  service_account_id = google_service_account.climate_vm.name
  role               = "roles/iam.serviceAccountUser"
  member             = local.deployer_member
}

# OS Login with sudo on this one VM: deploy.sh needs root to run docker compose.
resource "google_compute_instance_iam_member" "deployer_os_admin_login" {
  project       = google_compute_instance.climate_vm.project
  zone          = google_compute_instance.climate_vm.zone
  instance_name = google_compute_instance.climate_vm.name
  role          = "roles/compute.osAdminLogin"
  member        = local.deployer_member
}
