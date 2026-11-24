variable "agent_public_key" {
  type        = string
  description = "Public Ed25519 Agent authentication PEM. Keep its private key at the site."
}

variable "approved_inventory_file" {
  type        = string
  description = "Path to the public rollout-catalogue JSON export envelope, including declarations and inner native_execution."
}

locals {
  catalogue = jsondecode(file(var.approved_inventory_file))
}

resource "platform-orchestrator_native_agent_runner" "example" {
  id          = "example-native-agent"
  description = "Native logical execution pool"
  runner_configuration = {
    key                 = var.agent_public_key
    max_parallel_jobs   = 4
    supported_protocols = local.catalogue.supported_protocols
    capabilities        = local.catalogue.capabilities
    native_execution    = jsonencode(local.catalogue.native_execution)
  }
  state_storage_configuration = {
    type             = "s3"
    s3_configuration = { bucket = "replace-with-state-bucket" }
  }
  lifecycle {
    prevent_destroy = true
  }
}

output "runner_registration_uuid" {
  value = platform-orchestrator_native_agent_runner.example.uuid
}
