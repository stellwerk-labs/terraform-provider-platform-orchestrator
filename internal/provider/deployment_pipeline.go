package provider

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/pipelines"
	dp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-dp"
)

func (d *DeploymentResource) deploymentPipeline(ctx context.Context, value types.String, project string) (*dp.DeploymentPipelineSelection, error) {
	if value.IsNull() {
		return nil, nil
	}
	if value.IsUnknown() {
		return nil, fmt.Errorf("pipeline_version_id must be known before starting a deployment")
	}
	id, err := uuid.Parse(value.ValueString())
	if err != nil || id == uuid.Nil {
		return nil, fmt.Errorf("pipeline_version_id must be a published version UUID")
	}
	response, err := d.pipelineClient.GetPublishedProjectPipelineWithResponse(ctx, d.orgId, project, id)
	if err != nil {
		return nil, fmt.Errorf("read published pipeline: %w", err)
	}
	if response.StatusCode() != 200 || response.JSON200 == nil {
		return nil, fmt.Errorf("read published pipeline returned HTTP %d; verify project pipeline permissions and the exact published version", response.StatusCode())
	}
	return publishedDeploymentSelection(*response.JSON200, id, project)
}

func publishedDeploymentSelection(published pipelines.PublishedPipelineVersion, id uuid.UUID, project string) (*dp.DeploymentPipelineSelection, error) {
	if published.Id != id || published.ProjectId != project || published.Version < 1 {
		return nil, fmt.Errorf("published pipeline identity does not match the selected project/version")
	}
	if published.RetiredAt != nil {
		return nil, fmt.Errorf("selected pipeline version is retired; choose an allowed published version")
	}
	// Ordinary DP uses {id,version}, not the Core/plugin {version_id,digest} reference.
	return &dp.DeploymentPipelineSelection{Id: id.String(), Version: published.Version}, nil
}
