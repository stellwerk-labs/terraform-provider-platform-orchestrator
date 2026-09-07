package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// No artifact download or execution is implied: Core validates the declaration,
// and an omitted external digest remains omitted throughout publication/import.
func TestAccModuleConformanceDeclarationsAndOptionalDigest(t *testing.T) {
	id := fmt.Sprintf("contract-release-%d", time.Now().UnixNano())
	config := fmt.Sprintf(`
resource "platform-orchestrator_resource_type" "contract" {
  id = %[1]q
  output_schema = jsonencode({type = "object", properties = {name = {type = "string"}}})
  module_contract = jsonencode({
    type = "object"
    required = ["module_inputs", "output_schema"]
    properties = {module_inputs = {type = "object", required = ["name"], properties = {name = {type = "string"}}}}
  })
  deletion_policy = "retain"
}
data "platform-orchestrator_resource_type" "contract" {
  id = platform-orchestrator_resource_type.contract.id
}
resource "platform-orchestrator_module_catalogue_entry" "contract" {
  id = %[1]q
  resource_type = platform-orchestrator_resource_type.contract.id
}
resource "platform-orchestrator_module_version" "contract" {
  module_id = platform-orchestrator_module_catalogue_entry.contract.id
  semantic_version = "1.0.0"
  definition = jsonencode({
    semantic_version = "1.0.0"
    module_source = "git::https://github.com/stellwerk-labs/first-deployment//modules/postgres?ref=4b17d97474a6cdb51d4da1b42dd041f6d4e03aee"
    source_revision = "4b17d97474a6cdb51d4da1b42dd041f6d4e03aee"
    output_schema = jsondecode(platform-orchestrator_resource_type.contract.output_schema)
    module_inputs = {name = "contract-release"}
    module_params = {}
    provider_mapping = {}
    dependencies = {}
    coprovisioned = []
  })
}
data "platform-orchestrator_module_version" "contract" {
  module_id = platform-orchestrator_module_catalogue_entry.contract.id
  id = platform-orchestrator_module_version.contract.id
  compare_to = platform-orchestrator_module_version.contract.id
}
`, id)
	client := NewPlatformOrchestratorControlPlaneClient(t)
	resource.Test(t, resource.TestCase{
		PreCheck: func() { testAccPreCheck(t) }, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config, Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttrPair("platform-orchestrator_resource_type.contract", "module_contract", "data.platform-orchestrator_resource_type.contract", "module_contract"),
				resource.TestCheckResourceAttrWith("data.platform-orchestrator_module_version.contract", "definition_json", func(value string) error {
					var definition cp.CoreModuleVersionDetail
					if err := json.Unmarshal([]byte(value), &definition); err != nil {
						return err
					}
					if definition.OutputSchema == nil || (*definition.OutputSchema)["type"] != "object" {
						return fmt.Errorf("Version data source lost its output declaration")
					}
					return nil
				}),
				resource.TestCheckResourceAttrWith("data.platform-orchestrator_module_version.contract", "comparison_json", func(value string) error {
					var comparison cp.ModuleVersionComparison
					if err := json.Unmarshal([]byte(value), &comparison); err != nil {
						return err
					}
					if comparison.OutputSchemaChanged || comparison.Before.OutputSchema == nil || comparison.After.OutputSchema == nil {
						return fmt.Errorf("self-comparison must retain both output declarations without reporting a change")
					}
					return nil
				}),
				resource.TestCheckResourceAttrWith("data.platform-orchestrator_module_version.contract", "lifecycle_events_json", func(value string) error {
					var events []cp.ModuleVersionLifecycleEvent
					if err := json.Unmarshal([]byte(value), &events); err != nil {
						return err
					}
					if len(events) != 1 || events[0].ToStatus != "proposed" {
						return fmt.Errorf("data source must retain the publication lifecycle event")
					}
					return nil
				}),
				func(_ *terraform.State) error {
					response, err := client.GetModuleVersionWithResponse(t.Context(), os.Getenv(PO_ORG_ID_ENV_VAR), id, "1.0.0")
					if err != nil {
						return err
					}
					if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
						return fmt.Errorf("get published declaration: HTTP %d", response.StatusCode())
					}
					if response.JSON200.Version.ArtifactDigest != "" || response.JSON200.Version.VerificationStatus != "unverified" || response.JSON200.OutputSchema == nil {
						return fmt.Errorf("publication lost its declared output or fabricated artifact verification")
					}
					return nil
				},
			)},
			{Config: config, PlanOnly: true},
			{ResourceName: "platform-orchestrator_resource_type.contract", ImportState: true, ImportStateVerify: true, ImportStateId: id, ImportStateVerifyIgnore: []string{"deletion_policy"}},
			{ResourceName: "platform-orchestrator_module_version.contract", ImportState: true, ImportStateVerify: true, ImportStateId: id + "/1.0.0"},
			{Config: config, PlanOnly: true},
		},
	})
}
