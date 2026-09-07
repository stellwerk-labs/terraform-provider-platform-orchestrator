package provider

import (
	"context"
	"fmt"
	"net/http"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSource = &ModuleVersionsDataSource{}

func NewModuleVersionsDataSource() datasource.DataSource { return &ModuleVersionsDataSource{} }

type ModuleVersionsDataSource struct {
	cpClient cp.ClientWithResponsesInterface
	orgID    string
}

type ModuleVersionsDataSourceModel struct {
	ModuleID          types.String `tfsdk:"module_id"`
	IncludeDeprecated types.Bool   `tfsdk:"include_deprecated"`
	IncludeDefective  types.Bool   `tfsdk:"include_defective"`
	VersionsJSON      types.String `tfsdk:"versions_json"`
}

func (d *ModuleVersionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_module_versions"
}

func (d *ModuleVersionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists complete immutable Core Module Version history using server-side pagination. This is normal OSS Core functionality.",
		Attributes: map[string]schema.Attribute{
			"module_id":          schema.StringAttribute{Required: true},
			"include_deprecated": schema.BoolAttribute{Optional: true},
			"include_defective":  schema.BoolAttribute{Optional: true},
			"versions_json":      schema.StringAttribute{Computed: true, MarkdownDescription: "All matching version details as a JSON array."},
		},
	}
}

func (d *ModuleVersionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *ModuleVersionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state ModuleVersionsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	items := make([]cp.CoreModuleVersionDetail, 0)
	page := ""
	for {
		params := &cp.ListModuleVersionsParams{
			IncludeDeprecated: boolPointer(state.IncludeDeprecated), IncludeDefective: boolPointer(state.IncludeDefective),
		}
		if page != "" {
			params.Page = &page
		}
		response, err := d.cpClient.ListModuleVersionsWithResponse(ctx, d.orgID, state.ModuleID.ValueString(), params)
		if err != nil {
			resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to list Module Versions: "+err.Error())
			return
		}
		if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
			addAPIResponseError(&resp.Diagnostics, "list Module Versions", response.StatusCode(), response.Body)
			return
		}
		items = append(items, response.JSON200.Items...)
		if response.JSON200.NextPageToken == nil || *response.JSON200.NextPageToken == "" {
			break
		}
		page = *response.JSON200.NextPageToken
	}
	if !setJSONString(&resp.Diagnostics, &state.VersionsJSON, items) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func boolPointer(value types.Bool) *bool {
	if value.IsNull() || value.IsUnknown() {
		return nil
	}
	result := value.ValueBool()
	return &result
}
