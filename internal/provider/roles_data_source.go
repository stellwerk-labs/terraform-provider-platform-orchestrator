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

var _ datasource.DataSource = &RolesDataSource{}

func NewRolesDataSource() datasource.DataSource { return &RolesDataSource{} }

type RolesDataSource struct {
	iamClient iam.ClientWithResponsesInterface
	orgId     string
}

type RolesDataSourceModel struct {
	Roles types.List `tfsdk:"roles"`
}

func (d *RolesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_roles"
}

func (d *RolesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Lists all organization roles.", Attributes: map[string]schema.Attribute{
		"roles": schema.ListNestedAttribute{MarkdownDescription: "The organization roles.", Computed: true, NestedObject: schema.NestedAttributeObject{Attributes: roleDataSourceAttributes(false)}},
	}}
}

func (d *RolesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func roleObjectType() types.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"id": types.StringType, "display_name": types.StringType, "permissions": types.SetType{ElemType: types.StringType},
		"is_system": types.BoolType, "created_at": types.StringType, "created_by": types.StringType,
	}}
}

func (d *RolesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data RolesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var roles []attr.Value
	var page *string
	for {
		httpResp, err := d.iamClient.ListRolesWithResponse(ctx, d.orgId, &iam.ListRolesParams{Page: page})
		if err != nil {
			resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to list roles: %s", err))
			return
		}
		if httpResp.StatusCode() != http.StatusOK {
			resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to list roles, unexpected status code: %d, body: %s", httpResp.StatusCode(), httpResp.Body))
			return
		}
		for _, role := range httpResp.JSON200.Items {
			value, diags := types.ObjectValueFrom(ctx, roleObjectType().AttrTypes, toRoleModel(role))
			resp.Diagnostics.Append(diags...)
			if resp.Diagnostics.HasError() {
				return
			}
			roles = append(roles, value)
		}
		page = httpResp.JSON200.NextPageToken
		if page == nil || *page == "" {
			break
		}
	}
	data.Roles = types.ListValueMust(roleObjectType(), roles)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
