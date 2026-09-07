package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &ModuleVersionPinNoteResource{}
var _ resource.ResourceWithImportState = &ModuleVersionPinNoteResource{}

func NewModuleVersionPinNoteResource() resource.Resource { return &ModuleVersionPinNoteResource{} }

type ModuleVersionPinNoteResource struct {
	cpClient cp.ClientWithResponsesInterface
	orgID    string
}

type ModuleVersionPinNoteResourceModel struct {
	ID                types.String `tfsdk:"id"`
	PinID             types.String `tfsdk:"pin_id"`
	Note              types.String `tfsdk:"note"`
	Revision          types.Int64  `tfsdk:"revision"`
	ActivationEventID types.String `tfsdk:"activation_event_id"`
	Actor             types.String `tfsdk:"actor"`
	ActorType         types.String `tfsdk:"actor_type"`
}

func (r *ModuleVersionPinNoteResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_module_version_pin_note"
}

func (r *ModuleVersionPinNoteResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Appends one immutable note to a Core Module Version Pin. Notes add operational context but never change the Pin state, Version, protection, activation boundary or an add-on approval. Terraform destroy only forgets the note because Pin history is append-only.",
		Attributes: map[string]schema.Attribute{
			"id":                  schema.StringAttribute{Computed: true, MarkdownDescription: "Immutable Pin event UUID."},
			"pin_id":              schema.StringAttribute{Required: true, PlanModifiers: replace},
			"note":                schema.StringAttribute{Required: true, PlanModifiers: replace},
			"revision":            schema.Int64Attribute{Computed: true},
			"activation_event_id": schema.StringAttribute{Computed: true, MarkdownDescription: "Unchanged Pin approval boundary returned with the note event."},
			"actor":               schema.StringAttribute{Computed: true},
			"actor_type":          schema.StringAttribute{Computed: true},
		},
	}
}

func (r *ModuleVersionPinNoteResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *ModuleVersionPinNoteResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ModuleVersionPinNoteResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pinID, err := uuid.Parse(plan.PinID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "pin_id must be a UUID: "+err.Error())
		return
	}
	if plan.Note.ValueString() == "" {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "note must not be empty")
		return
	}
	idempotencyKey := uuid.NewSHA1(pinID, []byte("terraform-note\x00"+plan.Note.ValueString())).String()
	response, err := r.cpClient.AppendEnvironmentModuleVersionPinNoteWithResponse(ctx, r.orgID, pinID,
		&cp.AppendEnvironmentModuleVersionPinNoteParams{IdempotencyKey: idempotencyKey},
		cp.ModuleVersionPinNoteBody{Note: plan.Note.ValueString()})
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to append Module Version Pin note: "+err.Error())
		return
	}
	if response.StatusCode() != http.StatusCreated || response.JSON201 == nil {
		addAPIResponseError(&resp.Diagnostics, "append Module Version Pin note", response.StatusCode(), response.Body)
		return
	}
	applyModuleVersionPinNoteState(&plan, *response.JSON201)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ModuleVersionPinNoteResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ModuleVersionPinNoteResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pinID, err := uuid.Parse(state.PinID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "pin_id must be a UUID: "+err.Error())
		return
	}
	response, err := r.cpClient.ListEnvironmentModuleVersionPinEventsWithResponse(ctx, r.orgID, pinID)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to read Module Version Pin notes: "+err.Error())
		return
	}
	if response.StatusCode() == http.StatusNotFound {
		resp.Diagnostics.AddError(PO_RESOURCE_NOT_FOUND_ERR, "The parent Pin and its append-only note history are unavailable; catalogue integrity requires operator investigation.")
		return
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		addAPIResponseError(&resp.Diagnostics, "read Module Version Pin notes", response.StatusCode(), response.Body)
		return
	}
	for _, event := range *response.JSON200 {
		if event.Id.String() == state.ID.ValueString() {
			applyModuleVersionPinNoteState(&state, event)
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	resp.Diagnostics.AddError(PO_RESOURCE_NOT_FOUND_ERR, "An immutable Module Version Pin note disappeared from append-only history; catalogue integrity requires operator investigation.")
}

func (r *ModuleVersionPinNoteResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
	// pin_id and note both require replacement, so Terraform never calls Update.
}

func (r *ModuleVersionPinNoteResource) Delete(ctx context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning("Immutable Pin note retained", "Stellwerk permanently retains Pin notes in append-only history. Terraform removed only its state reference; no API deletion was attempted.")
	resp.State.RemoveResource(ctx)
}

func (r *ModuleVersionPinNoteResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Import ID must be <pin-uuid>/<note-event-uuid>")
		return
	}
	if _, err := uuid.Parse(parts[0]); err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Import Pin ID must be a UUID")
		return
	}
	if _, err := uuid.Parse(parts[1]); err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Import note event ID must be a UUID")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("pin_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}

func applyModuleVersionPinNoteState(state *ModuleVersionPinNoteResourceModel, event cp.ModuleVersionPinEvent) {
	state.ID = types.StringValue(event.Id.String())
	state.PinID = types.StringValue(event.PinId.String())
	state.Revision = types.Int64Value(event.Revision)
	state.ActivationEventID = types.StringValue(event.ActivationEventId.String())
	state.Actor = types.StringValue(event.Actor.String())
	state.ActorType = types.StringValue(event.ActorType)
	state.Note = nullableString(event.Note)
}
