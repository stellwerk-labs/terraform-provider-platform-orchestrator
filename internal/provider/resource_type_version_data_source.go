package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
)

var _ datasource.DataSource = &ResourceTypeVersionDataSource{}

func NewResourceTypeVersionDataSource() datasource.DataSource {
	return &ResourceTypeVersionDataSource{}
}

type ResourceTypeVersionDataSource struct {
	cpClient cp.ClientWithResponsesInterface
	orgID    string
}

type ResourceTypeVersionDataSourceModel struct {
	ResourceTypeID types.String `tfsdk:"resource_type_id"`
	Version        types.Int64  `tfsdk:"version"`
	UUID           types.String `tfsdk:"uuid"`
	OutputSchema   types.String `tfsdk:"output_schema"`
	ModuleContract types.String `tfsdk:"module_contract"`
}

func (d *ResourceTypeVersionDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_resource_type_version"
}

func (d *ResourceTypeVersionDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Reads an exact immutable Resource Type interface. The version is an interface number, not the catalogue resource_version used for concurrency.", Attributes: map[string]schema.Attribute{
		"resource_type_id": schema.StringAttribute{Required: true},
		"version":          schema.Int64Attribute{Required: true, MarkdownDescription: "Exact positive interface version; never resolves latest."},
		"uuid":             schema.StringAttribute{Computed: true, MarkdownDescription: "Immutable interface UUID retained by bound Module Versions."},
		"output_schema":    schema.StringAttribute{Computed: true, MarkdownDescription: "Exact output contract as JSON."},
		"module_contract":  schema.StringAttribute{Computed: true, MarkdownDescription: "Optional Module declaration contract as JSON. Absence remains null."},
	}}
}

func (d *ResourceTypeVersionDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *ResourceTypeVersionDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state ResourceTypeVersionDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.Version.ValueInt64() < 1 {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Resource Type version must be a positive integer")
		return
	}
	result, err := d.cpClient.GetResourceTypeVersionWithResponse(ctx, d.orgID, state.ResourceTypeID.ValueString(), state.Version.ValueInt64())
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, err.Error())
		return
	}
	if result.StatusCode() != http.StatusOK || result.JSON200 == nil {
		addAPIResponseError(&resp.Diagnostics, "read Resource Type version", result.StatusCode(), result.Body)
		return
	}
	state.UUID = types.StringValue(result.JSON200.Uuid.String())
	if !setJSONString(&resp.Diagnostics, &state.OutputSchema, result.JSON200.OutputSchema) {
		return
	}
	state.ModuleContract = types.StringNull()
	if result.JSON200.ModuleContract != nil && !setJSONString(&resp.Diagnostics, &state.ModuleContract, *result.JSON200.ModuleContract) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

var _ datasource.DataSource = &ResourceTypeVersionsDataSource{}

func NewResourceTypeVersionsDataSource() datasource.DataSource {
	return &ResourceTypeVersionsDataSource{}
}

type ResourceTypeVersionsDataSource struct {
	cpClient cp.ClientWithResponsesInterface
	orgID    string
}

type ResourceTypeVersionsDataSourceModel struct {
	ResourceTypeID types.String `tfsdk:"resource_type_id"`
	VersionsJSON   types.String `tfsdk:"versions_json"`
}

func (d *ResourceTypeVersionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_resource_type_versions"
}

func (d *ResourceTypeVersionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Lists retained Resource Type interface history, including immutable UUIDs, declarations and recorded publication actors/reasons.", Attributes: map[string]schema.Attribute{
		"resource_type_id": schema.StringAttribute{Required: true},
		"versions_json":    schema.StringAttribute{Computed: true, MarkdownDescription: "Complete interface history in ascending version order as a JSON array."},
	}}
}

func (d *ResourceTypeVersionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *ResourceTypeVersionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state ResourceTypeVersionsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	items, page := make([]cp.ResourceTypeVersion, 0), ""
	for {
		params := &cp.ListResourceTypeVersionsParams{}
		if page != "" {
			params.Page = &page
		}
		result, err := d.cpClient.ListResourceTypeVersionsWithResponse(ctx, d.orgID, state.ResourceTypeID.ValueString(), params)
		if err != nil {
			resp.Diagnostics.AddError(PO_CLIENT_ERR, err.Error())
			return
		}
		if result.StatusCode() != http.StatusOK || result.JSON200 == nil {
			addAPIResponseError(&resp.Diagnostics, "list Resource Type versions", result.StatusCode(), result.Body)
			return
		}
		items = append(items, result.JSON200.Items...)
		if result.JSON200.NextPageToken == nil || *result.JSON200.NextPageToken == "" {
			break
		}
		page = *result.JSON200.NextPageToken
	}
	if !setJSONString(&resp.Diagnostics, &state.VersionsJSON, items) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
