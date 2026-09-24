package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &ModuleCatalogueEntryResource{}
var _ resource.ResourceWithImportState = &ModuleCatalogueEntryResource{}

func NewModuleCatalogueEntryResource() resource.Resource { return &ModuleCatalogueEntryResource{} }

type ModuleCatalogueEntryResource struct {
	cpClient cp.ClientWithResponsesInterface
	orgID    string
}

type ModuleCatalogueEntryResourceModel struct {
	ID               types.String `tfsdk:"id"`
	UUID             types.String `tfsdk:"uuid"`
	DisplayName      types.String `tfsdk:"display_name"`
	Description      types.String `tfsdk:"description"`
	ResourceType     types.String `tfsdk:"resource_type"`
	Tags             types.Map    `tfsdk:"tags"`
	Status           types.String `tfsdk:"status"`
	TransitionReason types.String `tfsdk:"transition_reason"`
	DeletionReason   types.String `tfsdk:"deletion_reason"`
	ResourceVersion  types.Int64  `tfsdk:"resource_version"`
}

func (r *ModuleCatalogueEntryResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_module_catalogue_entry"
}

func (r *ModuleCatalogueEntryResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages one stable Orchestrator Module identity independently of its immutable Versions. Destroy deletes an eligible empty shell; a Module with published history is archived and retained before Terraform relinquishes ownership. Other deletion or archival blockers remain errors. Import a retained Module before managing it again. Archival never mutates deployed infrastructure.",
		Attributes: map[string]schema.Attribute{
			"id":                schema.StringAttribute{Required: true, MarkdownDescription: "Immutable technical Module slug.", PlanModifiers: replace},
			"uuid":              schema.StringAttribute{Computed: true, MarkdownDescription: "Immutable Module UUID."},
			"display_name":      schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Mutable human-readable name."},
			"description":       schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Mutable Module description."},
			"resource_type":     schema.StringAttribute{Required: true, MarkdownDescription: "Immutable Resource Type binding.", PlanModifiers: replace},
			"tags":              schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType, MarkdownDescription: "Mutable catalogue tags."},
			"status":            schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Desired catalogue status: active or archived.", Validators: []validator.String{stringvalidator.OneOf("active", "archived")}},
			"transition_reason": schema.StringAttribute{Optional: true, MarkdownDescription: "Mandatory reason when status changes."},
			"deletion_reason":   schema.StringAttribute{Optional: true, MarkdownDescription: "Reason recorded when destroy archives retained Module history. Defaults to 'Terraform removed Module catalogue ownership'."},
			"resource_version":  schema.Int64Attribute{Computed: true, MarkdownDescription: "Optimistic-concurrency version of the catalogue entry."},
		},
	}
}

func (r *ModuleCatalogueEntryResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func stringMap(ctx context.Context, value types.Map) (map[string]string, error) {
	if value.IsNull() || value.IsUnknown() {
		return map[string]string{}, nil
	}
	result := make(map[string]string)
	if diagnostics := value.ElementsAs(ctx, &result, false); diagnostics.HasError() {
		return nil, fmt.Errorf("string map could not be decoded: %s", diagnostics.Errors()[0].Detail())
	}
	return result, nil
}

func (r *ModuleCatalogueEntryResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ModuleCatalogueEntryResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tags, err := stringMap(ctx, plan.Tags)
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, err.Error())
		return
	}
	desiredStatus := plan.Status.ValueString()
	reason := plan.TransitionReason.ValueString()
	if desiredStatus == string(cp.ModuleCatalogueStatusArchived) && strings.TrimSpace(reason) == "" {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "transition_reason is required when creating an archived Module catalogue entry")
		return
	}
	body := cp.ModuleCatalogueCreateBody{Slug: plan.ID.ValueString(), ResourceType: plan.ResourceType.ValueString()}
	if value := plan.DisplayName.ValueString(); value != "" {
		body.DisplayName = &value
	}
	if value := plan.Description.ValueString(); value != "" {
		body.Description = &value
	}
	if len(tags) > 0 {
		body.Tags = &tags
	}
	key := uuid.NewString()
	response, err := r.cpClient.CreateModuleCatalogueEntryWithResponse(ctx, r.orgID,
		&cp.CreateModuleCatalogueEntryParams{IdempotencyKey: key}, body)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to create Module catalogue entry: "+err.Error())
		return
	}
	if response.StatusCode() != http.StatusCreated || response.JSON201 == nil {
		addAPIResponseError(&resp.Diagnostics, "create Module catalogue entry", response.StatusCode(), response.Body)
		return
	}
	applyModuleCatalogueEntryState(ctx, &resp.Diagnostics, &plan, *response.JSON201)
	// A failed follow-up command must not orphan the successfully created shell.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if desiredStatus == string(cp.ModuleCatalogueStatusArchived) {
		if !r.transition(ctx, &plan, cp.ChangeModuleCatalogueStatusParamsCatalogueActionArchive, &resp.Diagnostics) {
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ModuleCatalogueEntryResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ModuleCatalogueEntryResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	response, err := r.cpClient.GetModuleCatalogueEntryWithResponse(ctx, r.orgID, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to read Module catalogue entry: "+err.Error())
		return
	}
	if response.StatusCode() == http.StatusNotFound {
		resp.State.RemoveResource(ctx)
		return
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		addAPIResponseError(&resp.Diagnostics, "read Module catalogue entry", response.StatusCode(), response.Body)
		return
	}
	applyModuleCatalogueEntryState(ctx, &resp.Diagnostics, &state, *response.JSON200)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ModuleCatalogueEntryResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ModuleCatalogueEntryResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.DisplayName.IsUnknown() {
		plan.DisplayName = state.DisplayName
	}
	if plan.Description.IsUnknown() {
		plan.Description = state.Description
	}
	if plan.Tags.IsUnknown() {
		plan.Tags = state.Tags
	}
	if plan.Status.IsUnknown() {
		plan.Status = state.Status
	}
	tags, err := stringMap(ctx, plan.Tags)
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, err.Error())
		return
	}
	desiredStatus := plan.Status.ValueString()
	if desiredStatus != state.Status.ValueString() && strings.TrimSpace(plan.TransitionReason.ValueString()) == "" {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "transition_reason is required when Module catalogue status changes")
		return
	}
	metadataChanged := !plan.DisplayName.Equal(state.DisplayName) || !plan.Description.Equal(state.Description) || !plan.Tags.Equal(state.Tags)
	plan.UUID = state.UUID
	plan.ResourceVersion = state.ResourceVersion
	if metadataChanged {
		response, updateErr := r.cpClient.UpdateModuleCatalogueEntryWithResponse(ctx, r.orgID, state.ID.ValueString(), cp.ModuleCatalogueUpdateBody{
			DisplayName: plan.DisplayName.ValueString(), Description: plan.Description.ValueString(), Tags: tags,
			ExpectedResourceVersion: state.ResourceVersion.ValueInt64(),
		})
		if updateErr != nil {
			resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to update Module catalogue metadata: "+updateErr.Error())
			return
		}
		if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
			addAPIResponseError(&resp.Diagnostics, "update Module catalogue metadata", response.StatusCode(), response.Body)
			return
		}
		applyModuleCatalogueEntryState(ctx, &resp.Diagnostics, &plan, *response.JSON200)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if desiredStatus != state.Status.ValueString() {
		action := cp.ChangeModuleCatalogueStatusParamsCatalogueActionArchive
		if desiredStatus == string(cp.ModuleCatalogueStatusActive) {
			action = cp.ChangeModuleCatalogueStatusParamsCatalogueActionUnarchive
		}
		if !r.transition(ctx, &plan, action, &resp.Diagnostics) {
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ModuleCatalogueEntryResource) transition(ctx context.Context, state *ModuleCatalogueEntryResourceModel, action cp.ChangeModuleCatalogueStatusParamsCatalogueAction, diagnostics *diag.Diagnostics) bool {
	key := reasonedCommandKey(r.orgID, state.UUID.ValueString(), string(action), state.ResourceVersion.ValueInt64(), state.TransitionReason.ValueString())
	response, err := r.cpClient.ChangeModuleCatalogueStatusWithResponse(ctx, r.orgID, state.ID.ValueString(), action,
		&cp.ChangeModuleCatalogueStatusParams{IdempotencyKey: key}, cp.ModuleReasonedCommand{
			ExpectedResourceVersion: state.ResourceVersion.ValueInt64(), Reason: state.TransitionReason.ValueString(),
		})
	if err != nil {
		diagnostics.AddError(PO_CLIENT_ERR, "Unable to change Module catalogue status: "+err.Error())
		return false
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		addAPIResponseError(diagnostics, "change Module catalogue status", response.StatusCode(), response.Body)
		return false
	}
	applyModuleCatalogueEntryState(ctx, diagnostics, state, *response.JSON200)
	return !diagnostics.HasError()
}

func applyModuleCatalogueEntryState(ctx context.Context, diagnostics *diag.Diagnostics, state *ModuleCatalogueEntryResourceModel, entry cp.ModuleCatalogueEntry) {
	state.ID = types.StringValue(entry.Slug)
	state.UUID = types.StringValue(entry.Uuid.String())
	state.DisplayName = types.StringValue(entry.DisplayName)
	state.Description = types.StringValue(entry.Description)
	state.ResourceType = types.StringValue(entry.ResourceType)
	state.Status = types.StringValue(string(entry.Status))
	state.ResourceVersion = types.Int64Value(entry.ResourceVersion)
	tags, tagDiagnostics := types.MapValueFrom(ctx, types.StringType, entry.Tags)
	diagnostics.Append(tagDiagnostics...)
	state.Tags = tags
}

func (r *ModuleCatalogueEntryResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ModuleCatalogueEntryResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	response, err := r.cpClient.DeleteModuleWithResponse(ctx, r.orgID, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to delete empty Module catalogue entry: "+err.Error())
		return
	}
	if response.StatusCode() != http.StatusNoContent && response.StatusCode() != http.StatusNotFound {
		var problem platformAPIError
		if response.StatusCode() == http.StatusConflict && json.Unmarshal(response.Body, &problem) == nil && problem.Code == "module_history_retained" {
			if !r.archiveRetainedModule(ctx, &state, &resp.Diagnostics) {
				return
			}
			resp.Diagnostics.AddWarning("Module history retained", fmt.Sprintf("Module %q was archived with its immutable Versions and audit history. Terraform removed only catalogue ownership. Import this slug to manage it again.", state.ID.ValueString()))
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIResponseError(&resp.Diagnostics, "delete empty Module catalogue entry", response.StatusCode(), response.Body)
		return
	}
	resp.State.RemoveResource(ctx)
}

func (r *ModuleCatalogueEntryResource) archiveRetainedModule(ctx context.Context, state *ModuleCatalogueEntryResourceModel, diagnostics *diag.Diagnostics) bool {
	response, err := r.cpClient.GetModuleCatalogueEntryWithResponse(ctx, r.orgID, state.ID.ValueString())
	if err != nil {
		diagnostics.AddError(PO_CLIENT_ERR, "Unable to inspect retained Module catalogue entry: "+err.Error())
		return false
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		addAPIResponseError(diagnostics, "inspect retained Module catalogue entry", response.StatusCode(), response.Body)
		return false
	}
	applyModuleCatalogueEntryState(ctx, diagnostics, state, *response.JSON200)
	if diagnostics.HasError() {
		return false
	}
	if state.Status.ValueString() == string(cp.ModuleCatalogueStatusArchived) {
		return true
	}
	reason := strings.TrimSpace(state.DeletionReason.ValueString())
	if reason == "" {
		reason = "Terraform removed Module catalogue ownership"
	}
	state.TransitionReason = types.StringValue(reason)
	return r.transition(ctx, state, cp.ChangeModuleCatalogueStatusParamsCatalogueActionArchive, diagnostics)
}

func (r *ModuleCatalogueEntryResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
