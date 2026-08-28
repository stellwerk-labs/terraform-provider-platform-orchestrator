package provider

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	iam "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-iam"
)

var _ resource.Resource = &RoleResource{}
var _ resource.ResourceWithImportState = &RoleResource{}

func NewRoleResource() resource.Resource { return &RoleResource{} }

type RoleResource struct {
	iamClient iam.ClientWithResponsesInterface
	orgId     string
}

type RoleModel struct {
	Id          types.String `tfsdk:"id"`
	DisplayName types.String `tfsdk:"display_name"`
	Permissions types.Set    `tfsdk:"permissions"`
	IsSystem    types.Bool   `tfsdk:"is_system"`
	CreatedAt   types.String `tfsdk:"created_at"`
	CreatedBy   types.String `tfsdk:"created_by"`
}

func roleAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id": schema.StringAttribute{MarkdownDescription: "The role UUID.", Computed: true, PlanModifiers: []planmodifier.String{
			stringplanmodifier.UseStateForUnknown(),
		}},
		"display_name": schema.StringAttribute{MarkdownDescription: "The role's display name.", Required: true},
		"permissions":  schema.SetAttribute{MarkdownDescription: "The complete set of granular permission identifiers assigned to the role.", Required: true, ElementType: types.StringType},
		"is_system":    schema.BoolAttribute{MarkdownDescription: "Whether this is an immutable built-in role.", Computed: true},
		"created_at":   schema.StringAttribute{MarkdownDescription: "The time the role was created.", Computed: true},
		"created_by":   schema.StringAttribute{MarkdownDescription: "The UUID of the user that created the role.", Computed: true},
	}
}

func (r *RoleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_role"
}

func (r *RoleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "A configurable organization role.", Attributes: roleAttributes()}
}

func (r *RoleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	providerData, ok := req.ProviderData.(*PlatformOrchestratorProviderData)
	if !ok {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, fmt.Sprintf("Expected *PlatformOrchestratorProviderData, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}
	r.iamClient = providerData.IamClient
	r.orgId = providerData.OrgId
}

func roleWriteBody(ctx context.Context, data RoleModel) (iam.RoleWriteBody, diag.Diagnostics) {
	var permissions []string
	diagnostics := data.Permissions.ElementsAs(ctx, &permissions, false)
	return iam.RoleWriteBody{DisplayName: data.DisplayName.ValueString(), Permissions: permissions}, diagnostics
}

func (r *RoleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data RoleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, diagnostics := roleWriteBody(ctx, data)
	resp.Diagnostics.Append(diagnostics...)
	if resp.Diagnostics.HasError() {
		return
	}
	httpResp, err := r.iamClient.CreateRoleWithResponse(ctx, r.orgId, body)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to create role: %s", err))
		return
	}
	if httpResp.StatusCode() != http.StatusCreated {
		resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to create role, unexpected status code: %d, body: %s", httpResp.StatusCode(), httpResp.Body))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, toRoleModel(*httpResp.JSON201))...)
}

func (r *RoleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data RoleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	roleID, err := uuid.Parse(data.Id.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, fmt.Sprintf("Invalid role UUID in state: %s", err))
		return
	}
	httpResp, err := r.iamClient.GetRoleWithResponse(ctx, r.orgId, roleID)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to read role: %s", err))
		return
	}
	if httpResp.StatusCode() == http.StatusNotFound {
		resp.State.RemoveResource(ctx)
		return
	}
	if httpResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to read role, unexpected status code: %d, body: %s", httpResp.StatusCode(), httpResp.Body))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, toRoleModel(*httpResp.JSON200))...)
}

func (r *RoleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data RoleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	roleID, err := uuid.Parse(data.Id.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, fmt.Sprintf("Invalid role UUID in state: %s", err))
		return
	}
	body, diagnostics := roleWriteBody(ctx, data)
	resp.Diagnostics.Append(diagnostics...)
	if resp.Diagnostics.HasError() {
		return
	}
	httpResp, err := r.iamClient.UpdateRoleWithResponse(ctx, r.orgId, roleID, body)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to update role: %s", err))
		return
	}
	if httpResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to update role, unexpected status code: %d, body: %s", httpResp.StatusCode(), httpResp.Body))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, toRoleModel(*httpResp.JSON200))...)
}

func (r *RoleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data RoleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	roleID, err := uuid.Parse(data.Id.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, fmt.Sprintf("Invalid role UUID in state: %s", err))
		return
	}
	httpResp, err := r.iamClient.DeleteRoleWithResponse(ctx, r.orgId, roleID)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to delete role: %s", err))
		return
	}
	if httpResp.StatusCode() != http.StatusNoContent && httpResp.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to delete role, unexpected status code: %d, body: %s", httpResp.StatusCode(), httpResp.Body))
	}
}

func (r *RoleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func toRoleModel(role iam.Role) RoleModel {
	permissionValues := make([]attr.Value, len(role.Permissions))
	for i, permission := range role.Permissions {
		permissionValues[i] = types.StringValue(permission)
	}
	return RoleModel{
		Id: types.StringValue(role.Id.String()), DisplayName: types.StringValue(role.DisplayName), Permissions: types.SetValueMust(types.StringType, permissionValues),
		IsSystem: types.BoolValue(role.IsSystem), CreatedAt: types.StringValue(role.CreatedAt.Format(time.RFC3339)), CreatedBy: types.StringValue(role.CreatedBy.String()),
	}
}
