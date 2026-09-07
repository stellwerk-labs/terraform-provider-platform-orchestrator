package provider

import (
	"context"
	"net/http"
	"testing"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/require"
)

func TestReasonedCommandIdentitySeparatesLifecycleCycles(t *testing.T) {
	t.Parallel()
	key := reasonedCommandKey("org", "version", "restore", 3, "Recover")
	require.Equal(t, key, reasonedCommandKey("org", "version", "restore", 3, "Recover"))
	require.NotEqual(t, key, reasonedCommandKey("org", "version", "restore", 5, "Recover"))
	require.NotEqual(t, key, reasonedCommandKey("org", "version", "restore", 3, "Different decision"))
	require.NotEqual(t, key, reasonedCommandKey("other", "version", "restore", 3, "Recover"))
}

func TestModuleCreationRequiresLifecycleReasonBeforePublication(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	for _, desired := range []string{"default", "deprecated", "defective"} {
		t.Run(desired, func(t *testing.T) {
			r := &ModuleVersionResource{}
			var schemaResponse resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &schemaResponse)
			plan := tfsdk.Plan{Schema: schemaResponse.Schema}
			require.False(t, plan.Set(ctx, &ModuleVersionResourceModel{
				ID: types.StringUnknown(), ModuleID: types.StringValue("database"), SemanticVersion: types.StringValue("1.0.0"),
				Definition: jsontypes.NewNormalizedValue(`{"module_source":"inline"}`), LifecycleStatus: types.StringValue(desired),
				TransitionReason: types.StringValue(" "), ResourceVersion: types.Int64Unknown(), Verification: types.StringUnknown(),
			}).HasError())
			response := resource.CreateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &response)
			require.True(t, response.Diagnostics.HasError())
			require.Contains(t, response.Diagnostics.Errors()[0].Detail(), "transition_reason")
		})
	}
}

func TestCatalogueCreationRequiresArchiveReasonBeforeCreatingShell(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	r := &ModuleCatalogueEntryResource{}
	var schemaResponse resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResponse)
	plan := tfsdk.Plan{Schema: schemaResponse.Schema}
	require.False(t, plan.Set(ctx, &ModuleCatalogueEntryResourceModel{
		ID: types.StringValue("database"), UUID: types.StringUnknown(), ResourceType: types.StringValue("database"),
		DisplayName: types.StringUnknown(), Description: types.StringUnknown(), Tags: types.MapUnknown(types.StringType),
		Status: types.StringValue("archived"), TransitionReason: types.StringNull(), ResourceVersion: types.Int64Unknown(),
	}).HasError())
	response := resource.CreateResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &response)
	require.True(t, response.Diagnostics.HasError())
	require.Contains(t, response.Diagnostics.Errors()[0].Detail(), "transition_reason")
}

func TestResourceTypeStateRetainsLocalOwnershipPolicy(t *testing.T) {
	t.Parallel()
	state := ResourceTypeResourceModel{DeletionPolicy: types.StringValue("retain"), TransitionReason: types.StringValue("Retire contract")}
	diagnostics := diag.Diagnostics{}
	require.True(t, applyResourceTypeState(&state, cp.ResourceType{
		Id: "database", OutputSchema: map[string]interface{}{}, CatalogueStatus: cp.ResourceTypeCatalogueStatusArchived, ResourceVersion: 4,
	}, &diagnostics))
	require.Equal(t, "retain", state.DeletionPolicy.ValueString())
	require.Equal(t, "Retire contract", state.TransitionReason.ValueString())
	require.Equal(t, "archived", state.Status.ValueString())
	require.Equal(t, int64(4), state.ResourceVersion.ValueInt64())
}

func TestLegacyModuleDiagnosticDoesNotMaskOtherValidationFailures(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		status int
		body   string
		want   bool
	}{
		{http.StatusBadRequest, `{"error":"HTTP-400","message":"semantic_version is required while Core Module Version Management is enabled"}`, true},
		{http.StatusForbidden, `{"message":"semantic_version is required while Core Module Version Management is enabled"}`, false},
		{http.StatusBadRequest, `{"message":"invalid provider mapping"}`, false},
		{http.StatusBadRequest, `not JSON`, false},
	} {
		diagnostics := diag.Diagnostics{}
		require.Equal(t, test.want, addLegacyModuleWriteDiagnostic(&diagnostics, test.status, []byte(test.body)))
		require.Equal(t, test.want, diagnostics.HasError())
	}
}

func TestModuleVersionReasonOnlyUpdatePreservesComputedState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := &ModuleVersionResource{}
	var schemaResponse resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResponse)
	before := ModuleVersionResourceModel{
		ID: types.StringValue("version-uuid"), ModuleID: types.StringValue("database"), SemanticVersion: types.StringValue("1.0.0"),
		Definition: jsontypes.NewNormalizedValue(`{"module_source":"inline"}`), LifecycleStatus: types.StringValue("default"),
		TransitionReason: types.StringValue("Release"), ResourceVersion: types.Int64Value(2), Verification: types.StringValue("unverified"),
	}
	after := before
	after.ID, after.LifecycleStatus, after.Verification = types.StringUnknown(), types.StringUnknown(), types.StringUnknown()
	after.ResourceVersion = types.Int64Unknown()
	after.TransitionReason = types.StringValue("Next release explanation")
	state := tfsdk.State{Schema: schemaResponse.Schema}
	plan := tfsdk.Plan{Schema: schemaResponse.Schema}
	require.False(t, state.Set(ctx, &before).HasError())
	require.False(t, plan.Set(ctx, &after).HasError())
	response := resource.UpdateResponse{State: state}
	r.Update(ctx, resource.UpdateRequest{Plan: plan, State: state}, &response)
	require.False(t, response.Diagnostics.HasError(), "%v", response.Diagnostics)
	var result ModuleVersionResourceModel
	require.False(t, response.State.Get(ctx, &result).HasError())
	require.Equal(t, before.ID, result.ID)
	require.Equal(t, before.Verification, result.Verification)
	require.Equal(t, before.ResourceVersion, result.ResourceVersion)
	require.Equal(t, before.LifecycleStatus, result.LifecycleStatus)
	require.Equal(t, after.TransitionReason, result.TransitionReason)
}
