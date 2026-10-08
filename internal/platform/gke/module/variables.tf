# Inputs for a GKE cluster. tuggy's gke platform fills these from the spec
# file or flags (see internal/platform/gke). A Go struct mirrors this file and
# a contract test keeps the two in sync.

variable "name" {
  description = "Cluster name."
  type        = string

  validation {
    condition     = can(regex("^[a-z]([-a-z0-9]{0,38}[a-z0-9])?$", var.name))
    error_message = "name must be 1-40 lowercase letters, digits or '-', start with a letter and end with a letter or digit."
  }
}

variable "project" {
  description = "Google Cloud project ID."
  type        = string
}

variable "location" {
  description = "A zone (e.g. us-central1-a) for a zonal cluster, or a region (e.g. us-central1) for a regional one."
  type        = string
}

variable "release_channel" {
  description = "GKE release channel, which controls automatic upgrades."
  type        = string
  default     = "REGULAR"

  validation {
    condition     = contains(["RAPID", "REGULAR", "STABLE", "EXTENDED", "UNSPECIFIED"], var.release_channel)
    error_message = "release_channel must be RAPID, REGULAR, STABLE, EXTENDED or UNSPECIFIED."
  }
}

variable "kubernetes_version" {
  description = "Minimum control plane version, such as \"1.34\". Null uses the release channel's default."
  type        = string
  default     = null
}

variable "network" {
  description = "VPC network name."
  type        = string
  default     = "default"
}

variable "subnetwork" {
  description = "Subnetwork name. Null lets GKE choose the network's subnet for the region."
  type        = string
  default     = null
}

variable "node_pools" {
  description = "Worker node pools."
  type = list(object({
    name         = string
    machine_type = string
    count        = number
    spot         = optional(bool, false)
    disk_size_gb = optional(number)
  }))

  validation {
    condition     = length(var.node_pools) > 0
    error_message = "at least one node pool is required."
  }

  validation {
    condition     = length(distinct([for p in var.node_pools : p.name])) == length(var.node_pools)
    error_message = "node pool names must be unique."
  }

  validation {
    condition     = alltrue([for p in var.node_pools : p.count >= 0])
    error_message = "node pool counts must not be negative."
  }
}

variable "labels" {
  description = "Labels applied to every resource, for example managed-by = tuggy."
  type        = map(string)
  default     = {}
}

variable "impersonate_service_account" {
  description = "Service account email to impersonate. Null uses the caller's credentials."
  type        = string
  default     = null
}

variable "deletion_protection" {
  description = "Block deletion of the cluster until this is set to false."
  type        = bool
  default     = false
}
