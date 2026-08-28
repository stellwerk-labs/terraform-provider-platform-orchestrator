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

var _ datasource.DataSource = &PermissionsDataSource{}

func NewPermissionsDataSource() datasource.DataSource { return &PermissionsDataSource{} }

type PermissionsDataSource struct {
	iamClient iam.ClientWithResponsesInterface
	orgId     string
}

type PermissionsDataSourceModel struct {
	Permissions types.List `tfsdk:"permissions"`
}

type PermissionModel struct {
	Id          types.String `tfsdk:"id"`
	DisplayName types.String `tfsdk:"display_name"`
	Description types.String `tfsdk:"description"`
	Category    types.String `tfsdk:"category"`
	Level       types.String `tfsdk:"level"`
	Scopes      types.Set    `tfsdk:"scopes"`
}

func permissionAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id":           schema.StringAttribute{MarkdownDescription: "The exact identifier accepted by a role's permissions set.", Computed: true},
		"display_name": schema.StringAttribute{MarkdownDescription: "The permission display name.", Computed: true},
		"description":  schema.StringAttribute{MarkdownDescription: "The permission description.", Computed: true},
		"category":     schema.StringAttribute{MarkdownDescription: "The permission category.", Computed: true},
		"level":        schema.StringAttribute{MarkdownDescription: "The legacy access level.", Computed: true},
		"scopes":       schema.SetAttribute{MarkdownDescription: "The assignment scopes where the permission is effective.", Computed: true, ElementType: types.StringType},
	}
}

func permissionObjectType() types.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"id": types.StringType, "display_name": types.StringType, "description": types.StringType,
		"category": types.StringType, "level": types.StringType, "scopes": types.SetType{ElemType: types.StringType},
	}}
}

func (d *PermissionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_permissions"
}

func (d *PermissionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Lists the authoritative catalog of permissions assignable to configurable roles.", Attributes: map[string]schema.Attribute{
		"permissions": schema.ListNestedAttribute{MarkdownDescription: "The permission catalog.", Computed: true, NestedObject: schema.NestedAttributeObject{Attributes: permissionAttributes()}},
	}}
}

func (d *PermissionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func toPermissionModel(permission iam.PermissionDefinition) PermissionModel {
	scopes := make([]attr.Value, len(permission.Scopes))
	for i, scope := range permission.Scopes {
		scopes[i] = types.StringValue(string(scope))
	}
	return PermissionModel{
		Id: types.StringValue(permission.Id), DisplayName: types.StringValue(permission.DisplayName),
		Description: types.StringValue(permission.Description), Category: types.StringValue(permission.Category),
		Level: types.StringValue(string(permission.Level)), Scopes: types.SetValueMust(types.StringType, scopes),
	}
}

func (d *PermissionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data PermissionsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	httpResp, err := d.iamClient.ListPermissionsWithResponse(ctx, d.orgId)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to list permissions: %s", err))
		return
	}
	if httpResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to list permissions, unexpected status code: %d, body: %s", httpResp.StatusCode(), httpResp.Body))
		return
	}
	permissions := make([]attr.Value, 0, len(httpResp.JSON200.Items))
	for _, permission := range httpResp.JSON200.Items {
		value, diags := types.ObjectValueFrom(ctx, permissionObjectType().AttrTypes, toPermissionModel(permission))
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		permissions = append(permissions, value)
	}
	data.Permissions = types.ListValueMust(permissionObjectType(), permissions)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
