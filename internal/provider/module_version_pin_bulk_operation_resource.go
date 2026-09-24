package provider

import (
	"context"
	"fmt"
	"net/http"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &ModuleVersionPinBulkOperationResource{}

func NewModuleVersionPinBulkOperationResource() resource.Resource {
	return &ModuleVersionPinBulkOperationResource{}
}

type ModuleVersionPinBulkOperationResource struct {
	cpClient cp.ClientWithResponsesInterface
	orgID    string
}

type ModuleVersionPinBulkOperationResourceModel struct {
	ID                           types.String `tfsdk:"id"`
	ModuleUUID                   types.String `tfsdk:"module_uuid"`
	EnvironmentUUIDs             types.Set    `tfsdk:"environment_uuids"`
	Action                       types.String `tfsdk:"action"`
	Reason                       types.String `tfsdk:"reason"`
	ConfirmDefectiveVersionUUIDs types.Set    `tfsdk:"confirm_defective_version_uuids"`
	PreviewFingerprint           types.String `tfsdk:"preview_fingerprint"`
	PinsJSON                     types.String `tfsdk:"pins_json"`
}

func (r *ModuleVersionPinBulkOperationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_module_version_pin_bulk_operation"
}

func (r *ModuleVersionPinBulkOperationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	stringReplace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	setReplace := []planmodifier.Set{setplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Executes one atomic Orchestrator bulk Pin, Unpin or permanent-discard operation over a frozen explicit Environment UUID set. The resource is an immutable Terraform receipt; destroy forgets it and never reverses the audited operation implicitly.",
		Attributes: map[string]schema.Attribute{
			"id":                              schema.StringAttribute{Computed: true, MarkdownDescription: "Orchestrator bulk operation UUID."},
			"module_uuid":                     schema.StringAttribute{Required: true, PlanModifiers: stringReplace},
			"environment_uuids":               schema.SetAttribute{Required: true, ElementType: types.StringType, PlanModifiers: setReplace, MarkdownDescription: "Non-empty frozen explicit Environment UUIDs. Future Environments never inherit this action."},
			"action":                          schema.StringAttribute{Required: true, PlanModifiers: stringReplace, Validators: []validator.String{stringvalidator.OneOf("pin", "unpin", "discard")}},
			"reason":                          schema.StringAttribute{Required: true, PlanModifiers: stringReplace},
			"confirm_defective_version_uuids": schema.SetAttribute{Optional: true, ElementType: types.StringType, PlanModifiers: setReplace, MarkdownDescription: "Exact Defective Version UUID acknowledgements for otherwise-authorised Pin creation."},
			"preview_fingerprint":             schema.StringAttribute{Computed: true, MarkdownDescription: "Deterministic server preview accepted by the atomic command."},
			"pins_json":                       schema.StringAttribute{Computed: true, MarkdownDescription: "Materialised per-Environment Pin records returned atomically by the Orchestrator."},
		},
	}
}

func (r *ModuleVersionPinBulkOperationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func uuidSet(ctx context.Context, value types.Set) ([]uuid.UUID, error) {
	var raw []string
	if diagnostics := value.ElementsAs(ctx, &raw, false); diagnostics.HasError() {
		return nil, fmt.Errorf("UUID set could not be decoded: %s", diagnostics.Errors()[0].Detail())
	}
	result := make([]uuid.UUID, 0, len(raw))
	for _, item := range raw {
		parsed, err := uuid.Parse(item)
		if err != nil {
			return nil, fmt.Errorf("%q is not a UUID: %w", item, err)
		}
		result = append(result, parsed)
	}
	return result, nil
}

func (r *ModuleVersionPinBulkOperationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ModuleVersionPinBulkOperationResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	moduleID, err := uuid.Parse(plan.ModuleUUID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "module_uuid must be a UUID: "+err.Error())
		return
	}
	environmentIDs, err := uuidSet(ctx, plan.EnvironmentUUIDs)
	if err != nil || len(environmentIDs) == 0 {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "environment_uuids must contain explicit UUIDs: "+fmt.Sprint(err))
		return
	}
	confirmed, err := uuidSet(ctx, plan.ConfirmDefectiveVersionUUIDs)
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "confirm_defective_version_uuids is invalid: "+err.Error())
		return
	}
	action := cp.ModuleVersionPinBulkAction(plan.Action.ValueString())
	previewBody := cp.ModuleVersionPinBulkPreviewBody{Action: action, EnvironmentUuids: environmentIDs, ModuleUuid: moduleID}
	if len(confirmed) > 0 {
		previewBody.ConfirmDefectiveVersionUuids = &confirmed
	}
	preview, err := r.cpClient.PreviewEnvironmentModuleVersionPinBulkOperationWithResponse(ctx, r.orgID, previewBody)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to preview atomic Module Version Pin operation: "+err.Error())
		return
	}
	if preview.StatusCode() != http.StatusOK || preview.JSON200 == nil {
		addAPIResponseError(&resp.Diagnostics, "preview atomic Module Version Pin operation", preview.StatusCode(), preview.Body)
		return
	}
	if !preview.JSON200.Eligible {
		if !setJSONString(&resp.Diagnostics, &plan.PinsJSON, preview.JSON200.Items) {
			return
		}
		resp.Diagnostics.AddError(PO_API_ERR, "Core rejected one or more Environments in the atomic Pin preview; pins_json contains the per-Environment reasons.")
		return
	}
	commandBody := cp.ModuleVersionPinBulkCommandBody{
		Action: action, EnvironmentUuids: environmentIDs, ModuleUuid: moduleID,
		PreviewFingerprint: preview.JSON200.Fingerprint, Reason: plan.Reason.ValueString(),
	}
	if len(confirmed) > 0 {
		commandBody.ConfirmDefectiveVersionUuids = &confirmed
	}
	idempotencyKey := uuid.NewSHA1(preview.JSON200.OperationId, []byte("terraform\x00"+plan.Reason.ValueString())).String()
	result, err := r.cpClient.ExecuteEnvironmentModuleVersionPinBulkOperationWithResponse(ctx, r.orgID,
		&cp.ExecuteEnvironmentModuleVersionPinBulkOperationParams{IdempotencyKey: idempotencyKey}, commandBody)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to execute atomic Module Version Pin operation: "+err.Error())
		return
	}
	if result.StatusCode() != http.StatusOK || result.JSON200 == nil {
		addAPIResponseError(&resp.Diagnostics, "execute atomic Module Version Pin operation", result.StatusCode(), result.Body)
		return
	}
	plan.ID = types.StringValue(result.JSON200.OperationId.String())
	plan.PreviewFingerprint = types.StringValue(preview.JSON200.Fingerprint)
	if !setJSONString(&resp.Diagnostics, &plan.PinsJSON, result.JSON200.Pins) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ModuleVersionPinBulkOperationResource) Read(context.Context, resource.ReadRequest, *resource.ReadResponse) {
}

func (r *ModuleVersionPinBulkOperationResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
}

func (r *ModuleVersionPinBulkOperationResource) Delete(ctx context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning("Atomic Pin operation retained", "This resource is an immutable receipt. Stellwerk retained the materialised Pins and append-only audit history; Terraform did not infer a reverse bulk action.")
	resp.State.RemoveResource(ctx)
}
