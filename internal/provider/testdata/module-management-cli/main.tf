terraform {
  required_providers {
    platform-orchestrator = {
      source = "registry.terraform.io/stellwerk-labs/platform-orchestrator"
    }
  }
}

variable "catalogue_id" {
  type = string
}

variable "catalogue_status" {
  type    = string
  default = "active"
}

provider "platform-orchestrator" {}

resource "platform-orchestrator_provider" "release" {
  id                 = var.catalogue_id
  provider_type      = "random"
  source             = "hashicorp/random"
  version_constraint = "= 3.7.2"
  configuration      = jsonencode({})
  deletion_policy    = "retain"
}

resource "platform-orchestrator_resource_type" "release" {
  id              = var.catalogue_id
  output_schema   = jsonencode({ type = "object" })
  deletion_policy = "retain"
}

resource "platform-orchestrator_module_catalogue_entry" "release" {
  id                = var.catalogue_id
  description       = "Real Terraform and OpenTofu acceptance fixture"
  resource_type     = platform-orchestrator_resource_type.release.id
  status            = var.catalogue_status
  transition_reason = "Review client lifecycle"
  deletion_reason   = "Retain immutable client acceptance history"
}

resource "platform-orchestrator_module_version" "release" {
  module_id        = platform-orchestrator_module_catalogue_entry.release.id
  semantic_version = "1.0.0"
  definition = jsonencode({
    module_source      = "inline"
    module_source_code = "output \"labels\" { value = { managed_by = \"stellwerk\" } }"
    module_inputs      = {}
    module_params      = {}
    provider_mapping   = { random = "random.${platform-orchestrator_provider.release.id}" }
    dependencies       = {}
    coprovisioned      = []
  })
}

resource "platform-orchestrator_module_version_lifecycle_transaction" "release" {
  transaction = jsonencode({
    transitions = [{
      module_id                 = platform-orchestrator_module_catalogue_entry.release.id
      module_version_id         = platform-orchestrator_module_version.release.id
      expected_resource_version = 1
      action                    = "promote"
      reason                    = "Publish the client acceptance release"
    }]
  })
}
