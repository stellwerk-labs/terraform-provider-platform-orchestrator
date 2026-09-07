package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSource = &ModuleVersionDataSource{}

func NewModuleVersionDataSource() datasource.DataSource { return &ModuleVersionDataSource{} }

type ModuleVersionDataSource struct {
	cpClient cp.ClientWithResponsesInterface
	orgID    string
}

type ModuleVersionDataSourceModel struct {
	ID              types.String `tfsdk:"id"`
	ModuleID        types.String `tfsdk:"module_id"`
	SemanticVersion types.String `tfsdk:"semantic_version"`
	LifecycleStatus types.String `tfsdk:"lifecycle_status"`
	Verification    types.String `tfsdk:"verification_status"`
	ResourceVersion types.Int64  `tfsdk:"resource_version"`
	DefinitionJSON  types.String `tfsdk:"definition_json"`
	LifecycleJSON   types.String `tfsdk:"lifecycle_events_json"`
	UsageJSON       types.String `tfsdk:"usage_json"`
	CompareTo       types.String `tfsdk:"compare_to"`
	ComparisonJSON  types.String `tfsdk:"comparison_json"`
}

func (d *ModuleVersionDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_module_version"
}

func (d *ModuleVersionDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads one immutable Core Module Version together with its append-only lifecycle, current adoption and optional structural comparison. This is normal OSS Core functionality and does not require an add-on.",
		Attributes: map[string]schema.Attribute{
			"id":                    schema.StringAttribute{Required: true, MarkdownDescription: "Module Version UUID or canonical SemVer."},
			"module_id":             schema.StringAttribute{Required: true, MarkdownDescription: "Module technical slug."},
			"semantic_version":      schema.StringAttribute{Computed: true},
			"lifecycle_status":      schema.StringAttribute{Computed: true},
			"verification_status":   schema.StringAttribute{Computed: true},
			"resource_version":      schema.Int64Attribute{Computed: true},
			"definition_json":       schema.StringAttribute{Computed: true, MarkdownDescription: "Complete immutable version definition as JSON."},
			"lifecycle_events_json": schema.StringAttribute{Computed: true, MarkdownDescription: "Complete append-only lifecycle timeline as JSON."},
			"usage_json":            schema.StringAttribute{Computed: true, MarkdownDescription: "Current adoption, Environment, Deployment, Pin and unknown-state evidence as JSON."},
			"compare_to":            schema.StringAttribute{Optional: true, MarkdownDescription: "Optional second Version UUID or SemVer for a server-owned structural comparison."},
			"comparison_json":       schema.StringAttribute{Computed: true, MarkdownDescription: "Typed before/after structural comparison when compare_to is set."},
		},
	}
}

func (d *ModuleVersionDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*PlatformOrchestratorProviderData)
	if !ok {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, fmt.Sprintf("Expected *PlatformOrchestratorProviderData, got %T", req.ProviderData))
		return
	}
	d.cpClient, d.orgID = data.CpClient, data.OrgId
}

func (d *ModuleVersionDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state ModuleVersionDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	detail, err := d.cpClient.GetModuleVersionWithResponse(ctx, d.orgID, state.ModuleID.ValueString(), state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to read Module Version: "+err.Error())
		return
	}
	if detail.StatusCode() != http.StatusOK || detail.JSON200 == nil {
		addAPIResponseError(&resp.Diagnostics, "read Module Version", detail.StatusCode(), detail.Body)
		return
	}
	events, err := d.cpClient.ListModuleVersionLifecycleEventsWithResponse(ctx, d.orgID, state.ModuleID.ValueString(), state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to read Module Version lifecycle: "+err.Error())
		return
	}
	if events.StatusCode() != http.StatusOK || events.JSON200 == nil {
		addAPIResponseError(&resp.Diagnostics, "read Module Version lifecycle", events.StatusCode(), events.Body)
		return
	}
	usage, err := d.cpClient.GetModuleVersionUsageWithResponse(ctx, d.orgID, state.ModuleID.ValueString(), state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to read Module Version usage: "+err.Error())
		return
	}
	if usage.StatusCode() != http.StatusOK || usage.JSON200 == nil {
		addAPIResponseError(&resp.Diagnostics, "read Module Version usage", usage.StatusCode(), usage.Body)
		return
	}
	state.ID = types.StringValue(detail.JSON200.Version.Uuid.String())
	state.SemanticVersion = types.StringValue(detail.JSON200.Version.OpaqueVersionId)
	state.LifecycleStatus = types.StringValue(string(detail.JSON200.Version.LifecycleStatus))
	state.Verification = types.StringValue(string(detail.JSON200.Version.VerificationStatus))
	state.ResourceVersion = types.Int64Value(detail.JSON200.Version.ResourceVersion)
	if !setJSONString(&resp.Diagnostics, &state.DefinitionJSON, detail.JSON200) ||
		!setJSONString(&resp.Diagnostics, &state.LifecycleJSON, events.JSON200) ||
		!setJSONString(&resp.Diagnostics, &state.UsageJSON, usage.JSON200) {
		return
	}
	if state.CompareTo.ValueString() == "" {
		state.ComparisonJSON = types.StringNull()
	} else {
		comparison, err := d.cpClient.CompareModuleVersionsWithResponse(ctx, d.orgID, state.ModuleID.ValueString(), state.ID.ValueString(), state.CompareTo.ValueString())
		if err != nil {
			resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to compare Module Versions: "+err.Error())
			return
		}
		if comparison.StatusCode() != http.StatusOK || comparison.JSON200 == nil {
			addAPIResponseError(&resp.Diagnostics, "compare Module Versions", comparison.StatusCode(), comparison.Body)
			return
		}
		if !setJSONString(&resp.Diagnostics, &state.ComparisonJSON, comparison.JSON200) {
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func setJSONString(diagnostics *diag.Diagnostics, target *types.String, value any) bool {
	encoded, err := json.Marshal(value)
	if err != nil {
		diagnostics.AddError(PO_PROVIDER_ERR, "Unable to encode API response as JSON: "+err.Error())
		return false
	}
	*target = types.StringValue(string(encoded))
	return true
}
