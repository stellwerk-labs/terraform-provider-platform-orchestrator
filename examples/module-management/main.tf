terraform {
  required_providers {
    platform-orchestrator = {
      source = "stellwerk-labs/platform-orchestrator"
    }
  }
}

# Configure PO_API_URL, PO_ORG_ID and PO_AUTH_TOKEN outside this file.
provider "platform-orchestrator" {}

resource "platform-orchestrator_resource_type" "service_labels" {
  id              = "service-labels"
  output_schema   = jsonencode({ type = "object" })
  module_contract = jsonencode({ type = "object", required = ["output_schema"] })
  deletion_policy = "retain"
}

resource "platform-orchestrator_module_catalogue_entry" "service_labels" {
  id              = "service-labels"
  display_name    = "Service Labels"
  resource_type   = platform-orchestrator_resource_type.service_labels.id
  status          = "active"
  deletion_reason = "Service Labels example removed from Terraform"
}

resource "platform-orchestrator_module_version" "release" {
  module_id        = platform-orchestrator_module_catalogue_entry.service_labels.id
  semantic_version = "1.0.0"
  definition = jsonencode({
    output_schema      = jsondecode(platform-orchestrator_resource_type.service_labels.output_schema)
    module_source      = "inline"
    module_source_code = "output \"labels\" { value = { managed_by = \"stellwerk\" } }"
    module_inputs      = {}
    module_params      = {}
    provider_mapping   = {}
    dependencies       = {}
    coprovisioned      = []
  })
}

# An operation receipt records this explicit promotion once. Freeze the observed
# revision in its input; never reference the mutable lifecycle resource_version.
resource "platform-orchestrator_module_version_lifecycle_transaction" "release" {
  transaction = jsonencode({
    transitions = [{
      module_id                 = platform-orchestrator_module_catalogue_entry.service_labels.id
      module_version_id         = platform-orchestrator_module_version.release.id
      expected_resource_version = 1
      action                    = "promote"
      reason                    = "Publish the initial Service Labels release"
    }]
  })
}
