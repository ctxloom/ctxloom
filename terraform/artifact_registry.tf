# Artifact Registry for container images

resource "google_artifact_registry_repository" "mlcm" {
  provider = google-beta

  location      = var.region
  repository_id = "mlcm"
  description   = "Container images for MLCM Fragment Server"
  format        = "DOCKER"

  labels = merge(var.labels, {
    environment = var.environment
    managed-by  = "terraform"
  })

  depends_on = [google_project_service.apis]
}

# Output the repository URL for docker push
output "artifact_registry_url" {
  description = "Artifact Registry URL for pushing images"
  value       = "${var.region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.mlcm.repository_id}"
}
