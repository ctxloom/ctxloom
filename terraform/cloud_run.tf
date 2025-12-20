# Cloud Run service for MLCM Fragment Server

locals {
  # Use provided image or build from Artifact Registry
  container_image = var.container_image != "" ? var.container_image : "${var.region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.mlcm.repository_id}/${var.service_name}:latest"
}

resource "google_cloud_run_v2_service" "main" {
  provider = google-beta

  name     = var.service_name
  location = var.region

  template {
    service_account = google_service_account.cloud_run.email

    scaling {
      min_instance_count = var.min_instances
      max_instance_count = var.max_instances
    }

    containers {
      image = local.container_image

      # HTTP port (REST gateway)
      ports {
        container_port = 8080
      }

      resources {
        limits = {
          cpu    = var.cpu
          memory = var.memory
        }
      }

      # Environment variables
      env {
        name  = "GCP_PROJECT"
        value = var.project_id
      }

      env {
        name  = "STORAGE_TYPE"
        value = "firestore"
      }

      env {
        name  = "HTTP_PORT"
        value = "8080"
      }

      env {
        name  = "GRPC_PORT"
        value = "50051"
      }

      env {
        name  = "RATE_LIMIT"
        value = var.enable_rate_limiting ? "true" : "false"
      }

      # Health checks
      startup_probe {
        http_get {
          path = "/v1/fragments"
          port = 8080
        }
        initial_delay_seconds = 5
        timeout_seconds       = 3
        period_seconds        = 5
        failure_threshold     = 3
      }

      liveness_probe {
        http_get {
          path = "/v1/fragments"
          port = 8080
        }
        timeout_seconds   = 3
        period_seconds    = 30
        failure_threshold = 3
      }
    }

    labels = merge(var.labels, {
      environment = var.environment
      managed-by  = "terraform"
    })
  }

  traffic {
    type    = "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST"
    percent = 100
  }

  labels = merge(var.labels, {
    environment = var.environment
    managed-by  = "terraform"
  })

  depends_on = [
    google_project_service.apis,
    google_firestore_database.main,
  ]
}
