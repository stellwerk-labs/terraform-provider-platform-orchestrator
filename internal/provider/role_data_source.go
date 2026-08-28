package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	iam "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-iam"
)

var _ datasource.DataSource = &RoleDataSource{}

func NewRoleDataSource() datasource.DataSource { return &RoleDataSource{} }

type RoleDataSource struct {
	iamClient iam.ClientWithResponsesInterface
	orgId     string
}

func roleDataSourceAttributes(idRequired bool) map[string]dsschema.Attribute {
	return map[string]dsschema.Attribute{
		"id":           dsschema.StringAttribute{MarkdownDescription: "The role UUID.", Required: idRequired, Computed: !idRequired},
		"display_name": dsschema.StringAttribute{MarkdownDescription: "The role's display name.", Computed: true},
		"permissions":  dsschema.SetAttribute{MarkdownDescription: "The granular permission identifiers assigned to the role.", Computed: true, ElementType: types.StringType},
		"is_system":    dsschema.BoolAttribute{MarkdownDescription: "Whether this is an immutable built-in role.", Computed: true},
		"created_at":   dsschema.StringAttribute{MarkdownDescription: "The time the role was created.", Computed: true},
		"created_by":   dsschema.StringAttribute{MarkdownDescription: "The UUID of the user that created the role.", Computed: true},
	}
}

func (d *RoleDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_role"
}

func (d *RoleDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dsschema.Schema{MarkdownDescription: "Looks up an organization role by UUID.", Attributes: roleDataSourceAttributes(true)}
}

func (d *RoleDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *RoleDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data RoleModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	roleID, err := uuid.Parse(data.Id.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, fmt.Sprintf("Role ID must be a UUID: %s", err))
		return
	}
	httpResp, err := d.iamClient.GetRoleWithResponse(ctx, d.orgId, roleID)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to read role: %s", err))
		return
	}
	if httpResp.StatusCode() == http.StatusNotFound {
		resp.Diagnostics.AddError(PO_RESOURCE_NOT_FOUND_ERR, fmt.Sprintf("Role %s not found in org %s", roleID, d.orgId))
		return
	}
	if httpResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to read role, unexpected status code: %d, body: %s", httpResp.StatusCode(), httpResp.Body))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, toRoleModel(*httpResp.JSON200))...)
}
