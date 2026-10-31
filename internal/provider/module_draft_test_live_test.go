package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// This test uses a real API, IAM and Runner. Its dedicated fixture supplies an
// unused Module, its Resource Type and an Environment with a scoped Module Rule.
// The fixture owns their cleanup; immutable capture history survives TF destroy.
func TestAccModuleDraftTestLiveLifecycle(t *testing.T) {
	if os.Getenv("MVM_ACC_MODULE_ID") == "" {
		t.Skip("requires a dedicated live Module/Environment fixture; see docs/testing-module-drafts.md")
	}
	for _, name := range []string{"MVM_ACC_MODULE_ID", "MVM_ACC_RESOURCE_TYPE", "MVM_ACC_PROJECT_ID", "MVM_ACC_ENV_ID", PO_API_URL_ENV_VAR} {
		checkEnvVar(t, name)
	}
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "stellwerk-labs")
	testAccPreCheck(t)
	client := NewPlatformOrchestratorControlPlaneClient(t)
	module, resourceType := os.Getenv("MVM_ACC_MODULE_ID"), os.Getenv("MVM_ACC_RESOURCE_TYPE")
	project, environment, org := os.Getenv("MVM_ACC_PROJECT_ID"), os.Getenv("MVM_ACC_ENV_ID"), os.Getenv(PO_ORG_ID_ENV_VAR)
	authoringKey := uuid.NewString()
	config := func(label string, revision int) string {
		return fmt.Sprintf(`
terraform {
 required_providers {
  platform-orchestrator = { source = "registry.terraform.io/stellwerk-labs/platform-orchestrator" }
 }
}
provider "platform-orchestrator" {}
resource "platform-orchestrator_module_draft" "test" {
 module_id = %q
 authoring_key = %q
 semantic_version = "1.0.0"
 reason = "Review unchanged intended SemVer"
 definition = jsonencode({
  resource_type_version = 1
  module_source = "inline"
  module_source_code = %q
  module_inputs = {}
  module_params = {}
  provider_mapping = {}
  dependencies = {}
  coprovisioned = []
  output_schema = {type = "object", properties = {name = {type = "string"}}, required = ["name"]}
 })
}
resource "platform-orchestrator_module_draft_test" "reviewed" {
 module_id = %q
 draft_id = platform-orchestrator_module_draft.test.id
 project_id = %q
 env_id = %q
 capture_key = "live-reviewed-revision"
 expected_resource_version = %d
 reason = "Capture exact reviewed source"
}
resource "platform-orchestrator_deployment" "test" {
 project_id = %q
 env_id = %q
 draft_test_snapshots = {
  (platform-orchestrator_module_draft_test.reviewed.module_uuid) = platform-orchestrator_module_draft_test.reviewed.id
 }
 manifest = jsonencode({shared = {}, workloads = {check = {
  resources = {message = {type = %q}}
  outputs = {TEST_VALUE = "$${resources.message.outputs.name}"}
 }}})
}
`, module, authoringKey, fmt.Sprintf("output \"name\" { value = %q }", label), module, project, environment, revision, project, environment, resourceType)
	}
	var firstDraft, firstSnapshot, firstDeployment string
	check := func(revision int, output string, changed bool) resource.TestCheckFunc {
		return func(state *terraform.State) error {
			draft := state.RootModule().Resources["platform-orchestrator_module_draft.test"].Primary
			snapshot := state.RootModule().Resources["platform-orchestrator_module_draft_test.reviewed"].Primary
			deployment := state.RootModule().Resources["platform-orchestrator_deployment.test"].Primary
			if draft.Attributes["resource_version"] != fmt.Sprint(revision) || draft.Attributes["semantic_version"] != "1.0.0" {
				return fmt.Errorf("Draft must advance to revision %d while keeping intended SemVer", revision)
			}
			if firstDraft == "" {
				firstDraft, firstSnapshot, firstDeployment = draft.ID, snapshot.ID, deployment.ID
			}
			if draft.ID != firstDraft {
				return fmt.Errorf("editing must preserve the stable Draft identity")
			}
			if changed {
				if snapshot.ID == firstSnapshot || deployment.ID == firstDeployment {
					return fmt.Errorf("explicit review must capture and execute a new immutable snapshot")
				}
			} else if snapshot.ID != firstSnapshot || deployment.ID != firstDeployment {
				return fmt.Errorf("authoring alone must leave captured material and deployment unchanged")
			}
			var outputs map[string]map[string]string
			if err := json.Unmarshal([]byte(deployment.Attributes["outputs"]), &outputs); err != nil {
				return err
			}
			if deployment.Attributes["status"] != "succeeded" || outputs["check"]["TEST_VALUE"] != output {
				return fmt.Errorf("real Runner must complete with reviewed output %q", output)
			}
			retained, err := client.GetModuleDraftTestSnapshotWithResponse(t.Context(), org, project, environment, module, uuid.MustParse(firstSnapshot))
			if err != nil {
				return err
			}
			if retained.StatusCode() != http.StatusOK || retained.JSON200 == nil || retained.JSON200.DraftRevision != 1 {
				return fmt.Errorf("first immutable capture must remain readable at revision one")
			}
			versions, err := client.ListModuleVersionsWithResponse(t.Context(), org, module, nil)
			if err != nil {
				return err
			}
			if versions.StatusCode() != http.StatusOK || versions.JSON200 == nil || len(versions.JSON200.Items) != 0 {
				return fmt.Errorf("saving and executing Drafts must not publish any Module Version")
			}
			return nil
		}
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config("A", 1), Check: check(1, "A", false)},
			{Config: config("B", 1), Check: check(2, "A", false)},
			{Config: config("B", 2), Check: check(2, "B", true)},
		},
	})
}
