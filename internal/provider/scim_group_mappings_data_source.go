package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	iam "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-iam"
)

var _ datasource.DataSource = &ScimGroupMappingsDataSource{}

func NewScimGroupMappingsDataSource() datasource.DataSource { return &ScimGroupMappingsDataSource{} }

type ScimGroupMappingsDataSource struct {
	iamClient iam.ClientWithResponsesInterface
	orgId     string
}

type ScimGroupMappingsDataSourceModel struct {
	Mappings types.List `tfsdk:"mappings"`
}

func scimGroupMappingObjectType() types.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{"group_display_name": types.StringType, "role_id": types.StringType, "created_at": types.StringType}}
}

func (d *ScimGroupMappingsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_scim_group_mappings"
}

func (d *ScimGroupMappingsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Lists all SCIM group-to-role mappings in the organization.", Attributes: map[string]schema.Attribute{
		"mappings": schema.ListNestedAttribute{MarkdownDescription: "The SCIM group mappings.", Computed: true, NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"group_display_name": schema.StringAttribute{Computed: true, MarkdownDescription: "The SCIM group's display name."},
			"role_id":            schema.StringAttribute{Computed: true, MarkdownDescription: "The mapped role UUID."},
			"created_at":         schema.StringAttribute{Computed: true, MarkdownDescription: "The mapping creation time."},
		}}},
	}}
}

func (d *ScimGroupMappingsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	providerData, ok := req.ProviderData.(*PlatformOrchestratorProviderData)
	if !ok {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, fmt.Sprintf("Expected *PlatformOrchestratorProviderData, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}
	d.iamClient = providerData.IamClient
	d.orgId = providerData.OrgId
}

func (d *ScimGroupMappingsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data ScimGroupMappingsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	httpResp, err := d.iamClient.ListScimGroupMappingsWithResponse(ctx, d.orgId)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to list SCIM group mappings: %s", err))
		return
	}
	if httpResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to list SCIM group mappings, unexpected status code: %d, body: %s", httpResp.StatusCode(), httpResp.Body))
		return
	}
	mappings := make([]attr.Value, 0, len(httpResp.JSON200.Items))
	for _, mapping := range httpResp.JSON200.Items {
		value, diags := types.ObjectValueFrom(ctx, scimGroupMappingObjectType().AttrTypes, toScimGroupMappingModel(mapping))
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		mappings = append(mappings, value)
	}
	data.Mappings = types.ListValueMust(scimGroupMappingObjectType(), mappings)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
