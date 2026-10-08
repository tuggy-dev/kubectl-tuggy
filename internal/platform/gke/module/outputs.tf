# tuggy stores these in the cluster's record and uses endpoint and
# ca_certificate to build the kubeconfig.

output "name" {
  value = google_container_cluster.this.name
}

output "location" {
  value = google_container_cluster.this.location
}

output "project" {
  value = var.project
}

output "endpoint" {
  description = "API server URL."
  value       = "https://${google_container_cluster.this.endpoint}"
}

output "ca_certificate" {
  description = "Base64-encoded certificate of the cluster's certificate authority."
  value       = google_container_cluster.this.master_auth[0].cluster_ca_certificate
}

output "kubernetes_version" {
  value = google_container_cluster.this.master_version
}
