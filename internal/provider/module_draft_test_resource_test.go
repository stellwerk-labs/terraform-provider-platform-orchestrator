package provider

import (
	dp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-dp"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/require"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
)

func TestModuleDraftTestCaptureIdentityIncludesScopeAndReviewedRevision(t *testing.T) {
	original := ModuleDraftTestResourceModel{ModuleID: types.StringValue("greeting"), CaptureKey: types.StringValue("review"), ProjectID: types.StringValue("project"), EnvironmentID: types.StringValue("development"), DraftID: types.StringValue(uuid.NewString()), ExpectedRevision: types.Int64Value(1), Reason: types.StringValue("Review A")}
	first := draftTestCaptureKey("org", original)
	require.Equal(t, first, draftTestCaptureKey("org", original))
	for _, change := range []func(*ModuleDraftTestResourceModel){
		func(m *ModuleDraftTestResourceModel) { m.ExpectedRevision = types.Int64Value(2) },
		func(m *ModuleDraftTestResourceModel) { m.EnvironmentID = types.StringValue("production") },
		func(m *ModuleDraftTestResourceModel) { m.DraftID = types.StringValue(uuid.NewString()) },
		func(m *ModuleDraftTestResourceModel) { m.CaptureKey = types.StringValue("another review") },
	} {
		candidate := original
		change(&candidate)
		require.NotEqual(t, first, draftTestCaptureKey("org", candidate))
	}
}

func TestModuleDraftTestStateRetainsDistinctExecutableAndScopeIdentities(t *testing.T) {
	snapshot := cp.ModuleDraftTestSnapshot{Id: uuid.New(), DraftId: uuid.New(), ModuleUuid: uuid.New(), EnvironmentUuid: uuid.New(), ProjectUuid: uuid.New(), DraftRevision: 3, ResourceTypeVersion: 2, ResourceTypeVersionUuid: uuid.New(), Reason: "Review exact B"}
	var state ModuleDraftTestResourceModel
	applyDraftTestState(&state, snapshot)
	require.Equal(t, snapshot.Id.String(), state.ID.ValueString())
	require.NotEqual(t, state.DraftID.ValueString(), state.ID.ValueString())
	require.Equal(t, snapshot.EnvironmentUuid.String(), state.EnvironmentUUID.ValueString())
	require.Equal(t, snapshot.ModuleUuid.String(), state.ModuleUUID.ValueString())
	require.Equal(t, int64(3), state.ExpectedRevision.ValueInt64())
	require.Equal(t, int64(2), state.ResourceTypeVersion.ValueInt64())
}

func TestDeploymentDraftSelectionDefersUnknownSnapshotButRejectsItAtApply(t *testing.T) {
	module, snapshot := uuid.NewString(), uuid.New()
	value := types.MapValueMust(types.StringType, map[string]attr.Value{module: types.StringUnknown()})
	var diagnostics diag.Diagnostics
	validateDeploymentDraftTests(value, true, &diagnostics)
	require.False(t, diagnostics.HasError())
	validateDeploymentDraftTests(value, false, &diagnostics)
	require.True(t, diagnostics.HasError())
	diagnostics = nil
	value = types.MapValueMust(types.StringType, map[string]attr.Value{module: types.StringValue(snapshot.String())})
	require.Equal(t, map[string]uuid.UUID{module: snapshot}, validateDeploymentDraftTests(value, false, &diagnostics))
	require.False(t, diagnostics.HasError())
	value = types.MapValueMust(types.StringType, map[string]attr.Value{"greeting": types.StringValue(snapshot.String())})
	validateDeploymentDraftTests(value, false, &diagnostics)
	require.True(t, diagnostics.HasError(), "a Module slug is not an immutable test binding")
}

func TestDeploymentDraftSelectionDoesNotFallBackToOlderServer(t *testing.T) {
	var ordinaryCalls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("POST /orgs/org/deployments", func(w http.ResponseWriter, _ *http.Request) { ordinaryCalls.Add(1); w.WriteHeader(http.StatusCreated) })
	server := httptest.NewServer(mux)
	defer server.Close()
	client, err := dp.NewClientWithResponses(server.URL)
	require.NoError(t, err)
	resource := &DeploymentResource{dpClient: client, orgId: "org"}
	data := DeploymentResourceModel{ProjectId: types.StringValue("project"), EnvId: types.StringValue("development"), Manifest: types.StringValue(`{"workloads":{},"shared":{}}`), DraftTests: types.MapValueMust(types.StringType, map[string]attr.Value{uuid.NewString(): types.StringValue(uuid.NewString())})}
	var diagnostics diag.Diagnostics
	resource.doDeployment(t.Context(), &data, &diagnostics)
	require.True(t, diagnostics.HasError())
	require.Zero(t, ordinaryCalls.Load(), "missing captured-test support must not deploy the ordinary Default")
}
