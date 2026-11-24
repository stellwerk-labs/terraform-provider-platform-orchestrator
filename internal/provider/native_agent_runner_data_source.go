package provider

import (
	"context"
	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
)

type NativeAgentRunnerDataSource struct {
	client cp.ClientWithResponsesInterface
	org    string
}

func NewNativeAgentRunnerDataSource() datasource.DataSource { return &NativeAgentRunnerDataSource{} }
func (d *NativeAgentRunnerDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_native_agent_runner"
}
func (d *NativeAgentRunnerDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Reads a native Agent registration and its public immutable UUID. Does not enroll, initialize or activate execution.", Attributes: map[string]schema.Attribute{
		"id": schema.StringAttribute{Required: true, MarkdownDescription: "Runner ID in the configured organization, using the Core pattern and Native v2 profile of 2–128 characters.", Validators: []validator.String{stringvalidator.RegexMatches(nativeRunnerIDPattern, "must match the Core Runner ID pattern"), stringvalidator.LengthBetween(2, 128)}}, "uuid": schema.StringAttribute{Computed: true, MarkdownDescription: "Server-issued immutable registration UUID."}, "description": schema.StringAttribute{Computed: true},
		"runner_configuration": schema.SingleNestedAttribute{Computed: true, Attributes: map[string]schema.Attribute{
			"key": schema.StringAttribute{Computed: true}, "max_parallel_jobs": schema.Int64Attribute{Computed: true},
			"supported_protocols": schema.SetAttribute{Computed: true, ElementType: types.StringType}, "capabilities": schema.SetAttribute{Computed: true, ElementType: types.StringType},
			"native_execution": schema.StringAttribute{Computed: true, CustomType: jsontypes.NormalizedType{}},
		}}, "state_storage_configuration": commonRunnerStateStorageDataSourceSchema,
	}}
}
func (d *NativeAgentRunnerDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*PlatformOrchestratorProviderData)
	if !ok {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, "Expected provider configuration.")
		return
	}
	d.client, d.org = data.CpClient, data.OrgId
}
func (d *NativeAgentRunnerDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var id types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("id"), &id)...)
	if resp.Diagnostics.HasError() {
		return
	}
	actual, diags := readNativeAgent(ctx, d.client, d.org, id.ValueString(), types.StringNull())
	resp.Diagnostics.Append(diags...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &actual)...)
	}
}
