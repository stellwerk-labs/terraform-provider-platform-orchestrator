package provider

import (
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"testing"
)

func TestAccResourceTypeVersionPlanLocal(t *testing.T) {
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: []resource.TestStep{{PlanOnly: true, ExpectNonEmptyPlan: true, Config: `
provider "platform-orchestrator" {
 api_url = "http://127.0.0.1:9"
 org_id = "local-plan-only"
 auth_token = "not-a-real-credential"
}
resource "platform-orchestrator_resource_type_version" "storage" {
 resource_type_id = "storage"
 version = 2
 expected_resource_version = 1
 output_schema = jsonencode({type = "object", properties = {endpoint = {type = "string"}}})
 reason = "Add a reviewed endpoint contract"
}
`}}})
}
