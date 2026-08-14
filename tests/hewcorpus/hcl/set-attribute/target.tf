terraform {
  required_version = ">= 1.6"
}

provider "google" {
  project = "old-project"
  region  = "us-central1"
}
