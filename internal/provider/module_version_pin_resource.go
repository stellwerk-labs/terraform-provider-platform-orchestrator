package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &ModuleVersionPinResource{}
var _ resource.ResourceWithImportState = &ModuleVersionPinResource{}

func NewModuleVersionPinResource() resource.Resource { return &ModuleVersionPinResource{} }

type ModuleVersionPinResource struct {
	cpClient cp.ClientWithResponsesInterface
	orgID    string
}

type ModuleVersionPinResourceModel struct {
	ID                      types.String `tfsdk:"id"`
	ProjectUUID             types.String `tfsdk:"project_uuid"`
	EnvironmentUUID         types.String `tfsdk:"environment_uuid"`
	ModuleUUID              types.String `tfsdk:"module_uuid"`
	VersionUUID             types.String `tfsdk:"version_uuid"`
	Reason                  types.String `tfsdk:"reason"`
	RemovalReason           types.String `tfsdk:"removal_reason"`
	ConfirmDefectiveVersion types.Bool   `tfsdk:"confirm_defective_version"`
	Status                  types.String `tfsdk:"status"`
	ResourceVersion         types.Int64  `tfsdk:"resource_version"`
	ActivationEventID       types.String `tfsdk:"activation_event_id"`
	OverrideOperationID     types.String `tfsdk:"override_operation_id"`
	OverrideDeploymentID    types.String `tfsdk:"override_deployment_id"`
	CreatedBy               types.String `tfsdk:"created_by"`
}

func (r *ModuleVersionPinResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_module_version_pin"
}

func (r *ModuleVersionPinResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages one exact Orchestrator Environment Module Version Pin. Pin authority is Environment scoped and independent from add-on control. Destroy performs an explicit audited Unpin or permanent discard; override-pending Pins remain operation locked.",
		Attributes: map[string]schema.Attribute{
			"id":                        schema.StringAttribute{Computed: true},
			"project_uuid":              schema.StringAttribute{Required: true, PlanModifiers: replace},
			"environment_uuid":          schema.StringAttribute{Required: true, PlanModifiers: replace},
			"module_uuid":               schema.StringAttribute{Required: true, PlanModifiers: replace},
			"version_uuid":              schema.StringAttribute{Required: true, PlanModifiers: replace},
			"reason":                    schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Immutable human-readable creation reason. Required when creating a Pin and recovered from its append-only history during import.", PlanModifiers: replace},
			"removal_reason":            schema.StringAttribute{Optional: true, MarkdownDescription: "Human-readable reason used if Terraform destroys this Pin. Required at destroy time."},
			"confirm_defective_version": schema.BoolAttribute{Optional: true, MarkdownDescription: "Exact acknowledgement required with the dedicated capability when pinning an active Defective Version."},
			"status":                    schema.StringAttribute{Computed: true},
			"resource_version":          schema.Int64Attribute{Computed: true},
			"activation_event_id":       schema.StringAttribute{Computed: true, MarkdownDescription: "Immutable approval boundary of the current protection."},
			"override_operation_id":     schema.StringAttribute{Computed: true},
			"override_deployment_id":    schema.StringAttribute{Computed: true},
			"created_by":                schema.StringAttribute{Computed: true},
		},
	}
}

func (r *ModuleVersionPinResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*PlatformOrchestratorProviderData)
	if !ok {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, fmt.Sprintf("Expected *PlatformOrchestratorProviderData, got %T", req.ProviderData))
		return
	}
	r.cpClient, r.orgID = data.CpClient, data.OrgId
}

func parseUUIDAttribute(diagnostics *diag.Diagnostics, name string, value types.String) (uuid.UUID, bool) {
	parsed, err := uuid.Parse(value.ValueString())
	if err != nil {
		diagnostics.AddError(PO_INPUT_ERR, fmt.Sprintf("%s must be a UUID: %s", name, err))
		return uuid.Nil, false
	}
	return parsed, true
}

func (r *ModuleVersionPinResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ModuleVersionPinResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if strings.TrimSpace(plan.Reason.ValueString()) == "" {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "reason is required to create a Module Version Pin")
		return
	}
	project, ok := parseUUIDAttribute(&resp.Diagnostics, "project_uuid", plan.ProjectUUID)
	if !ok {
		return
	}
	environment, ok := parseUUIDAttribute(&resp.Diagnostics, "environment_uuid", plan.EnvironmentUUID)
	if !ok {
		return
	}
	module, ok := parseUUIDAttribute(&resp.Diagnostics, "module_uuid", plan.ModuleUUID)
	if !ok {
		return
	}
	version, ok := parseUUIDAttribute(&resp.Diagnostics, "version_uuid", plan.VersionUUID)
	if !ok {
		return
	}
	body := cp.EnvironmentModuleVersionPinCreateBody{
		ProjectUuid: project, EnvironmentUuid: environment, ModuleUuid: module, VersionUuid: version,
		Reason: plan.Reason.ValueString(),
	}
	if plan.ConfirmDefectiveVersion.ValueBool() {
		body.ConfirmDefectiveVersionUuid = &version
	}
	key := uuid.NewString()
	response, err := r.cpClient.CreateEnvironmentModuleVersionPinWithResponse(ctx, r.orgID, &cp.CreateEnvironmentModuleVersionPinParams{IdempotencyKey: key}, body)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to create Module Version Pin: "+err.Error())
		return
	}
	if response.StatusCode() != http.StatusCreated || response.JSON201 == nil {
		addAPIResponseError(&resp.Diagnostics, "create Module Version Pin", response.StatusCode(), response.Body)
		return
	}
	applyModuleVersionPinState(&plan, *response.JSON201)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ModuleVersionPinResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ModuleVersionPinResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, ok := parseUUIDAttribute(&resp.Diagnostics, "id", state.ID)
	if !ok {
		return
	}
	response, err := r.cpClient.GetEnvironmentModuleVersionPinWithResponse(ctx, r.orgID, id)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to read Module Version Pin: "+err.Error())
		return
	}
	if response.StatusCode() == http.StatusNotFound {
		resp.State.RemoveResource(ctx)
		return
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		addAPIResponseError(&resp.Diagnostics, "read Module Version Pin", response.StatusCode(), response.Body)
		return
	}
	if response.JSON200.Status == cp.ModuleVersionPinStatusRemoved {
		resp.State.RemoveResource(ctx)
		return
	}
	applyModuleVersionPinState(&state, *response.JSON200)
	if state.Reason.ValueString() == "" {
		events, err := r.cpClient.ListEnvironmentModuleVersionPinEventsWithResponse(ctx, r.orgID, id)
		if err != nil {
			resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to read Module Version Pin history: "+err.Error())
			return
		}
		if events.StatusCode() != http.StatusOK || events.JSON200 == nil {
			addAPIResponseError(&resp.Diagnostics, "read Module Version Pin history", events.StatusCode(), events.Body)
			return
		}
		for _, event := range *events.JSON200 {
			if event.EventType == "created" && event.Reason != nil {
				state.Reason = types.StringValue(*event.Reason)
				break
			}
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ModuleVersionPinResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ModuleVersionPinResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.RemovalReason = plan.RemovalReason
	state.ConfirmDefectiveVersion = plan.ConfirmDefectiveVersion
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ModuleVersionPinResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ModuleVersionPinResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if strings.TrimSpace(state.RemovalReason.ValueString()) == "" {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "removal_reason is required to destroy a Module Version Pin")
		return
	}
	id, ok := parseUUIDAttribute(&resp.Diagnostics, "id", state.ID)
	if !ok {
		return
	}
	action := cp.TransitionEnvironmentModuleVersionPinParamsPinActionUnpin
	if state.Status.ValueString() == string(cp.ModuleVersionPinStatusOverridden) {
		action = cp.TransitionEnvironmentModuleVersionPinParamsPinActionDiscard
	} else if state.Status.ValueString() == string(cp.ModuleVersionPinStatusOverridePending) {
		resp.Diagnostics.AddError(PO_API_ERR, "The Pin is override_pending and owned by an add-on operation. Finish or cancel that operation before destroying the Pin resource.")
		return
	}
	key := reasonedCommandKey(r.orgID, id.String(), string(action), state.ResourceVersion.ValueInt64(), state.RemovalReason.ValueString())
	response, err := r.cpClient.TransitionEnvironmentModuleVersionPinWithResponse(ctx, r.orgID, id, action,
		&cp.TransitionEnvironmentModuleVersionPinParams{IdempotencyKey: key}, cp.ModuleReasonedCommand{
			ExpectedResourceVersion: state.ResourceVersion.ValueInt64(), Reason: state.RemovalReason.ValueString(),
		})
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to remove Module Version Pin: "+err.Error())
		return
	}
	if response.StatusCode() != http.StatusOK {
		addAPIResponseError(&resp.Diagnostics, "remove Module Version Pin", response.StatusCode(), response.Body)
		return
	}
	resp.State.RemoveResource(ctx)
}

func applyModuleVersionPinState(state *ModuleVersionPinResourceModel, pin cp.EnvironmentModuleVersionPin) {
	state.ID = types.StringValue(pin.Id.String())
	state.ProjectUUID = types.StringValue(pin.ProjectUuid.String())
	state.EnvironmentUUID = types.StringValue(pin.EnvironmentUuid.String())
	state.ModuleUUID = types.StringValue(pin.ModuleUuid.String())
	state.VersionUUID = types.StringValue(pin.VersionUuid.String())
	state.Status = types.StringValue(string(pin.Status))
	state.ResourceVersion = types.Int64Value(pin.ResourceVersion)
	state.ActivationEventID = types.StringValue(pin.ActivationEventId.String())
	state.CreatedBy = types.StringValue(pin.CreatedBy.String())
	state.OverrideOperationID = nullableUUIDString(pin.OverrideOperationId)
	state.OverrideDeploymentID = nullableUUIDString(pin.OverrideDeploymentId)
}

func nullableUUIDString(value *uuid.UUID) types.String {
	if value == nil {
		return types.StringNull()
	}
	return types.StringValue(value.String())
}

func (r *ModuleVersionPinResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := uuid.Parse(req.ID); err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Import ID must be a Module Version Pin UUID")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
