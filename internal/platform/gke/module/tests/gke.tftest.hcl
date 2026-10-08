# Tests for the GKE module. They run with a mock Google provider, so they need
# no Google Cloud account: `tofu test` from the module directory.

mock_provider "google" {
  mock_resource "google_container_cluster" {
    defaults = {
      id             = "projects/acme-dev/locations/us-central1-a/clusters/dev"
      endpoint       = "34.1.2.3"
      master_version = "1.34.1-gke.1200"
      master_auth = {
        cluster_ca_certificate = "Q0EgY2VydGlmaWNhdGU="
      }
    }
  }
}

variables {
  name     = "dev"
  project  = "acme-dev"
  location = "us-central1-a"
  node_pools = [
    { name = "default", machine_type = "e2-standard-2", count = 1 },
  ]
  labels = {
    managed-by    = "tuggy"
    tuggy-cluster = "dev"
  }
}

run "zonal_cluster_with_one_pool" {
  command = apply

  assert {
    condition     = google_container_cluster.this.name == "dev" && google_container_cluster.this.location == "us-central1-a"
    error_message = "cluster name or location is wrong"
  }
  assert {
    condition     = google_container_cluster.this.remove_default_node_pool
    error_message = "the default node pool must be removed; pools are managed separately"
  }
  assert {
    condition     = google_container_cluster.this.release_channel[0].channel == "REGULAR"
    error_message = "release channel should default to REGULAR"
  }
  assert {
    condition     = google_container_cluster.this.workload_identity_config[0].workload_pool == "acme-dev.svc.id.goog"
    error_message = "Workload Identity must use the project's pool"
  }
  assert {
    condition     = google_container_cluster.this.deletion_protection == false
    error_message = "deletion protection should default to off so tuggy can delete the cluster"
  }
  assert {
    condition     = length(google_container_node_pool.this) == 1 && google_container_node_pool.this["default"].node_count == 1
    error_message = "expected one pool with one node"
  }
  assert {
    condition     = google_container_node_pool.this["default"].node_config[0].machine_type == "e2-standard-2"
    error_message = "machine type not passed to the pool"
  }
  assert {
    condition     = google_container_node_pool.this["default"].node_config[0].spot == false
    error_message = "spot should default to false"
  }
  assert {
    condition     = google_container_node_pool.this["default"].node_config[0].resource_labels["managed-by"] == "tuggy"
    error_message = "node VMs must carry the tuggy labels for cost attribution"
  }
  assert {
    condition     = output.endpoint == "https://34.1.2.3"
    error_message = "endpoint output must be an https URL"
  }
  assert {
    condition     = output.ca_certificate == "Q0EgY2VydGlmaWNhdGU="
    error_message = "ca_certificate output is wrong"
  }
  assert {
    condition     = output.name == "dev" && output.project == "acme-dev" && output.kubernetes_version == "1.34.1-gke.1200"
    error_message = "name, project or kubernetes_version output is wrong"
  }
}

run "regional_cluster_with_several_pools" {
  command = plan

  variables {
    location        = "us-central1"
    release_channel = "STABLE"
    node_pools = [
      { name = "default", machine_type = "e2-standard-2", count = 2 },
      { name = "batch", machine_type = "e2-standard-8", count = 0, spot = true, disk_size_gb = 200 },
    ]
  }

  assert {
    condition     = google_container_cluster.this.location == "us-central1"
    error_message = "a region should give a regional cluster"
  }
  assert {
    condition     = length(google_container_node_pool.this) == 2
    error_message = "expected two pools"
  }
  assert {
    condition     = google_container_node_pool.this["batch"].node_config[0].spot && google_container_node_pool.this["batch"].node_config[0].disk_size_gb == 200
    error_message = "spot and disk size not passed to the batch pool"
  }
  assert {
    condition     = google_container_node_pool.this["batch"].node_count == 0
    error_message = "a pool may have zero nodes"
  }
}

run "pinned_version_and_custom_network" {
  command = plan

  variables {
    kubernetes_version = "1.34"
    network            = "tuggy-vpc"
    subnetwork         = "tuggy-subnet"
  }

  assert {
    condition     = google_container_cluster.this.min_master_version == "1.34"
    error_message = "kubernetes_version not passed as min_master_version"
  }
  assert {
    condition     = google_container_cluster.this.network == "tuggy-vpc" && google_container_cluster.this.subnetwork == "tuggy-subnet"
    error_message = "network or subnetwork not passed"
  }
}

run "rejects_invalid_name" {
  command = plan
  variables { name = "My_Cluster" }
  expect_failures = [var.name]
}

run "rejects_long_name" {
  command = plan
  variables { name = "a234567890123456789012345678901234567890x" }
  expect_failures = [var.name]
}

run "rejects_unknown_release_channel" {
  command = plan
  variables { release_channel = "FAST" }
  expect_failures = [var.release_channel]
}

run "rejects_no_pools" {
  command = plan
  variables { node_pools = [] }
  expect_failures = [var.node_pools]
}

run "rejects_duplicate_pool_names" {
  command = plan
  variables {
    node_pools = [
      { name = "default", machine_type = "e2-small", count = 1 },
      { name = "default", machine_type = "e2-small", count = 1 },
    ]
  }
  expect_failures = [var.node_pools]
}

run "rejects_negative_count" {
  command = plan
  variables {
    node_pools = [{ name = "default", machine_type = "e2-small", count = -1 }]
  }
  expect_failures = [var.node_pools]
}
