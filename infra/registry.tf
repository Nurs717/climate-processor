resource "google_artifact_registry_repository" "climate" {
  repository_id = "climate"
  location      = var.region
  format        = "DOCKER"
  description   = "Docker images for climate-processor"

  # Keep the registry small: the five newest versions, everything else is deleted.
  cleanup_policies {
    id     = "keep-recent-versions"
    action = "KEEP"

    most_recent_versions {
      keep_count = 5
    }
  }

  cleanup_policies {
    id     = "delete-rest"
    action = "DELETE"

    condition {
      tag_state = "ANY"
    }
  }

  depends_on = [google_project_service.enabled]
}
