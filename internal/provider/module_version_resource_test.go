package provider

import (
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
	"github.com/stretchr/testify/require"
)

func TestModuleVersionDefinitionRejectsUnknownFieldsAndPreservesDeclarations(t *testing.T) {
	for _, value := range []string{`{"output_schmea":{}}`, `{} {}`, `null`, `[]`} {
		var body cp.ModuleVersionPublishBody
		require.Error(t, decodeModuleVersionDefinition(value, &body), value)
	}
	for _, output := range []string{"", `,"output_schema":{}`, `,"output_schema":{"type":"object"}`} {
		var body cp.ModuleVersionPublishBody
		require.NoError(t, decodeModuleVersionDefinition(`{"module_source":"https://example.invalid/module.zip","source_revision":"0123456789abcdef"`+output+`}`, &body))
		require.Nil(t, body.ArtifactDigest, "omitted external digest must not be invented")
		if output == "" {
			require.Nil(t, body.OutputSchema)
		} else {
			require.NotNil(t, body.OutputSchema)
		}
		importBody := moduleVersionPublishBody(cp.CoreModuleVersionDetail{OutputSchema: body.OutputSchema})
		state := ModuleVersionResourceModel{Definition: jsontypes.NewNormalizedNull()}
		require.NoError(t, applyModuleVersionDefinition(&state, importBody))
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(state.Definition.ValueString()), &fields))
		_, present := fields["output_schema"]
		require.Equal(t, output != "", present)
		if present {
			encoded, err := json.Marshal(body.OutputSchema)
			require.NoError(t, err)
			require.JSONEq(t, string(encoded), string(fields["output_schema"]))
		}
	}
}

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
