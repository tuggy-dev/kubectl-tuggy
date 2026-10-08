resource "google_container_cluster" "this" {
  name     = var.name
  location = var.location

  # Node pools are managed separately below, so they can be changed without
  # replacing the cluster. GKE still needs a default pool at creation, which
  # is removed straight away (as in the Sarayo demo).
  remove_default_node_pool = true
  initial_node_count       = 1

  network             = var.network
  subnetwork          = var.subnetwork
  min_master_version  = var.kubernetes_version
  deletion_protection = var.deletion_protection

  release_channel {
    channel = var.release_channel
  }

  # Users sign in with their Google identity (gke-gcloud-auth-plugin), never
  # with legacy client certificates.
  master_auth {
    client_certificate_config {
      issue_client_certificate = false
    }
  }

  # Lets workloads use Google service accounts without node-wide keys.
  workload_identity_config {
    workload_pool = "${var.project}.svc.id.goog"
  }
}

resource "google_container_node_pool" "this" {
  for_each = { for pool in var.node_pools : pool.name => pool }

  name       = each.key
  cluster    = google_container_cluster.this.id
  location   = var.location
  node_count = each.value.count

  management {
    auto_repair  = true
    auto_upgrade = true
  }

  node_config {
    machine_type = each.value.machine_type
    spot         = each.value.spot
    disk_size_gb = each.value.disk_size_gb
    oauth_scopes = ["https://www.googleapis.com/auth/cloud-platform"]

    # Label the node VMs too, so their cost is attributed to the cluster.
    resource_labels = var.labels

    workload_metadata_config {
      mode = "GKE_METADATA"
    }
  }
}
