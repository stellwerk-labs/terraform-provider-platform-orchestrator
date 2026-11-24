package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	frameworkresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/require"
)

// This journey uses the actual configured Orchestrator, including persistence,
// authorization, lifecycle commands and retained-history deletion checks.
func TestAccModuleManagementLifecycle(t *testing.T) {
	id := fmt.Sprintf("module-release-%d", time.Now().UnixNano())
	versionID := ""
	importedVersionResourceVersion := ""
	client := NewPlatformOrchestratorControlPlaneClient(t)
	checkRetained := func(_ *terraform.State) error {
		entry, err := client.GetModuleCatalogueEntryWithResponse(t.Context(), os.Getenv(PO_ORG_ID_ENV_VAR), id)
		if err != nil {
			return err
		}
		if entry.StatusCode() != http.StatusOK || entry.JSON200 == nil || entry.JSON200.Status != "archived" {
			return fmt.Errorf("retained Module was not archived: HTTP %d", entry.StatusCode())
		}
		version, err := client.GetModuleVersionWithResponse(t.Context(), os.Getenv(PO_ORG_ID_ENV_VAR), id, versionID)
		if err != nil {
			return err
		}
		if version.StatusCode() != http.StatusOK || version.JSON200 == nil || version.JSON200.Version.LifecycleStatus != "default" {
			return fmt.Errorf("immutable Version or lifecycle was lost: HTTP %d", version.StatusCode())
		}
		return nil
	}
	resource.Test(t, resource.TestCase{
		PreCheck: func() { testAccPreCheck(t) }, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: checkRetained,
		Steps: []resource.TestStep{
			{
				Config: moduleManagementAcceptanceConfig(id, "active", "Release readiness", "Initial catalogue", "default"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("platform-orchestrator_module_version.release", "lifecycle_status", "default"),
					func(state *terraform.State) error {
						versionID = state.RootModule().Resources["platform-orchestrator_module_version.release"].Primary.ID
						return nil
					},
				),
			},
			{Config: moduleManagementAcceptanceConfig(id, "active", "Next release explanation", "Initial catalogue", "default")},
			{
				Config: moduleManagementAcceptanceConfig(id, "archived", "Release review", "Reviewed catalogue", "default"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("platform-orchestrator_module_catalogue_entry.release", "status", "archived"),
					resource.TestCheckResourceAttr("platform-orchestrator_module_catalogue_entry.release", "description", "Reviewed catalogue"),
					resource.TestCheckResourceAttr("platform-orchestrator_resource_type.release", "status", "archived"),
				),
			},
			{Config: moduleManagementAcceptanceConfig(id, "active", "Resume release", "Reviewed catalogue", "default")},
			{Config: moduleManagementAcceptanceConfig(id, "archived", "Release review", "Reviewed catalogue", "default")},
			{Config: moduleManagementAcceptanceConfig(id, "active", "Resume release", "Reviewed catalogue", "default")},
			{Config: moduleManagementAcceptanceConfig(id, "active", "Resume release", "Reviewed catalogue", "default"), Destroy: true},
			{
				Config:       moduleManagementAcceptanceConfig(id, "active", "Reclaim retained catalogue", "Reviewed catalogue", ""),
				ResourceName: "platform-orchestrator_resource_type.release", ImportState: true, ImportStatePersist: true, ImportStateId: id,
			},
			{ResourceName: "platform-orchestrator_provider.release", ImportState: true, ImportStatePersist: true, ImportStateId: "random." + id},
			{ResourceName: "platform-orchestrator_module_catalogue_entry.release", ImportState: true, ImportStatePersist: true, ImportStateId: id},
			{
				ResourceName: "platform-orchestrator_module_version.release", ImportState: true, ImportStatePersist: true, ImportStateId: id + "/1.0.0",
				ImportStateCheck: moduleVersionImportDefinitionHasNoSemanticVersion(&importedVersionResourceVersion),
			},
			{
				Config: moduleManagementAcceptanceConfig(id, "active", "Reclaim retained catalogue", "Reviewed catalogue", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("platform-orchestrator_module_version.release", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("platform-orchestrator_module_catalogue_entry.release", "status", "active"),
					resource.TestCheckResourceAttr("platform-orchestrator_module_version.release", "lifecycle_status", "default"),
					func(state *terraform.State) error {
						currentResourceVersion := state.RootModule().Resources["platform-orchestrator_module_version.release"].Primary.Attributes["resource_version"]
						if currentResourceVersion != importedVersionResourceVersion {
							return fmt.Errorf("reason-only import convergence changed state resource_version from %s to %s", importedVersionResourceVersion, currentResourceVersion)
						}
						version, err := client.GetModuleVersionWithResponse(t.Context(), os.Getenv(PO_ORG_ID_ENV_VAR), id, versionID)
						if err != nil {
							return err
						}
						if version.StatusCode() != http.StatusOK || version.JSON200 == nil {
							return fmt.Errorf("read Module Version after import convergence: HTTP %d", version.StatusCode())
						}
						if apiResourceVersion := fmt.Sprintf("%d", version.JSON200.Version.ResourceVersion); apiResourceVersion != importedVersionResourceVersion {
							return fmt.Errorf("reason-only import convergence changed API resource_version from %s to %s", importedVersionResourceVersion, apiResourceVersion)
						}
						return nil
					},
				),
			},
			{Config: moduleManagementAcceptanceConfig(id, "active", "Reclaim retained catalogue", "Reviewed catalogue", ""), PlanOnly: true},
		},
	})
}

func moduleManagementAcceptanceConfig(id, status, reason, description, lifecycle string) string {
	lifecycleConfig := ""
	if lifecycle != "" {
		lifecycleConfig = fmt.Sprintf("lifecycle_status = %q", lifecycle)
	}
	return fmt.Sprintf(`
resource "platform-orchestrator_resource_type" "release" {
  id = %[1]q
  output_schema = jsonencode({ type = "object" })
  module_contract = jsonencode({ type = "object", required = ["output_schema"] })
  deletion_policy = "retain"
  status = %[2]q
  transition_reason = %[3]q
}
resource "platform-orchestrator_provider" "release" {
  id = %[1]q
  provider_type = "random"
  source = "hashicorp/random"
  version_constraint = "= 3.7.2"
  configuration = jsonencode({})
  deletion_policy = "retain"
}
resource "platform-orchestrator_module_catalogue_entry" "release" {
  id = %[1]q
  resource_type = platform-orchestrator_resource_type.release.id
  description = %[4]q
  status = %[2]q
  transition_reason = %[3]q
  deletion_reason = "Release test finished"
}
resource "platform-orchestrator_module_version" "release" {
  module_id = platform-orchestrator_module_catalogue_entry.release.id
  semantic_version = "1.0.0"
  definition = jsonencode({
    output_schema = jsondecode(platform-orchestrator_resource_type.release.output_schema)
    module_source = "inline"
    module_source_code = "output \"value\" { value = \"ready\" }"
    module_inputs = {}
    module_params = {}
    provider_mapping = { random = "random.${platform-orchestrator_provider.release.id}" }
    dependencies = {}
    coprovisioned = []
  })
  %[5]s
  transition_reason = %[3]q
}
`, id, status, reason, description, lifecycleConfig)
}

func moduleVersionImportDefinitionHasNoSemanticVersion(resourceVersion *string) resource.ImportStateCheckFunc {
	return func(states []*terraform.InstanceState) error {
		for _, state := range states {
			definition := state.Attributes["definition"]
			if definition == "" {
				continue
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(definition), &fields); err != nil {
				return err
			}
			if _, ok := fields["semantic_version"]; ok {
				return fmt.Errorf("imported Module Version definition duplicated semantic_version identity")
			}
			*resourceVersion = state.Attributes["resource_version"]
			if *resourceVersion == "" {
				return fmt.Errorf("imported Module Version resource_version was empty")
			}
			return nil
		}
		return fmt.Errorf("imported Module Version state not found")
	}
}

func TestAccModuleCatalogueEmptyShellRecreation(t *testing.T) {
	id := fmt.Sprintf("empty-shell-%d", time.Now().UnixNano())
	firstUUID := ""
	config := fmt.Sprintf(`
resource "platform-orchestrator_resource_type" "empty" {
  id = %[1]q
  output_schema = jsonencode({})
}

resource "platform-orchestrator_module_catalogue_entry" "empty" {
  id = %[1]q
  resource_type = platform-orchestrator_resource_type.empty.id
}
`, id)
	resource.Test(t, resource.TestCase{
		PreCheck: func() { testAccPreCheck(t) }, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config, Check: func(state *terraform.State) error {
				firstUUID = state.RootModule().Resources["platform-orchestrator_module_catalogue_entry.empty"].Primary.Attributes["uuid"]
				return nil
			}},
			{Config: config, Destroy: true},
			{Config: config, Check: func(state *terraform.State) error {
				if state.RootModule().Resources["platform-orchestrator_module_catalogue_entry.empty"].Primary.Attributes["uuid"] == firstUUID {
					return fmt.Errorf("recreated shell replayed deleted identity %s", firstUUID)
				}
				return nil
			}},
		},
	})
}
func TestAccModuleVersionPromotionRestoreCycles(t *testing.T) {
	id := fmt.Sprintf("restore-cycle-%d", time.Now().UnixNano())
	config := func(firstStatus, secondStatus string, includeSecond bool) string {
		first := moduleManagementAcceptanceConfig(id, "active", "Restore release", "Restoration test", firstStatus)
		if !includeSecond {
			return first
		}
		second := `
resource "platform-orchestrator_module_version" "successor" {
  module_id = platform-orchestrator_module_catalogue_entry.release.id
  semantic_version = "1.1.0"
  definition = jsonencode({
    output_schema = jsondecode(platform-orchestrator_resource_type.release.output_schema)
    module_source = "inline"
    module_source_code = "output \"value\" { value = \"updated\" }"
    module_inputs = {}
    module_params = {}
    provider_mapping = {}
    dependencies = {}
    coprovisioned = []
  })
  transition_reason = "Restore release"
`
		if secondStatus != "" {
			second += fmt.Sprintf("  lifecycle_status = %q\n", secondStatus)
		}
		return first + second + "}\n"
	}
	check := func(first, second string) resource.TestCheckFunc {
		return resource.ComposeTestCheckFunc(
			resource.TestCheckResourceAttr("platform-orchestrator_module_version.release", "lifecycle_status", first),
			resource.TestCheckResourceAttr("platform-orchestrator_module_version.successor", "lifecycle_status", second),
		)
	}
	resource.Test(t, resource.TestCase{
		PreCheck: func() { testAccPreCheck(t) }, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config("default", "", false)},
			{Config: config("", "default", true)},
			{RefreshState: true, Check: check("deprecated", "default")},
			{Config: config("default", "", true)},
			{RefreshState: true, Check: check("default", "deprecated")},
			{Config: config("", "default", true)},
			{Config: config("default", "", true)},
			{Config: config("default", "", true), PlanOnly: true, Check: check("default", "deprecated")},
		},
	})
}

func TestAccModuleLifecycleTransactionNoOpPlan(t *testing.T) {
	id := fmt.Sprintf("lifecycle-receipt-%d", time.Now().UnixNano())
	config := moduleManagementAcceptanceConfig(id, "active", "Publication", "Atomic receipt test", "") + `
resource "platform-orchestrator_module_version_lifecycle_transaction" "promotion" {
  transaction = jsonencode({ transitions = [{
    module_id = platform-orchestrator_module_catalogue_entry.release.id
    module_version_id = platform-orchestrator_module_version.release.id
    expected_resource_version = 1
    action = "promote"
    reason = "Release the initial stable version"
  }] })
}
`
	resource.Test(t, resource.TestCase{
		PreCheck: func() { testAccPreCheck(t) }, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{RefreshState: true, Check: resource.TestCheckResourceAttr("platform-orchestrator_module_version.release", "lifecycle_status", "default")},
			{Config: config, PlanOnly: true},
			{Config: strings.ReplaceAll(config, "Atomic receipt test", "Updated metadata"), Check: resource.TestCheckResourceAttr("platform-orchestrator_module_version.release", "lifecycle_status", "default")},
		},
	})
}

func TestAccModuleVersionPartialPublicationRetainsState(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests require TF_ACC")
	}
	testAccPreCheck(t)
	ctx := t.Context()
	client := NewPlatformOrchestratorControlPlaneClient(t)
	org := os.Getenv(PO_ORG_ID_ENV_VAR)
	id := "partial-publication-" + uuid.NewString()
	rt, err := client.CreateResourceTypeWithResponse(ctx, org, cp.ResourceTypeCreateBody{Id: id, OutputSchema: map[string]interface{}{}})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, rt.StatusCode())
	created, err := client.CreateModuleCatalogueEntryWithResponse(ctx, org, &cp.CreateModuleCatalogueEntryParams{IdempotencyKey: uuid.NewString()}, cp.ModuleCatalogueCreateBody{Slug: id, ResourceType: id})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, created.StatusCode())
	r := &ModuleVersionResource{cpClient: client, orgID: org}
	var schemaResponse frameworkresource.SchemaResponse
	r.Schema(ctx, frameworkresource.SchemaRequest{}, &schemaResponse)
	plan := tfsdk.Plan{Schema: schemaResponse.Schema}
	require.False(t, plan.Set(ctx, &ModuleVersionResourceModel{
		ID: types.StringUnknown(), ModuleID: types.StringValue(id), SemanticVersion: types.StringValue("1.0.0-rc.1"),
		Definition:      jsontypes.NewNormalizedValue(`{"module_source":"inline","module_source_code":"output \"name\" { value = \"candidate\" }","module_inputs":{},"module_params":{},"provider_mapping":{},"dependencies":{},"coprovisioned":[]}`),
		LifecycleStatus: types.StringValue("default"), TransitionReason: types.StringValue("Attempt candidate promotion"), ResourceVersion: types.Int64Unknown(), Verification: types.StringUnknown(),
	}).HasError())
	response := frameworkresource.CreateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
	r.Create(ctx, frameworkresource.CreateRequest{Plan: plan}, &response)
	require.True(t, response.Diagnostics.HasError(), "Core must refuse a prerelease Default")
	var published ModuleVersionResourceModel
	require.False(t, response.State.Get(ctx, &published).HasError())
	require.NotEmpty(t, published.ID.ValueString(), "failed promotion must preserve created Version ownership")
	require.Equal(t, "proposed", published.LifecycleStatus.ValueString())
	require.Equal(t, int64(1), published.ResourceVersion.ValueInt64())

	recovery := published
	recovery.LifecycleStatus = types.StringValue("deprecated")
	recovery.TransitionReason = types.StringValue("Withdraw candidate after failed promotion")
	require.False(t, plan.Set(ctx, &recovery).HasError())
	updated := frameworkresource.UpdateResponse{State: response.State}
	r.Update(ctx, frameworkresource.UpdateRequest{Plan: plan, State: response.State}, &updated)
	require.False(t, updated.Diagnostics.HasError(), "%v", updated.Diagnostics)
	version, err := client.GetModuleVersionWithResponse(ctx, org, id, published.ID.ValueString())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, version.StatusCode())
	require.Equal(t, cp.ModuleVersionSemanticStatusDeprecated, version.JSON200.Version.LifecycleStatus)
	require.Equal(t, int64(2), version.JSON200.Version.ResourceVersion)
	entry, err := client.GetModuleCatalogueEntryWithResponse(ctx, org, id)
	require.NoError(t, err)
	require.NotNil(t, entry.JSON200)
	archived, err := client.ChangeModuleCatalogueStatusWithResponse(ctx, org, id, cp.ChangeModuleCatalogueStatusParamsCatalogueActionArchive,
		&cp.ChangeModuleCatalogueStatusParams{IdempotencyKey: uuid.NewString()}, cp.ModuleReasonedCommand{ExpectedResourceVersion: entry.JSON200.ResourceVersion, Reason: "Partial publication recovery test completed"})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, archived.StatusCode())
}
