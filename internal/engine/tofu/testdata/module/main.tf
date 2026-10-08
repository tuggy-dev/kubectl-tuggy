# A tiny module for engine tests. It needs no cloud account: the random
# provider only generates values locally.
terraform {
  required_providers {
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

variable "name" {
  type = string
}

variable "length" {
  type    = number
  default = 2

  validation {
    condition     = var.length >= 1
    error_message = "length must be at least 1."
  }
}

# A complex variable, to check that lists of objects reach OpenTofu intact.
variable "pools" {
  type = list(object({
    name  = string
    count = number
  }))
  default = []
}

resource "random_pet" "this" {
  prefix = var.name
  length = var.length
}

output "pet" {
  value = random_pet.this.id
}

output "name" {
  value = var.name
}

output "pool_names" {
  value = join(",", [for p in var.pools : "${p.name}=${p.count}"])
}
