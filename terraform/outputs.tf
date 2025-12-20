# Terraform outputs for MLCM Fragment Server deployment

output "service_url" {
  description = "URL of the Cloud Run service"
  value       = google_cloud_run_v2_service.main.uri
}

output "service_name" {
  description = "Name of the Cloud Run service"
  value       = google_cloud_run_v2_service.main.name
}

output "service_account_email" {
  description = "Email of the service account"
  value       = google_service_account.cloud_run.email
}

output "project_id" {
  description = "GCP project ID"
  value       = var.project_id
}

output "region" {
  description = "Deployment region"
  value       = var.region
}

output "container_image" {
  description = "Container image URL"
  value       = local.container_image
}

output "docker_push_command" {
  description = "Command to push a new image"
  value       = "docker push ${var.region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.mlcm.repository_id}/${var.service_name}:latest"
}

output "docker_build_command" {
  description = "Command to build and tag the image"
  value       = "docker build -t ${var.region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.mlcm.repository_id}/${var.service_name}:latest -f server/Dockerfile ."
}
