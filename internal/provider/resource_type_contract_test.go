package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
	"github.com/stretchr/testify/require"
)

func TestResourceTypeContractImportPreservesOmissionAndEmptyObject(t *testing.T) {
	for _, contract := range []*cp.ResourceTypeModuleContract{nil, {}, {"type": "object"}} {
		item := cp.ResourceType{ModuleContract: contract}
		state := ResourceTypeResourceModel{}
		var diagnostics diag.Diagnostics
		require.True(t, applyResourceTypeState(&state, item, &diagnostics))
		require.False(t, diagnostics.HasError())
		require.Equal(t, contract == nil, state.ModuleContract.IsNull())
		if contract != nil {
			var roundtrip cp.ResourceTypeModuleContract
			require.False(t, state.ModuleContract.Unmarshal(&roundtrip).HasError())
			require.Equal(t, *contract, roundtrip)
		}
		require.False(t, resourceTypeContractChanged(t.Context(), state, state))
		plan := state
		plan.ModuleContract = jsontypes.NewNormalizedUnknown()
		require.False(t, resourceTypeContractChanged(t.Context(), state, plan), "omitted optional/computed contract preserves imported value")
		plan.ModuleContract = jsontypes.NewNormalizedValue(`{"type":"object","required":["module_inputs"]}`)
		require.True(t, resourceTypeContractChanged(t.Context(), state, plan), "same-identity immutable contract edits must fail")
	}
}

func TestResourceTypeJSONFormattingDoesNotBecomeContractMutation(t *testing.T) {
	state := ResourceTypeResourceModel{ModuleContract: jsontypes.NewNormalizedValue(`{"type":"object","required":["module_inputs"]}`)}
	plan := state
	plan.ModuleContract = jsontypes.NewNormalizedValue(`{ "required": ["module_inputs"], "type": "object" }`)
	require.False(t, resourceTypeContractChanged(t.Context(), state, plan))
	plan.ModuleContract = jsontypes.NewNormalizedNull()
	require.True(t, resourceTypeContractChanged(t.Context(), state, plan))
}
