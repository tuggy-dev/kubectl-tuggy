terraform {
  required_version = ">= 1.8"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.6"
    }
  }
}

provider "google" {
  project = var.project

  # Optional: act as a service account the user is allowed to impersonate,
  # as the Sarayo demo did. Null uses the caller's own credentials.
  impersonate_service_account = var.impersonate_service_account

  # Applied to every resource that supports labels.
  default_labels = var.labels
}
