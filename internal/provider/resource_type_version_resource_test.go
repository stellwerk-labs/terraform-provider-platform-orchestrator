package provider

import (
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/require"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
)

func interfaceResourceState() ResourceTypeVersionResourceModel {
	return ResourceTypeVersionResourceModel{ID: types.StringValue(uuid.NewString()), ResourceTypeID: types.StringValue("storage"), Version: types.Int64Value(2), ExpectedResourceVersion: types.Int64Value(1), OutputSchema: jsontypes.NewNormalizedValue(`{"type":"object","properties":{}}`), ModuleContract: jsontypes.NewNormalizedNull(), Reason: types.StringValue("Reviewed interface")}
}

func TestResourceTypeVersionContentComparison(t *testing.T) {
	original := interfaceResourceState()
	tests := []struct {
		name     string
		change   func(*ResourceTypeVersionResourceModel)
		planning bool
		changed  bool
	}{
		{name: "unchanged", change: func(*ResourceTypeVersionResourceModel) {}},
		{name: "JSON spelling", change: func(v *ResourceTypeVersionResourceModel) {
			v.OutputSchema = jsontypes.NewNormalizedValue("{\n \"properties\": {}, \"type\": \"object\"\n}")
		}},
		{name: "creation precondition", change: func(v *ResourceTypeVersionResourceModel) { v.ExpectedResourceVersion = types.Int64Value(9) }},
		{name: "changed output", change: func(v *ResourceTypeVersionResourceModel) {
			v.OutputSchema = jsontypes.NewNormalizedValue(`{"type":"string"}`)
		}, changed: true},
		{name: "introduced contract", change: func(v *ResourceTypeVersionResourceModel) {
			v.ModuleContract = jsontypes.NewNormalizedValue(`{"type":"object"}`)
		}, changed: true},
		{name: "changed reason", change: func(v *ResourceTypeVersionResourceModel) { v.Reason = types.StringValue("Other intent") }, changed: true},
		{name: "unknown at plan", change: func(v *ResourceTypeVersionResourceModel) { v.OutputSchema = jsontypes.NewNormalizedUnknown() }, planning: true},
		{name: "unknown at apply", change: func(v *ResourceTypeVersionResourceModel) { v.OutputSchema = jsontypes.NewNormalizedUnknown() }, changed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := original
			tt.change(&plan)
			require.Equal(t, tt.changed, resourceTypeVersionContentChanged(t.Context(), plan, original, tt.planning))
		})
	}
}

func TestResourceTypeVersionPlanRejectsIdentityReuse(t *testing.T) {
	r := &ResourceTypeVersionResource{}
	var schemaResponse resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &schemaResponse)
	original := interfaceResourceState()
	state := tfsdk.State{Schema: schemaResponse.Schema}
	require.False(t, state.Set(t.Context(), &original).HasError())
	for _, tt := range []struct {
		name      string
		version   int64
		wantError bool
	}{{"same permanent identity", 2, true}, {"new permanent identity", 3, false}} {
		t.Run(tt.name, func(t *testing.T) {
			wanted := original
			wanted.Version = types.Int64Value(tt.version)
			wanted.OutputSchema = jsontypes.NewNormalizedValue(`{"type":"string"}`)
			plan := tfsdk.Plan{Schema: schemaResponse.Schema}
			require.False(t, plan.Set(t.Context(), &wanted).HasError())
			var response resource.ModifyPlanResponse
			r.ModifyPlan(t.Context(), resource.ModifyPlanRequest{State: state, Plan: plan}, &response)
			require.Equal(t, tt.wantError, response.Diagnostics.HasError())
		})
	}
}

func TestResourceTypeVersionRefreshPreservesCreationPrecondition(t *testing.T) {
	state := interfaceResourceState()
	wantedID := uuid.New()
	reason := "Reviewed interface"
	require.NoError(t, applyResourceTypeVersionState(&state, cp.ResourceTypeVersion{Uuid: wantedID, ResourceTypeId: "storage", Version: 2, OutputSchema: map[string]any{"type": "object"}, Reason: &reason}))
	require.Equal(t, wantedID.String(), state.ID.ValueString())
	require.Equal(t, int64(1), state.ExpectedResourceVersion.ValueInt64())
	require.True(t, state.ModuleContract.IsNull())
}
