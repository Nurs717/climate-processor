terraform {
  required_version = ">= 1.7"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.0"
    }
  }

  # The state lives in a versioned bucket outside the repository (created once by hand, see infra/README.md).
  backend "gcs" {
    bucket = "project-86bdbd1e-0967-47e9-a65-tfstate"
    prefix = "climate"
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
  zone    = var.zone
}
