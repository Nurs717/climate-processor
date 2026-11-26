# Terraform manages only the secrets themselves, never their values: the values are added with
# `gcloud secrets versions add ...`, so they never appear in the state or in git.
locals {
  secrets = [
    "climate-mqtt-password",
    "climate-telegram-bot-token",
    "climate-telegram-admin-chat-ids",
    # Client secret of "Log In With Telegram" (the owner sign-in).
    "climate-telegram-oidc-client-secret",
  ]
}

resource "google_secret_manager_secret" "app" {
  for_each  = toset(local.secrets)
  secret_id = each.value

  replication {
    auto {}
  }

  depends_on = [google_project_service.enabled]
}
