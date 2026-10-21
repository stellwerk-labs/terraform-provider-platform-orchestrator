package provider

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/stretchr/testify/require"
)

// Exercise the real Provider protocol and Terraform/OpenTofu planning without
// publishing anything. Port 9 has no API fixture: any network dependency fails.
func TestAccModuleVersionContractPlanLocal(t *testing.T) {
	var steps []resource.TestStep
	for _, declaration := range []string{"", `{}`, `{"type":"object","properties":{"endpoint":{"type":"string"}}}`} {
		definition := map[string]any{
			"module_source": "inline", "module_source_code": `output "endpoint" { value = "x" }`,
			"module_inputs": map[string]any{}, "module_params": map[string]any{},
			"provider_mapping": map[string]any{}, "dependencies": map[string]any{}, "coprovisioned": []any{},
		}
		if declaration != "" {
			var schema map[string]any
			require.NoError(t, json.Unmarshal([]byte(declaration), &schema))
			definition["output_schema"] = schema
		}
		encoded, err := json.Marshal(definition)
		require.NoError(t, err)
		steps = append(steps, resource.TestStep{
			PlanOnly: true, ExpectNonEmptyPlan: true,
			Config: fmt.Sprintf(`
provider "platform-orchestrator" {
  api_url = "http://127.0.0.1:9"
  org_id = "local-plan-only"
  auth_token = "not-a-real-credential"
}
resource "platform-orchestrator_module_version" "declaration" {
  module_id = "test"
  semantic_version = "1.0.0"
  definition = %q
}
`, string(encoded)),
		})
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    steps,
	})
}
