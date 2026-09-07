package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
	"github.com/stretchr/testify/require"
)

func TestModuleVersionLifecycleAction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current string
		desired string
		want    cp.TransitionModuleVersionParamsLifecycleAction
		wantErr bool
	}{
		{name: "promote proposed", current: "proposed", desired: "default", want: cp.TransitionModuleVersionParamsLifecycleActionPromote},
		{name: "deprecate proposed", current: "proposed", desired: "deprecated", want: cp.TransitionModuleVersionParamsLifecycleActionDeprecate},
		{name: "deprecate default", current: "default", desired: "deprecated", want: cp.TransitionModuleVersionParamsLifecycleActionDeprecate},
		{name: "mark proposed defective", current: "proposed", desired: "defective", want: cp.TransitionModuleVersionParamsLifecycleActionMarkDefective},
		{name: "mark default defective", current: "default", desired: "defective", want: cp.TransitionModuleVersionParamsLifecycleActionMarkDefective},
		{name: "restore exact predecessor", current: "deprecated", desired: "default", want: cp.TransitionModuleVersionParamsLifecycleActionRestore},
		{name: "never infer downgrade to proposed", current: "default", desired: "proposed", wantErr: true},
		{name: "defective is terminal", current: "defective", desired: "default", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := moduleVersionLifecycleAction(test.current, test.desired)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestApplyModuleVersionDefinitionPreservesConfiguredImmutableJSON(t *testing.T) {
	t.Parallel()

	configured := `{"module_params":{"required":{"is_optional":false,"type":"map"}}}`
	state := ModuleVersionResourceModel{Definition: jsontypes.NewNormalizedValue(configured)}
	require.NoError(t, applyModuleVersionDefinition(&state, cp.ModuleVersionPublishBody{
		ModuleParams: map[string]cp.ModuleParamItem{"required": {Type: cp.ModuleParamItemType("map")}},
	}))
	require.JSONEq(t, configured, state.Definition.ValueString())
}

func TestApplyModuleVersionDefinitionReconstructsImports(t *testing.T) {
	t.Parallel()

	state := ModuleVersionResourceModel{Definition: jsontypes.NewNormalizedNull()}
	require.NoError(t, applyModuleVersionDefinition(&state, cp.ModuleVersionPublishBody{
		SemanticVersion: "1.0.0",
		ModuleSource:    "inline",
	}))
	require.False(t, state.Definition.IsNull())
	require.JSONEq(t, `{
		"semantic_version":"1.0.0",
		"module_source":"inline",
		"module_inputs":null,
		"module_params":null,
		"provider_mapping":null,
		"dependencies":null,
		"coprovisioned":null
	}`, state.Definition.ValueString())
}
