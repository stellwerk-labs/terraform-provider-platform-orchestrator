package provider

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/pipelines"
	dp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-dp"
	"github.com/stretchr/testify/require"
)

const deploymentPipelineTestProject = "owned"

func TestPublishedDeploymentSelectionUsesOrdinaryContract(t *testing.T) {
	id := uuid.New()
	published := pipelines.PublishedPipelineVersion{Id: id, ProjectId: deploymentPipelineTestProject, Version: 7, Digest: "server-owned-digest"}
	selected, err := publishedDeploymentSelection(published, id, deploymentPipelineTestProject)
	require.NoError(t, err)
	body := dp.DeploymentCreateBody{ProjectId: deploymentPipelineTestProject, EnvId: "target", Mode: dp.DeploymentCreateBodyModeDeploy, Pipeline: selected}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	var document map[string]any
	require.NoError(t, json.Unmarshal(raw, &document))
	require.Equal(t, map[string]any{"id": id.String(), "version": float64(7)}, document["pipeline"])
	wrapped, err := json.Marshal(dp.ModuleTestDeploymentCreateBody{Deployment: body, DraftTestSnapshots: map[string]uuid.UUID{uuid.NewString(): uuid.New()}})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(wrapped, &document))
	inner, ok := document["deployment"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, map[string]any{"id": id.String(), "version": float64(7)}, inner["pipeline"])
}
func TestPublishedDeploymentSelectionRejectsChangedIdentityOrRetirement(t *testing.T) {
	id := uuid.New()
	now := time.Now()
	for _, tc := range []struct {
		name  string
		value pipelines.PublishedPipelineVersion
	}{
		{"other version", pipelines.PublishedPipelineVersion{Id: uuid.New(), ProjectId: deploymentPipelineTestProject, Version: 1}},
		{"other project", pipelines.PublishedPipelineVersion{Id: id, ProjectId: "other", Version: 1}},
		{"invalid numeric version", pipelines.PublishedPipelineVersion{Id: id, ProjectId: deploymentPipelineTestProject}},
		{"retired", pipelines.PublishedPipelineVersion{Id: id, ProjectId: deploymentPipelineTestProject, Version: 1, RetiredAt: &now}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := publishedDeploymentSelection(tc.value, id, deploymentPipelineTestProject)
			require.Error(t, err)
		})
	}
}
func TestDeploymentPipelineOmissionAndInvalidValuesNeedNoAPI(t *testing.T) {
	r := &DeploymentResource{}
	selected, err := r.deploymentPipeline(t.Context(), types.StringNull(), deploymentPipelineTestProject)
	require.NoError(t, err)
	require.Nil(t, selected)
	for _, value := range []types.String{types.StringUnknown(), types.StringValue(""), types.StringValue("invalid"), types.StringValue(uuid.Nil.String())} {
		_, err := r.deploymentPipeline(t.Context(), value, deploymentPipelineTestProject)
		require.Error(t, err)
	}
}
