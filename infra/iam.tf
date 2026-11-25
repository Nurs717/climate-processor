# The VM runs as this account: it may read images and three secrets, nothing else.
resource "google_service_account" "climate_vm" {
  account_id   = "climate-vm"
  display_name = "climate-vm runtime"

  depends_on = [google_project_service.enabled]
}

# GitHub Actions deploys as this account through Workload Identity Federation (no keys).
resource "google_service_account" "github_deployer" {
  account_id   = "github-deployer"
  display_name = "GitHub Actions deployer"

  depends_on = [google_project_service.enabled]
}
