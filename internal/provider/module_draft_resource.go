package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
)

var _ resource.Resource = &ModuleDraftResource{}
var _ resource.ResourceWithImportState = &ModuleDraftResource{}

func NewModuleDraftResource() resource.Resource { return &ModuleDraftResource{} }

type ModuleDraftResource struct {
	cpClient cp.ClientWithResponsesInterface
	orgID    string
}

type ModuleDraftResourceModel struct {
	ID                   types.String         `tfsdk:"id"`
	ModuleID             types.String         `tfsdk:"module_id"`
	AuthoringKey         types.String         `tfsdk:"authoring_key"`
	SemanticVersion      types.String         `tfsdk:"semantic_version"`
	Definition           jsontypes.Normalized `tfsdk:"definition"`
	Reason               types.String         `tfsdk:"reason"`
	Status               types.String         `tfsdk:"status"`
	ResourceVersion      types.Int64          `tfsdk:"resource_version"`
	PublishedVersionUUID types.String         `tfsdk:"published_version_uuid"`
}

func (r *ModuleDraftResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_module_draft"
}

func (r *ModuleDraftResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manages an editable Module Draft with immutable authoring revisions. Saving does not reserve SemVer or execute a deployment. Publish only after a separate apply has saved the definition you reviewed. Destroy discards an open Draft, retains authoring history and never deletes a published Module Version.", Attributes: map[string]schema.Attribute{
		"id":                     schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Stable authoring Draft UUID."},
		"authoring_key":          schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, MarkdownDescription: "Stable identity of this authoring session. Keep it unchanged for retries; use a new key when replacing a closed Draft."},
		"module_id":              schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"semantic_version":       schema.StringAttribute{Required: true, MarkdownDescription: "Planned SemVer, editable while open and reserved only at publication."},
		"definition":             schema.StringAttribute{Required: true, CustomType: jsontypes.NormalizedType{}, MarkdownDescription: "Complete JSON authoring definition, excluding semantic_version. Accepts source, module inputs/parameters, provider mappings, dependencies, coprovisioned, output_schema, optional resource_type_version and release notes. External source_revision may be omitted while editing but is required at publication. artifact_digest remains optional and must be omitted for inline source."},
		"reason":                 schema.StringAttribute{Required: true, MarkdownDescription: "Input reason for the next save or publication, not a projection of historical reasons."},
		"status":                 schema.StringAttribute{Optional: true, Computed: true, Validators: []validator.String{stringvalidator.OneOf("open", "published", "discarded")}, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Initially open. Change to published to publish the exact stored revision, or discarded to close authoring. Closed Drafts cannot be reopened or edited."},
		"resource_version":       schema.Int64Attribute{Computed: true, MarkdownDescription: "Exact authoring revision used for optimistic concurrency. Changes when the Draft is saved or closed."},
		"published_version_uuid": schema.StringAttribute{Computed: true, MarkdownDescription: "Immutable Proposed Version UUID after publication; publication never promotes it to Default."},
	}}
}

func (r *ModuleDraftResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func draftPublicationBody(data ModuleDraftResourceModel) (cp.ModuleVersionPublishBody, error) {
	var body cp.ModuleVersionPublishBody
	if err := decodeModuleVersionDefinition(data.Definition.ValueString(), &body); err != nil {
		return body, err
	}
	if body.SemanticVersion != "" {
		return body, fmt.Errorf("semantic_version belongs to the top-level resource attribute, not definition")
	}
	body.SemanticVersion = data.SemanticVersion.ValueString()
	return body, nil
}

func sameDraftDefinition(left, right cp.ModuleVersionPublishBody) (bool, error) {
	first, err := json.Marshal(left)
	if err != nil {
		return false, err
	}
	second, err := json.Marshal(right)
	return bytes.Equal(first, second), err
}

// Preserve configured spelling only when the generated codec proves the full
// effective definition is unchanged. Actual remote edits must appear as drift.
func applyModuleDraftState(state *ModuleDraftResourceModel, draft cp.ModuleDraft) error {
	keepDefinition := false
	if !state.Definition.IsNull() && !state.Definition.IsUnknown() {
		body, err := draftPublicationBody(*state)
		if err == nil {
			keepDefinition, err = sameDraftDefinition(body, draft.Definition)
			if err != nil {
				return err
			}
		}
	}
	state.ID, state.ModuleID = types.StringValue(draft.Id.String()), types.StringValue(draft.ModuleSlug)
	state.SemanticVersion = types.StringValue(draft.Definition.SemanticVersion)
	state.Status, state.ResourceVersion = types.StringValue(string(draft.Status)), types.Int64Value(draft.ResourceVersion)
	state.PublishedVersionUUID = types.StringNull()
	if draft.PublishedVersionUuid != nil {
		state.PublishedVersionUUID = types.StringValue(draft.PublishedVersionUuid.String())
	}
	if keepDefinition {
		return nil
	}
	encoded, err := json.Marshal(draft.Definition)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return err
	}
	delete(fields, "semantic_version")
	encoded, err = json.Marshal(fields)
	if err == nil {
		state.Definition = jsontypes.NewNormalizedValue(string(encoded))
	}
	return err
}

func (r *ModuleDraftResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ModuleDraftResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if status := plan.Status.ValueString(); status != "" && status != "open" {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Create the open Draft first, review its stored definition, then change status in a separate apply.")
		return
	}
	body, err := draftPublicationBody(plan)
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, err.Error())
		return
	}
	if strings.TrimSpace(plan.AuthoringKey.ValueString()) == "" {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "authoring_key must identify one stable authoring session")
		return
	}
	key := moduleVersionCommandKey(r.orgID, plan.ModuleID.ValueString(), plan.AuthoringKey.ValueString(), "draft-create")
	result, err := r.cpClient.CreateModuleDraftWithResponse(ctx, r.orgID, plan.ModuleID.ValueString(), &cp.CreateModuleDraftParams{IdempotencyKey: key}, cp.ModuleDraftCreateBody{Definition: body, Reason: plan.Reason.ValueString()})
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, err.Error())
		return
	}
	if result.StatusCode() != http.StatusCreated || result.JSON201 == nil {
		addAPIResponseError(&resp.Diagnostics, "create Module Draft", result.StatusCode(), result.Body)
		return
	}
	current, err := r.cpClient.GetModuleDraftWithResponse(ctx, r.orgID, plan.ModuleID.ValueString(), result.JSON201.Id)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, err.Error())
		return
	}
	if current.StatusCode() != http.StatusOK || current.JSON200 == nil {
		addAPIResponseError(&resp.Diagnostics, "read created Module Draft", current.StatusCode(), current.Body)
		return
	}
	if current.JSON200.Status != cp.ModuleDraftStatusOpen {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "authoring_key already belongs to a closed Draft. Choose a new authoring_key for a new session, or import the existing Draft.")
		return
	}
	equal, err := sameDraftDefinition(body, current.JSON200.Definition)
	if err != nil || !equal {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "The authoring session changed after creation. Import and review the existing Draft before reconciling its definition.")
		return
	}
	if err := applyModuleDraftState(&plan, *current.JSON200); err != nil {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ModuleDraftResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ModuleDraftResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := uuid.Parse(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Invalid Draft UUID in state")
		return
	}
	result, err := r.cpClient.GetModuleDraftWithResponse(ctx, r.orgID, state.ModuleID.ValueString(), id)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, err.Error())
		return
	}
	if result.StatusCode() != http.StatusOK || result.JSON200 == nil {
		addAPIResponseError(&resp.Diagnostics, "read retained Module Draft", result.StatusCode(), result.Body)
		return
	}
	if err := applyModuleDraftState(&state, *result.JSON200); err != nil {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, err.Error())
		return
	}
	if state.Reason.IsNull() || state.Reason.IsUnknown() {
		revision, err := r.cpClient.GetModuleDraftRevisionWithResponse(ctx, r.orgID, state.ModuleID.ValueString(), id, state.ResourceVersion.ValueInt64())
		if err != nil {
			resp.Diagnostics.AddError(PO_CLIENT_ERR, err.Error())
			return
		}
		if revision.StatusCode() != http.StatusOK || revision.JSON200 == nil {
			addAPIResponseError(&resp.Diagnostics, "read imported Draft reason", revision.StatusCode(), revision.Body)
			return
		}
		state.Reason = types.StringValue(revision.JSON200.Reason)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ModuleDraftResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ModuleDraftResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	before, err := draftPublicationBody(state)
	if err != nil {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, "Invalid stored definition: "+err.Error())
		return
	}
	after, err := draftPublicationBody(plan)
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, err.Error())
		return
	}
	equal, err := sameDraftDefinition(before, after)
	if err != nil {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, err.Error())
		return
	}
	desired := plan.Status.ValueString()
	if plan.Status.IsUnknown() || plan.Status.IsNull() {
		desired = state.Status.ValueString()
	}
	if state.Status.ValueString() != "open" {
		if !equal || desired != state.Status.ValueString() {
			resp.Diagnostics.AddError(PO_INPUT_ERR, "Closed Drafts are immutable. Create another Draft or explicitly replace this Terraform resource.")
			return
		}
		plan.ID, plan.ResourceVersion, plan.Status, plan.PublishedVersionUUID = state.ID, state.ResourceVersion, state.Status, state.PublishedVersionUUID
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	if desired != "open" && !equal {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Save the edited definition with status=open in a separate apply before publishing or discarding its exact reviewed revision.")
		return
	}
	id, err := uuid.Parse(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Invalid Draft UUID in state")
		return
	}
	key := moduleVersionCommandKey(r.orgID, state.ModuleID.ValueString(), state.ID.ValueString(), "draft-"+desired+":"+strconv.FormatInt(state.ResourceVersion.ValueInt64(), 10))
	var draft *cp.ModuleDraft
	if desired == "open" {
		result, err := r.cpClient.UpdateModuleDraftWithResponse(ctx, r.orgID, state.ModuleID.ValueString(), id, &cp.UpdateModuleDraftParams{IdempotencyKey: key},
			cp.ModuleDraftUpdateBody{Definition: after, Reason: plan.Reason.ValueString(), ExpectedResourceVersion: state.ResourceVersion.ValueInt64()})
		if err != nil {
			resp.Diagnostics.AddError(PO_CLIENT_ERR, err.Error())
			return
		}
		if result.StatusCode() != http.StatusOK || result.JSON200 == nil {
			addAPIResponseError(&resp.Diagnostics, "save Module Draft", result.StatusCode(), result.Body)
			return
		}
		draft = result.JSON200
	} else {
		draft = r.close(ctx, state, desired, key, plan.Reason.ValueString(), &resp.Diagnostics)
		if draft == nil {
			return
		}
	}
	if err := applyModuleDraftState(&plan, *draft); err != nil {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ModuleDraftResource) close(ctx context.Context, state ModuleDraftResourceModel, desired, key, reason string, diagnostics *diag.Diagnostics) *cp.ModuleDraft {
	id, err := uuid.Parse(state.ID.ValueString())
	if err != nil {
		diagnostics.AddError(PO_INPUT_ERR, "Invalid Draft UUID in state")
		return nil
	}
	body := cp.ModuleReasonedCommand{ExpectedResourceVersion: state.ResourceVersion.ValueInt64(), Reason: reason}
	if desired == "published" {
		result, err := r.cpClient.PublishModuleDraftWithResponse(ctx, r.orgID, state.ModuleID.ValueString(), id, &cp.PublishModuleDraftParams{IdempotencyKey: key}, body)
		if err != nil {
			diagnostics.AddError(PO_CLIENT_ERR, err.Error())
			return nil
		}
		if result.StatusCode() != http.StatusCreated || result.JSON201 == nil {
			addAPIResponseError(diagnostics, "publish reviewed Module Draft", result.StatusCode(), result.Body)
			return nil
		}
		return &result.JSON201.Draft
	}
	if desired != "discarded" {
		diagnostics.AddError(PO_INPUT_ERR, "Draft status must be open, published or discarded")
		return nil
	}
	result, err := r.cpClient.DiscardModuleDraftWithResponse(ctx, r.orgID, state.ModuleID.ValueString(), id, &cp.DiscardModuleDraftParams{IdempotencyKey: key}, body)
	if err != nil {
		diagnostics.AddError(PO_CLIENT_ERR, err.Error())
		return nil
	}
	if result.StatusCode() != http.StatusOK || result.JSON200 == nil {
		addAPIResponseError(diagnostics, "discard Module Draft", result.StatusCode(), result.Body)
		return nil
	}
	return result.JSON200
}

func (r *ModuleDraftResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ModuleDraftResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.Status.ValueString() == "open" {
		id, err := uuid.Parse(state.ID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError(PO_INPUT_ERR, "Invalid Draft UUID in state")
			return
		}
		current, err := r.cpClient.GetModuleDraftWithResponse(ctx, r.orgID, state.ModuleID.ValueString(), id)
		if err != nil {
			resp.Diagnostics.AddError(PO_CLIENT_ERR, err.Error())
			return
		}
		if current.StatusCode() != http.StatusOK || current.JSON200 == nil {
			addAPIResponseError(&resp.Diagnostics, "read Draft before discarding", current.StatusCode(), current.Body)
			return
		}
		if current.JSON200.Status != cp.ModuleDraftStatusOpen {
			resp.State.RemoveResource(ctx)
			return
		}
		key := moduleVersionCommandKey(r.orgID, state.ModuleID.ValueString(), state.ID.ValueString(), "draft-discard:"+strconv.FormatInt(state.ResourceVersion.ValueInt64(), 10))
		if r.close(ctx, state, "discarded", key, "Discard Terraform-managed authoring Draft", &resp.Diagnostics) == nil {
			return
		}
	}
	resp.Diagnostics.AddWarning("Draft history retained", "Authoring revisions and any published Module Version remain in the Orchestrator. Terraform relinquished ownership of this Draft.")
	resp.State.RemoveResource(ctx)
}

func (r *ModuleDraftResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 3)
	if len(parts) != 3 || parts[0] == "" || strings.TrimSpace(parts[2]) == "" {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Import ID must be <module-id>/<draft-uuid>/<authoring-key>")
		return
	}
	if _, err := uuid.Parse(parts[1]); err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Invalid Draft UUID in import ID")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("module_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("authoring_key"), parts[2])...)
}
