package provider

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
	"github.com/stretchr/testify/require"
)

func parseModuleVersionDetail(t *testing.T, body string) *cp.CoreModuleVersionDetail {
	t.Helper()
	response, err := cp.ParseGetModuleVersionResponse(&http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	})
	require.NoError(t, err)
	require.NotNil(t, response.JSON200)
	return response.JSON200
}

func TestModuleVersionClientReadPreservesDeclarationInState(t *testing.T) {
	t.Parallel()

	const declaration = `{"type":"object","properties":{"endpoint":{"type":"string","nullable":false}}}`
	detail := parseModuleVersionDetail(t, `{"version":{"uuid":"11111111-1111-4111-8111-111111111111","module_slug":"test","opaque_version_id":"1.0.0"},"output_schema":`+declaration+`}`)
	state := ModuleVersionResourceModel{Definition: jsontypes.NewNormalizedNull()}
	require.NoError(t, applyModuleVersionDefinition(&state, moduleVersionPublishBody(*detail)))
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(state.Definition.ValueString()), &fields))
	require.JSONEq(t, declaration, string(fields["output_schema"]))
	require.NotContains(t, fields, "semantic_version", "import must not manufacture a replacement by duplicating resource identity")

	var detailJSON types.String
	diagnostics := diag.Diagnostics{}
	require.True(t, setJSONString(&diagnostics, &detailJSON, detail))
	require.NoError(t, json.Unmarshal([]byte(detailJSON.ValueString()), &fields))
	require.JSONEq(t, declaration, string(fields["output_schema"]), "data-source definition JSON must retain the interface too")
}

func TestModuleVersionGeneratedDetailImportDistinguishesArtifactClaims(t *testing.T) {
	t.Parallel()

	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, test := range []struct {
		name, source, metadataDigest string
		wantClaim                    bool
	}{
		{name: "inline computed metadata", source: "inline", metadataDigest: digest},
		{name: "external declared digest", source: "git::https://example.invalid/module.git", metadataDigest: digest, wantClaim: true},
		{name: "external absent digest", source: "git::https://example.invalid/module.git"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			encoded, err := json.Marshal(map[string]any{
				"module_source": test.source, "output_schema": map[string]any{},
				"version": map[string]any{"opaque_version_id": "1.0.0", "artifact_digest": test.metadataDigest},
			})
			require.NoError(t, err)
			detail := parseModuleVersionDetail(t, string(encoded))
			body := moduleVersionPublishBody(*detail)
			require.Equal(t, test.wantClaim, body.ArtifactDigest != nil)
			if test.wantClaim {
				require.Equal(t, digest, *body.ArtifactDigest)
			}
			state := ModuleVersionResourceModel{Definition: jsontypes.NewNormalizedNull()}
			require.NoError(t, applyModuleVersionDefinition(&state, body))
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(state.Definition.ValueString()), &fields))
			_, hasClaim := fields["artifact_digest"]
			require.Equal(t, test.wantClaim, hasClaim)
			require.Equal(t, test.metadataDigest, detail.Version.ArtifactDigest, "read-only metadata remains intact, not reclassified as an author claim")
		})
	}
}

func TestModuleVersionCoordinatePreservesCanonicalAndLegacyIdentity(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, metadata, want string
	}{
		{name: "migrated v1", metadata: `,"semantic_version":"1.0.0"`, want: "1.0.0"},
		{name: "retained v0 absent", want: "opaque-legacy-version"},
		{name: "retained empty metadata", metadata: `,"semantic_version":""`, want: "opaque-legacy-version"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			detail := parseModuleVersionDetail(t, `{"version":{"opaque_version_id":"opaque-legacy-version"`+test.metadata+`}}`)
			state := ModuleVersionResourceModel{}
			applyModuleVersionState(&state, detail.Version)
			require.Equal(t, test.want, state.SemanticVersion.ValueString())
			require.Equal(t, test.want, moduleVersionPublishBody(*detail).SemanticVersion)
			require.Equal(t, "opaque-legacy-version", detail.Version.OpaqueVersionId)
		})
	}
}

func TestModuleVersionDefinitionRejectsUnknownFieldsAndPreservesDeclarations(t *testing.T) {
	for _, value := range []string{`{"output_schmea":{}}`, `{"module_params":{"name":{"type":"string","is_optionl":true}}}`, `{} {}`, `null`, `[]`} {
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

	configured := `{"semantic_version":"1.0.0","module_params":{"required":{"is_optional":false,"type":"map"}}}`
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
		"module_source":"inline",
		"module_inputs":null,
		"module_params":null,
		"provider_mapping":null,
		"dependencies":null,
		"coprovisioned":null
	}`, state.Definition.ValueString())
}

func TestModuleVersionImportRetainsExplicitResourceTypeContract(t *testing.T) {
	detail := parseModuleVersionDetail(t, `{"version":{"semantic_version":"1.2.0","resource_type_version":2},"module_source":"inline","module_source_code":"output \"name\" { value = \"example\" }"}`)
	body := moduleVersionPublishBody(*detail)
	require.NotNil(t, body.ResourceTypeVersion)
	require.Equal(t, int64(2), *body.ResourceTypeVersion)
	state := ModuleVersionResourceModel{Definition: jsontypes.NewNormalizedNull()}
	require.NoError(t, applyModuleVersionDefinition(&state, body))
	var restored map[string]any
	require.NoError(t, json.Unmarshal([]byte(state.Definition.ValueString()), &restored))
	require.Equal(t, float64(2), restored["resource_type_version"])
	require.NotContains(t, restored, "semantic_version")
	legacy := moduleVersionPublishBody(cp.CoreModuleVersionDetail{})
	require.Nil(t, legacy.ResourceTypeVersion, "legacy imports must not invent a missing contract claim")
}
