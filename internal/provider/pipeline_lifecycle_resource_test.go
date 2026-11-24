package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/pipelines"
	"github.com/stretchr/testify/require"
)

func TestPipelinePolicyConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, document string
		valid          bool
	}{
		{"explicit empty policy", `{"enabled":false,"allowed_version_ids":[],"rollback_defaults":{}}`, true},
		{"both defaults", `{"enabled":true,"allowed_version_ids":["aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"],"default_version_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","rollback_defaults":{"default_version_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}}`, true},
		{"missing enabled", `{"allowed_version_ids":[],"rollback_defaults":{}}`, false},
		{"null enabled", `{"enabled":null,"allowed_version_ids":[],"rollback_defaults":{}}`, false},
		{"missing allow-list", `{"enabled":false,"rollback_defaults":{}}`, false},
		{"missing rollback ownership", `{"enabled":false,"allowed_version_ids":[]}`, false},
		{"null rollback ownership", `{"enabled":false,"allowed_version_ids":[],"rollback_defaults":null}`, false},
		{"revision belongs to state", `{"enabled":false,"allowed_version_ids":[],"rollback_defaults":{},"expected_resource_version":9}`, false},
		{"unknown nested field", `{"enabled":false,"allowed_version_ids":[],"rollback_defaults":{"fallback_to_forward":true}}`, false},
		{"multiple documents", `{"enabled":false,"allowed_version_ids":[],"rollback_defaults":{}} {}`, false},
		{"bounded document", strings.Repeat("x", 256*1024+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pipelinePolicyConfigurationFromDocument(tc.document)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
func TestPipelinePolicyBodyRetainsDuplicateKeysForCoreValidation(t *testing.T) {
	document := `{"enabled":false,"enabled":true,"allowed_version_ids":[],"rollback_defaults":{}}`
	raw, err := io.ReadAll(pipelinePolicyBody(document, 17))
	require.NoError(t, err)
	require.Equal(t, `{"expected_resource_version":17,`+document[1:], string(raw))
	require.True(t, json.Valid(raw))
}
func TestPipelinePolicyForgetRequiresExplicitEmptyCurrentPolicy(t *testing.T) {
	id := uuid.New()
	for _, tc := range []struct {
		name    string
		value   pipelines.PipelinePolicy
		allowed bool
	}{
		{"empty same revision", pipelines.PipelinePolicy{ResourceVersion: 4, RollbackDefaults: &pipelines.PipelineRollbackDefaults{}}, true},
		{"changed concurrently", pipelines.PipelinePolicy{ResourceVersion: 5}, false},
		{"protected", pipelines.PipelinePolicy{ResourceVersion: 4, Enabled: true}, false},
		{"allow-list", pipelines.PipelinePolicy{ResourceVersion: 4, AllowedVersionIds: []uuid.UUID{id}}, false},
		{"forward default", pipelines.PipelinePolicy{ResourceVersion: 4, DefaultVersionId: &id}, false},
		{"forward exception", pipelines.PipelinePolicy{ResourceVersion: 4, EnvironmentDefaultVersionIds: map[string]uuid.UUID{id.String(): id}}, false},
		{"rollback default", pipelines.PipelinePolicy{ResourceVersion: 4, RollbackDefaults: &pipelines.PipelineRollbackDefaults{DefaultVersionId: &id}}, false},
		{"rollback exception", pipelines.PipelinePolicy{ResourceVersion: 4, RollbackDefaults: &pipelines.PipelineRollbackDefaults{EnvironmentDefaultVersionIds: map[string]uuid.UUID{id.String(): id}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.allowed, pipelinePolicyCanForget(tc.value, 4)) })
	}
}
func TestPipelinePublicationInputAndImport(t *testing.T) {
	id := uuid.NewString()
	draft := uuid.NewString()
	value := PipelineVersionResourceModel{PipelineID: types.StringValue(draft), ExpectedResourceVersion: types.Int64Value(3), RequestKey: types.StringValue("stable-intent")}
	_, err := pipelinePublicationInput(value)
	require.NoError(t, err)
	for _, key := range []string{"", " ", strings.Repeat("x", 129)} {
		bad := value
		bad.RequestKey = types.StringValue(key)
		_, err := pipelinePublicationInput(bad)
		require.Error(t, err)
	}
	r := &PipelineVersionResource{}
	var schema resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &schema)
	for _, tc := range []struct {
		name, input string
		valid       bool
	}{
		{"explicit retained intent", `{"project_id":"owned","id":"` + id + `","pipeline_id":"` + draft + `","expected_resource_version":3,"request_key":"stable-intent"}`, true},
		{"cannot reconstruct intent", `{"project_id":"owned","id":"` + id + `"}`, false},
		{"trailing object", `{"project_id":"owned","id":"` + id + `","pipeline_id":"` + draft + `","expected_resource_version":3,"request_key":"stable-intent"} {}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := resource.ImportStateResponse{State: tfsdk.State{Schema: schema.Schema}}
			r.ImportState(t.Context(), resource.ImportStateRequest{ID: tc.input}, &resp)
			require.Equal(t, !tc.valid, resp.Diagnostics.HasError(), resp.Diagnostics)
			if tc.valid {
				var data PipelineVersionResourceModel
				require.False(t, resp.State.Get(t.Context(), &data).HasError())
				require.Equal(t, id, data.ID.ValueString())
				require.Equal(t, "stable-intent", data.RequestKey.ValueString())
			}
		})
	}
}

func TestAccPipelinePublicationAndPolicy(t *testing.T) {
	project := os.Getenv("PIPELINE_TEST_PROJECT")
	if project == "" {
		t.Skip("PIPELINE_TEST_PROJECT must identify an isolated project with no existing pipeline policy")
	}
	slug := "publication-" + uuid.NewString()[:8]
	configuration := func(name, key string, enabled bool) string {
		policy := `{enabled=false,allowed_version_ids=[],rollback_defaults={}}`
		if enabled {
			policy = `{enabled=true,allowed_version_ids=[platform-orchestrator_pipeline_version.test.id],default_version_id=platform-orchestrator_pipeline_version.test.id,rollback_defaults={default_version_id=platform-orchestrator_pipeline_version.test.id}}`
		}
		result := fmt.Sprintf(`resource "platform-orchestrator_pipeline" "test" {
 project_id=%q
 document=jsonencode({slug=%q,name=%q,definition={strategy="apply",steps=[]}})
}
resource "platform-orchestrator_pipeline_version" "test" {
 project_id=%q
 pipeline_id=platform-orchestrator_pipeline.test.id
 expected_resource_version=platform-orchestrator_pipeline.test.resource_version
 request_key=%q
 lifecycle { create_before_destroy=true }
}
resource "platform-orchestrator_pipeline_policy" "test" {
 project_id=%q
 document=jsonencode(%s)
}`, project, slug, name, project, key, project, policy)
		if os.Getenv("PIPELINE_TEST_POLICY_IMPORT") == "1" {
			result += fmt.Sprintf("\nimport {\n to=platform-orchestrator_pipeline_policy.test\n id=%q\n}\n", project)
		}
		return result
	}
	httpClient, err := newAPIHTTPClient(os.Getenv(PO_CA_FILE_ENV_VAR))
	require.NoError(t, err)
	client, err := pipelines.NewClientWithResponses(os.Getenv(PO_API_URL_ENV_VAR), pipelines.WithHTTPClient(httpClient), pipelines.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+os.Getenv(PO_AUTH_TOKEN_ENV_VAR))
		return nil
	}))
	require.NoError(t, err)
	externalRetire := func(state *terraform.State) error {
		attributes := state.RootModule().Resources["platform-orchestrator_pipeline_version.test"].Primary.Attributes
		id, err := uuid.Parse(attributes["id"])
		if err != nil {
			return err
		}
		result, err := client.RetireProjectPipelineWithResponse(t.Context(), os.Getenv(PO_ORG_ID_ENV_VAR), project, id)
		if err != nil {
			return err
		}
		if result.StatusCode() != 200 {
			return fmt.Errorf("external retirement returned HTTP %d", result.StatusCode())
		}
		// Retry the original intent through the resource itself: it must not claim a live creation.
		r := &PipelineVersionResource{client: client, orgID: os.Getenv(PO_ORG_ID_ENV_VAR)}
		var schema resource.SchemaResponse
		r.Schema(t.Context(), resource.SchemaRequest{}, &schema)
		model := PipelineVersionResourceModel{ProjectID: types.StringValue(project), PipelineID: types.StringValue(attributes["pipeline_id"]), ExpectedResourceVersion: types.Int64Value(2), RequestKey: types.StringValue(slug + "-second")}
		plan := tfsdk.Plan{Schema: schema.Schema}
		if diagnostics := plan.Set(t.Context(), &model); diagnostics.HasError() {
			return fmt.Errorf("set retained publication plan: %s", diagnostics)
		}
		response := resource.CreateResponse{State: tfsdk.State{Schema: schema.Schema}}
		r.Create(t.Context(), resource.CreateRequest{Plan: plan}, &response)
		if !response.Diagnostics.HasError() {
			return fmt.Errorf("retired publication retry unexpectedly succeeded")
		}
		return nil
	}
	initial := configuration("Reviewed first", slug+"-first", true)
	changed := configuration("Reviewed second", slug+"-second", true)
	tfresource.Test(t, tfresource.TestCase{PreCheck: func() { testAccPreCheck(t) }, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: []tfresource.TestStep{
		{Config: initial, Check: tfresource.ComposeTestCheckFunc(tfresource.TestCheckResourceAttrSet("platform-orchestrator_pipeline_version.test", "id"), tfresource.TestCheckResourceAttr("platform-orchestrator_pipeline_version.test", "version", "1"), tfresource.TestCheckResourceAttrSet("platform-orchestrator_pipeline_policy.test", "resource_version"))},
		{Config: initial, PlanOnly: true, ExpectNonEmptyPlan: false},
		{ResourceName: "platform-orchestrator_pipeline_version.test", ImportState: true, ImportStateVerify: true, ImportStateIdFunc: func(state *terraform.State) (string, error) {
			attributes := state.RootModule().Resources["platform-orchestrator_pipeline_version.test"].Primary.Attributes
			raw, err := json.Marshal(map[string]any{"project_id": project, "id": attributes["id"], "pipeline_id": attributes["pipeline_id"], "expected_resource_version": 1, "request_key": slug + "-first"})
			return string(raw), err
		}},
		{ResourceName: "platform-orchestrator_pipeline_policy.test", ImportState: true, ImportStateVerify: true, ImportStateId: project},
		{Config: changed, Check: tfresource.TestCheckResourceAttr("platform-orchestrator_pipeline_version.test", "version", "2")},
		{Config: configuration("Reviewed second", slug+"-second", false), Check: externalRetire},
		{Config: configuration("Reviewed second", slug+"-second", false), PlanOnly: true, ExpectNonEmptyPlan: false},
		{Config: configuration("Reviewed third", slug+"-third", false), Check: tfresource.TestCheckResourceAttr("platform-orchestrator_pipeline_version.test", "retired", "false")},
	}})
}
