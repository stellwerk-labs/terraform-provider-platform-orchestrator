package provider

import (
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"testing"
)

func TestAccModuleDraftTestPlanLocal(t *testing.T) {
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "stellwerk-labs")
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: []resource.TestStep{{PlanOnly: true, ExpectNonEmptyPlan: true, Config: `
terraform {
 required_providers {
  platform-orchestrator = { source = "registry.terraform.io/stellwerk-labs/platform-orchestrator" }
 }
}
provider "platform-orchestrator" {
 api_url = "http://127.0.0.1:9"
 org_id = "local-plan-only"
 auth_token = "not-a-real-credential"
}
resource "platform-orchestrator_module_draft" "greeting" {
 module_id = "greeting"
 authoring_key = "first-implementation"
 semantic_version = "1.0.0"
 reason = "Review implementation"
 definition = jsonencode({
  module_source = "inline"
  module_source_code = "output \"name\" { value = \"A\" }"
  module_inputs = {}
  module_params = {}
  provider_mapping = {}
  dependencies = {}
  coprovisioned = []
  output_schema = { name = { type = "string" } }
 })
}
resource "platform-orchestrator_module_draft_test" "reviewed" {
 module_id = "greeting"
 draft_id = platform-orchestrator_module_draft.greeting.id
 project_id = "project"
 env_id = "development"
 capture_key = "first-review"
 expected_resource_version = 1
 reason = "Capture reviewed A"
}
resource "platform-orchestrator_deployment" "test" {
 project_id = "project"
 env_id = "development"
 draft_test_snapshots = {
  (platform-orchestrator_module_draft_test.reviewed.module_uuid) = platform-orchestrator_module_draft_test.reviewed.id
 }
 manifest = jsonencode({workloads = {}, shared = {message = {type = "greeting"}}})
}
`}}})
}
