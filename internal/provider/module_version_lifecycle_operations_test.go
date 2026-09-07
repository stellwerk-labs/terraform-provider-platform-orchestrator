package provider

import (
	"context"
	"testing"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/require"
)

func TestStableSuccessorBodyRequiresCompleteReasonedPublication(t *testing.T) {
	t.Parallel()

	valid := StableModuleVersionSuccessorResourceModel{
		ExpectedPrereleaseResourceVersion: types.Int64Value(7),
		Reason:                            types.StringValue("Release candidate passed"),
		Definition: jsontypes.NewNormalizedValue(
			`{"semantic_version":"2.0.0","module_source":"inline","module_source_code":"output \"name\" { value = \"release\" }","module_inputs":{},"module_params":{},"provider_mapping":{},"dependencies":{},"coprovisioned":[]}`,
		),
	}
	body, err := stableSuccessorBody(valid)
	require.NoError(t, err)
	require.Equal(t, int64(7), body.ExpectedPrereleaseResourceVersion)
	require.Equal(t, "2.0.0", body.Version.SemanticVersion)

	withoutReason := valid
	withoutReason.Reason = types.StringValue(" ")
	_, err = stableSuccessorBody(withoutReason)
	require.ErrorContains(t, err, "reason")

	withoutVersion := valid
	withoutVersion.Definition = jsontypes.NewNormalizedValue(`{"module_source":"inline"}`)
	_, err = stableSuccessorBody(withoutVersion)
	require.ErrorContains(t, err, "semantic_version")
}

func TestLifecycleTransactionBodyRejectsEmptyAndPreservesExactItems(t *testing.T) {
	t.Parallel()

	_, err := lifecycleTransactionBody(`{"transitions":[]}`)
	require.ErrorContains(t, err, "must not be empty")

	body, err := lifecycleTransactionBody(`{"transitions":[{"module_id":"payments","module_version_id":"2.0.0","expected_resource_version":3,"action":"promote","reason":"Ship"}]}`)
	require.NoError(t, err)
	require.Len(t, body.Transitions, 1)
	require.Equal(t, cp.ModuleVersionLifecycleTransactionItemActionPromote, body.Transitions[0].Action)
	require.Equal(t, int64(3), body.Transitions[0].ExpectedResourceVersion)
}

func TestApplyModuleCatalogueEntryStatePreservesStableIdentityAndMetadata(t *testing.T) {
	t.Parallel()

	entryID := uuid.New()
	state := ModuleCatalogueEntryResourceModel{TransitionReason: types.StringValue("Keep this plan input")}
	diagnostics := diag.Diagnostics{}
	applyModuleCatalogueEntryState(context.Background(), &diagnostics, &state, cp.ModuleCatalogueEntry{
		Uuid: entryID, Slug: "payments", DisplayName: "Payments", Description: "Payment service",
		ResourceType: "service", Status: cp.ModuleCatalogueStatusArchived, Tags: map[string]string{"owner": "checkout"}, ResourceVersion: 9,
	})
	require.False(t, diagnostics.HasError())
	require.Equal(t, entryID.String(), state.UUID.ValueString())
	require.Equal(t, "payments", state.ID.ValueString())
	require.Equal(t, "archived", state.Status.ValueString())
	require.Equal(t, int64(9), state.ResourceVersion.ValueInt64())
	require.Equal(t, "Keep this plan input", state.TransitionReason.ValueString())

	var tags map[string]string
	require.False(t, state.Tags.ElementsAs(context.Background(), &tags, false).HasError())
	require.Equal(t, map[string]string{"owner": "checkout"}, tags)
}
