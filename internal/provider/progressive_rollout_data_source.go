package provider

import (
	"context"
	"fmt"

	rollouts "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/progressive-rollouts"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSource = &ProgressiveRolloutDataSource{}

func NewProgressiveRolloutDataSource() datasource.DataSource { return &ProgressiveRolloutDataSource{} }

type ProgressiveRolloutDataSource struct {
	client progressiveRolloutsClient
}

type ProgressiveRolloutDataSourceModel struct {
	ID                  types.String `tfsdk:"id"`
	Name                types.String `tfsdk:"name"`
	State               types.String `tfsdk:"state"`
	ResourceVersion     types.Int64  `tfsdk:"resource_version"`
	CurrentPlanRevision types.Int64  `tfsdk:"current_plan_revision"`
	SpecJSON            types.String `tfsdk:"spec_json"`
	PlanJSON            types.String `tfsdk:"plan_json"`
	StatusJSON          types.String `tfsdk:"status_json"`
	ApprovalsJSON       types.String `tfsdk:"approvals_json"`
	ConflictsJSON       types.String `tfsdk:"conflicts_json"`
	ExecutionsJSON      types.String `tfsdk:"executions_json"`
	EventsJSON          types.String `tfsdk:"events_json"`
	MetricsJSON         types.String `tfsdk:"metrics_json"`
}

func (d *ProgressiveRolloutDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_progressive_rollout"
}

func (d *ProgressiveRolloutDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Enterprise: reads a Progressive Rollout with its current immutable Plan, approvals, conflicts, executions, audit events and aggregate metrics. " + progressiveRolloutsRequirement,
		Attributes: map[string]schema.Attribute{
			"id":                    schema.StringAttribute{Required: true, MarkdownDescription: "Rollout UUID."},
			"name":                  schema.StringAttribute{Computed: true, MarkdownDescription: "Rollout name from its spec."},
			"state":                 schema.StringAttribute{Computed: true, MarkdownDescription: "Actual server-owned Rollout state."},
			"resource_version":      schema.Int64Attribute{Computed: true, MarkdownDescription: "Optimistic-concurrency version."},
			"current_plan_revision": schema.Int64Attribute{Computed: true, MarkdownDescription: "Current immutable numbered Plan revision, null while the Draft has not been prepared."},
			"spec_json":             schema.StringAttribute{Computed: true, MarkdownDescription: "ProgressiveRolloutSpec as JSON."},
			"plan_json":             schema.StringAttribute{Computed: true, MarkdownDescription: "Current immutable Plan as JSON, null before preparation."},
			"status_json":           schema.StringAttribute{Computed: true, MarkdownDescription: "Server-owned progress and terminal-result projection as JSON. Includes `entitlement_warning` (with `grace_until`) while the plugin licence is in its grace period and `entitlement_suspension` (licence state, `suspended_at`, `resume_available`) while the Rollout is paused because Core stopped allowing forward work."},
			"approvals_json":        schema.StringAttribute{Computed: true, MarkdownDescription: "Approvals of the current Plan as JSON."},
			"conflicts_json":        schema.StringAttribute{Computed: true, MarkdownDescription: "Conflicts of the current Plan as JSON."},
			"executions_json":       schema.StringAttribute{Computed: true, MarkdownDescription: "Environment executions of the current Plan as JSON."},
			"events_json":           schema.StringAttribute{Computed: true, MarkdownDescription: "Audit events as JSON."},
			"metrics_json":          schema.StringAttribute{Computed: true, MarkdownDescription: "Aggregate Rollout metrics as JSON."},
		},
	}
}

func (d *ProgressiveRolloutDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*PlatformOrchestratorProviderData)
	if !ok {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, fmt.Sprintf("Expected *PlatformOrchestratorProviderData, got %T", req.ProviderData))
		return
	}
	d.client = progressiveRolloutsClient{cp: data.CpClient, orgID: data.OrgId}
}

func (d *ProgressiveRolloutDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state ProgressiveRolloutDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || !d.client.require(ctx, &resp.Diagnostics, "getProgressiveRollout") {
		return
	}
	detail, ok := d.client.get(ctx, state.ID.ValueString(), &resp.Diagnostics)
	if !ok {
		return
	}
	applyProgressiveRolloutDataSourceState(&resp.Diagnostics, &state, detail)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func applyProgressiveRolloutDataSourceState(diagnostics *diag.Diagnostics, state *ProgressiveRolloutDataSourceModel, detail rollouts.ProgressiveRolloutDetail) {
	state.ID = types.StringValue(detail.Rollout.Id.String())
	state.Name = types.StringValue(detail.Rollout.Spec.Name)
	state.State = types.StringValue(string(detail.Rollout.State))
	state.ResourceVersion = types.Int64Value(detail.Rollout.ResourceVersion)
	if detail.Rollout.CurrentPlanRevision == nil {
		state.CurrentPlanRevision = types.Int64Null()
	} else {
		state.CurrentPlanRevision = types.Int64Value(int64(*detail.Rollout.CurrentPlanRevision))
	}
	state.PlanJSON = types.StringNull()
	if detail.Plan != nil && !setJSONString(diagnostics, &state.PlanJSON, detail.Plan) {
		return
	}
	for _, field := range []struct {
		target *types.String
		value  any
	}{
		{&state.SpecJSON, detail.Rollout.Spec},
		{&state.StatusJSON, detail.Rollout.Status},
		{&state.ApprovalsJSON, detail.Approvals},
		{&state.ConflictsJSON, detail.Conflicts},
		{&state.ExecutionsJSON, detail.Executions},
		{&state.EventsJSON, detail.Events},
		{&state.MetricsJSON, detail.Metrics},
	} {
		if !setJSONString(diagnostics, field.target, field.value) {
			return
		}
	}
}
