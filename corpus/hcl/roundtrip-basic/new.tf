terraform {
  required_version = ">= 1.7"
}

provider "google" {
  project = "old-project"
  region  = "us-central1"
}
