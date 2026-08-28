package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	iam "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-iam"
)

var _ resource.Resource = &ScimGroupMappingResource{}
var _ resource.ResourceWithImportState = &ScimGroupMappingResource{}

func NewScimGroupMappingResource() resource.Resource { return &ScimGroupMappingResource{} }

type ScimGroupMappingResource struct {
	iamClient iam.ClientWithResponsesInterface
	orgId     string
}

type ScimGroupMappingModel struct {
	GroupDisplayName types.String `tfsdk:"group_display_name"`
	RoleId           types.String `tfsdk:"role_id"`
	CreatedAt        types.String `tfsdk:"created_at"`
}

func (r *ScimGroupMappingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_scim_group_mapping"
}

func (r *ScimGroupMappingResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Maps members of a SCIM group to an organization role. Group display names are matched case-insensitively by the API.", Attributes: map[string]schema.Attribute{
		"group_display_name": schema.StringAttribute{MarkdownDescription: "The SCIM group's display name.", Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"role_id":            schema.StringAttribute{MarkdownDescription: "The UUID of the role granted to group members.", Required: true},
		"created_at":         schema.StringAttribute{MarkdownDescription: "The time the mapping was first created.", Computed: true},
	}}
}

func (r *ScimGroupMappingResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func scimWriteBody(data ScimGroupMappingModel) (iam.ScimGroupMappingWriteBody, error) {
	roleID, err := uuid.Parse(data.RoleId.ValueString())
	if err != nil {
		return iam.ScimGroupMappingWriteBody{}, fmt.Errorf("role_id must be a UUID: %w", err)
	}
	return iam.ScimGroupMappingWriteBody{RoleId: roleID}, nil
}

func (r *ScimGroupMappingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data ScimGroupMappingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := scimWriteBody(data)
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, err.Error())
		return
	}
	httpResp, err := r.iamClient.UpsertScimGroupMappingWithResponse(ctx, r.orgId, data.GroupDisplayName.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to create SCIM group mapping: %s", err))
		return
	}
	if httpResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to create SCIM group mapping, unexpected status code: %d, body: %s", httpResp.StatusCode(), httpResp.Body))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, toScimGroupMappingModel(*httpResp.JSON200))...)
}

func findScimGroupMapping(items []iam.ScimGroupMapping, groupDisplayName string) (iam.ScimGroupMapping, bool) {
	for _, mapping := range items {
		if strings.EqualFold(mapping.GroupDisplayName, groupDisplayName) {
			return mapping, true
		}
	}
	return iam.ScimGroupMapping{}, false
}

func (r *ScimGroupMappingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data ScimGroupMappingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	httpResp, err := r.iamClient.ListScimGroupMappingsWithResponse(ctx, r.orgId)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to list SCIM group mappings: %s", err))
		return
	}
	if httpResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to list SCIM group mappings, unexpected status code: %d, body: %s", httpResp.StatusCode(), httpResp.Body))
		return
	}
	mapping, found := findScimGroupMapping(httpResp.JSON200.Items, data.GroupDisplayName.ValueString())
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, toScimGroupMappingModel(mapping))...)
}

func (r *ScimGroupMappingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data ScimGroupMappingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := scimWriteBody(data)
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, err.Error())
		return
	}
	httpResp, err := r.iamClient.UpsertScimGroupMappingWithResponse(ctx, r.orgId, data.GroupDisplayName.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to update SCIM group mapping: %s", err))
		return
	}
	if httpResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to update SCIM group mapping, unexpected status code: %d, body: %s", httpResp.StatusCode(), httpResp.Body))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, toScimGroupMappingModel(*httpResp.JSON200))...)
}

func (r *ScimGroupMappingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data ScimGroupMappingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	httpResp, err := r.iamClient.DeleteScimGroupMappingWithResponse(ctx, r.orgId, data.GroupDisplayName.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to delete SCIM group mapping: %s", err))
		return
	}
	if httpResp.StatusCode() != http.StatusNoContent && httpResp.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to delete SCIM group mapping, unexpected status code: %d, body: %s", httpResp.StatusCode(), httpResp.Body))
	}
}

func (r *ScimGroupMappingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_display_name"), req.ID)...)
}

func toScimGroupMappingModel(mapping iam.ScimGroupMapping) ScimGroupMappingModel {
	return ScimGroupMappingModel{
		GroupDisplayName: types.StringValue(mapping.GroupDisplayName), RoleId: types.StringValue(mapping.RoleId.String()),
		CreatedAt: types.StringValue(mapping.CreatedAt.Format(time.RFC3339)),
	}
}
