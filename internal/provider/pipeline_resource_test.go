package provider

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/stretchr/testify/require"
)

func TestPipelineSlugReplacement(t *testing.T) {
	r := &PipelineResource{}
	var schemaResponse resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &schemaResponse)
	for _, tc := range []struct {
		name, document string
		replace        bool
	}{
		{"name update", `{"slug":"basic","name":"New","definition":{"strategy":"apply","steps":[]}}`, false},
		{"slug update", `{"slug":"renamed","name":"Basic","definition":{"strategy":"apply","steps":[]}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := PipelineResourceModel{ID: types.StringValue(uuid.NewString()), ProjectID: types.StringValue("test"), Document: jsontypes.NewNormalizedValue(`{"slug":"basic","name":"Basic","definition":{"strategy":"apply","steps":[]}}`), ResourceVersion: types.Int64Value(1)}
			state := tfsdk.State{Schema: schemaResponse.Schema}
			require.False(t, state.Set(t.Context(), &previous).HasError())
			planned := previous
			planned.Document = jsontypes.NewNormalizedValue(tc.document)
			plan := tfsdk.Plan{Schema: schemaResponse.Schema}
			require.False(t, plan.Set(t.Context(), &planned).HasError())
			var resp resource.ModifyPlanResponse
			r.ModifyPlan(t.Context(), resource.ModifyPlanRequest{State: state, Plan: plan}, &resp)
			require.False(t, resp.Diagnostics.HasError(), resp.Diagnostics)
			require.Equal(t, tc.replace, len(resp.RequiresReplace) > 0)
		})
	}
}

func TestPipelineImportIdentity(t *testing.T) {
	r := &PipelineResource{}
	var schemaResponse resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &schemaResponse)
	id := uuid.NewString()
	for _, value := range []string{"project/" + id, id, "project/not-uuid", "/" + id, "project/" + id + "/extra"} {
		resp := resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
		require.False(t, resp.State.Set(t.Context(), &PipelineResourceModel{}).HasError())
		r.ImportState(t.Context(), resource.ImportStateRequest{ID: value}, &resp)
		if value == "project/"+id {
			require.False(t, resp.Diagnostics.HasError(), resp.Diagnostics)
			var got string
			require.False(t, resp.State.GetAttribute(t.Context(), path.Root("project_id"), &got).HasError())
			require.Equal(t, "project", got)
		} else {
			require.True(t, resp.Diagnostics.HasError())
		}
	}
}

func TestAccPipelineResource(t *testing.T) {
	project := os.Getenv("PIPELINE_TEST_PROJECT")
	if project == "" {
		t.Skip("PIPELINE_TEST_PROJECT must identify an isolated acceptance project")
	}
	slug := fmt.Sprintf("provider-%d", time.Now().UnixNano())
	config := func(name string) string {
		return fmt.Sprintf(`resource "platform-orchestrator_pipeline" "test" {
 project_id = %q
 document = jsonencode({slug=%q,name=%q,definition={strategy="apply",steps=[]}})
}`, project, slug, name)
	}
	nonCanonical := fmt.Sprintf(`resource "platform-orchestrator_pipeline" "test" {
 project_id = %q
 document = %q
}`, project, fmt.Sprintf("{\n \"slug\":%q, \"name\":\"Basic\", \"definition\":{\"strategy\":\"apply\",\"steps\":[]}\n}", slug))
	tfresource.Test(t, tfresource.TestCase{PreCheck: func() { testAccPreCheck(t) }, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: []tfresource.TestStep{
		{Config: nonCanonical, Check: tfresource.ComposeTestCheckFunc(tfresource.TestCheckResourceAttrSet("platform-orchestrator_pipeline.test", "id"), tfresource.TestCheckResourceAttr("platform-orchestrator_pipeline.test", "resource_version", "1"))},
		{Config: nonCanonical, PlanOnly: true, ExpectNonEmptyPlan: false},
		{Config: config("Renamed"), Check: tfresource.TestCheckResourceAttr("platform-orchestrator_pipeline.test", "resource_version", "2")},
		{ResourceName: "platform-orchestrator_pipeline.test", ImportState: true, ImportStateVerify: true, ImportStateIdFunc: func(state *terraform.State) (string, error) {
			return project + "/" + state.RootModule().Resources["platform-orchestrator_pipeline.test"].Primary.ID, nil
		}},
	}})
}
