terraform {
  required_version = ">= 1.6"
}

provider "google" {
  project = "new-project"
  region  = "us-central1"
}
