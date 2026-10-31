package provider

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/require"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
)

func TestDraftRefreshDetectsActualRemoteEdits(t *testing.T) {
	state := ModuleDraftResourceModel{SemanticVersion: types.StringValue("1.2.0"), Definition: jsontypes.NewNormalizedValue(`{"module_source":"inline","module_source_code":"output \"name\" { value = \"first\" }"}`)}
	body, err := draftPublicationBody(state)
	require.NoError(t, err)
	body.ModuleSourceCode = ptrString(`output "name" { value = "second" }`)
	draft := cp.ModuleDraft{Id: uuid.New(), ModuleSlug: "storage", ResourceVersion: 2, Status: cp.ModuleDraftStatusOpen, Definition: body}
	require.NoError(t, applyModuleDraftState(&state, draft))
	require.Equal(t, int64(2), state.ResourceVersion.ValueInt64())
	updated, err := draftPublicationBody(state)
	require.NoError(t, err)
	require.Equal(t, body.ModuleSourceCode, updated.ModuleSourceCode, "refresh must expose remote edits instead of preserving stale configured source")
	var definition map[string]any
	require.NoError(t, json.Unmarshal([]byte(state.Definition.ValueString()), &definition))
	require.NotContains(t, definition, "semantic_version")
}

func TestDraftRefreshPreservesEquivalentExplicitFalseSpelling(t *testing.T) {
	configured := `{"module_source":"inline","module_source_code":"output \"name\" { value = \"first\" }","coprovisioned":[{"type":"database","is_dependent_on_current":false,"copy_dependents_from_current":false}]}`
	state := ModuleDraftResourceModel{SemanticVersion: types.StringValue("1.2.0"), Definition: jsontypes.NewNormalizedValue(configured)}
	body, err := draftPublicationBody(state)
	require.NoError(t, err)
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	var returned cp.ModuleVersionPublishBody
	require.NoError(t, json.Unmarshal(encoded, &returned))
	require.NoError(t, applyModuleDraftState(&state, cp.ModuleDraft{Id: uuid.New(), Definition: returned, ResourceVersion: 1, Status: cp.ModuleDraftStatusOpen}))
	require.Equal(t, configured, state.Definition.ValueString(), "API default omission must not manufacture a definition change")
}

func TestDraftDefinitionRejectsAmbiguousSemanticVersion(t *testing.T) {
	state := ModuleDraftResourceModel{SemanticVersion: types.StringValue("1.2.0"), Definition: jsontypes.NewNormalizedValue(`{"semantic_version":"2.0.0","module_source":"inline"}`)}
	_, err := draftPublicationBody(state)
	require.ErrorContains(t, err, "top-level resource attribute")
}

func TestDraftSchemaRoundTripsProviderState(t *testing.T) {
	r := &ModuleDraftResource{}
	var response resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &response)
	state := tfsdk.State{Schema: response.Schema}
	wanted := ModuleDraftResourceModel{ID: types.StringValue(uuid.NewString()), ModuleID: types.StringValue("storage"),
		AuthoringKey: types.StringValue("storage-1.2.0"), SemanticVersion: types.StringValue("1.2.0"),
		Definition: jsontypes.NewNormalizedValue(`{"module_source":"inline"}`), Reason: types.StringValue("Review"),
		Status: types.StringValue("open"), ResourceVersion: types.Int64Value(2), PublishedVersionUUID: types.StringNull()}
	require.False(t, state.Set(t.Context(), &wanted).HasError())
	var actual ModuleDraftResourceModel
	require.False(t, state.Get(t.Context(), &actual).HasError())
	require.Equal(t, wanted, actual)
}

func ptrString(value string) *string { return &value }
