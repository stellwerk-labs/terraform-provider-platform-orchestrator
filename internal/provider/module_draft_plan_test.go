package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccModuleDraftPlanLocal(t *testing.T) {
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{PlanOnly: true, ExpectNonEmptyPlan: true, Config: `
provider "platform-orchestrator" {
  api_url = "http://127.0.0.1:9"
  org_id = "local-plan-only"
  auth_token = "not-a-real-credential"
}
resource "platform-orchestrator_module_draft" "storage" {
  module_id = "storage"
  authoring_key = "storage-1.2.0-authoring"
  semantic_version = "1.2.0"
  reason = "Try an implementation without publishing"
  status = "open"
  definition = jsonencode({
    module_source = "inline"
    module_source_code = "output \"name\" { value = \"example\" }"
    module_inputs = {}
    module_params = {}
    provider_mapping = {}
    dependencies = {}
    coprovisioned = []
    resource_type_version = 2
    output_schema = {}
  })
}
`}},
	})
}
