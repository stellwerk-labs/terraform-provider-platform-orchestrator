package provider

import (
	"context"
	"fmt"
	"net/http"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
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

var nativeRunnerIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]+$`)

var _ resource.ResourceWithModifyPlan = &NativeAgentRunnerResource{}
var _ resource.ResourceWithImportState = &NativeAgentRunnerResource{}

type NativeAgentRunnerResource struct {
	client cp.ClientWithResponsesInterface
	org    string
}

func NewNativeAgentRunnerResource() resource.Resource { return &NativeAgentRunnerResource{} }
func (r *NativeAgentRunnerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_native_agent_runner"
}
func (r *NativeAgentRunnerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Runtime-independent native Agent registration. Core permanently retains its UUID and history. Destroy is refused and keeps Terraform state; decommission/erasure is unavailable. Clearing inventory disables new admission without cancelling accepted work. Registration does not activate or assign a site.", Attributes: map[string]schema.Attribute{
		"id":          schema.StringAttribute{Required: true, MarkdownDescription: "Organization-local Runner ID using the Core pattern and the Native v2 runtime profile of 2–128 characters. Changing it requires replacement, whose protected destroy is refused.", Validators: []validator.String{stringvalidator.RegexMatches(nativeRunnerIDPattern, "must match the Core Runner ID pattern"), stringvalidator.LengthBetween(2, 128)}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"uuid":        schema.StringAttribute{Computed: true, MarkdownDescription: "Server-issued immutable registration UUID, copied to private local configuration after Create.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"description": schema.StringAttribute{Optional: true, MarkdownDescription: "Public description. Use an explicit empty string to clear displayed text; Core cannot clear an existing description to null.", Validators: []validator.String{stringvalidator.LengthAtMost(200)}},
		"runner_configuration": schema.SingleNestedAttribute{Required: true, MarkdownDescription: "Public native registration only. No Job, DSN, private keys or local configuration.", Attributes: map[string]schema.Attribute{
			"key":                 schema.StringAttribute{Required: true, MarkdownDescription: "Agent authentication Ed25519 public PEM. The matching private key remains at the site."},
			"max_parallel_jobs":   schema.Int64Attribute{Required: true, MarkdownDescription: "Positive logical-pool admission capacity; no unlimited default.", Validators: []validator.Int64{int64validator.AtLeast(1)}},
			"supported_protocols": schema.SetAttribute{Required: true, ElementType: types.StringType, MarkdownDescription: "Explicit v2 pipeline and saved-plan protocols. Legacy v1 variants are forbidden.", Validators: []validator.Set{setvalidator.SizeAtMost(16), setvalidator.ValueStringsAre(stringvalidator.LengthBetween(1, 128))}},
			"capabilities":        schema.SetAttribute{Required: true, ElementType: types.StringType, MarkdownDescription: "Explicit public capabilities exported from the approved site. An empty set is allowed.", Validators: []validator.Set{setvalidator.SizeAtMost(32), setvalidator.ValueStringsAre(stringvalidator.RegexMatches(regexp.MustCompile(`^[a-z][a-z0-9_.:/-]{0,127}$`), "must be an execution capability"))}},
			"native_execution":    schema.StringAttribute{Optional: true, CustomType: jsontypes.NormalizedType{}, MarkdownDescription: "Inner approved public v2 catalogue JSON. Decode the rollout-catalogue export envelope with jsondecode(file(...)) or yamldecode(file(...)), use its supported_protocols/capabilities declarations and jsonencode(local.catalogue.native_execution). Required on new Create; null disables an existing registration. Never upload private local configuration."},
		}},
		"state_storage_configuration": commonRunnerStateStorageResourceSchema,
	}}
}
func (r *NativeAgentRunnerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*PlatformOrchestratorProviderData)
	if !ok {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, "Expected provider configuration.")
		return
	}
	r.client, r.org = data.CpClient, data.OrgId
}
func (r *NativeAgentRunnerResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var planned nativeAgentRunnerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &planned)...)
	if resp.Diagnostics.HasError() || planned.Configuration.IsUnknown() {
		return
	}
	config, d := nativeAgentConfig(ctx, planned.Configuration)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	inventoryPath := path.Root("runner_configuration").AtName("native_execution")
	if !config.Inventory.IsUnknown() {
		if _, err := nativeAgentInventory(config.Inventory, req.State.Raw.IsNull()); err != nil {
			resp.Diagnostics.AddAttributeError(inventoryPath, PO_INPUT_ERR, err.Error())
		}
	}
	if req.State.Raw.IsNull() {
		return
	}
	var previous nativeAgentRunnerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	if !previous.Description.IsNull() && planned.Description.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("description"), PO_INPUT_ERR, "Core cannot clear an existing description to null; use an explicit empty string.")
	}
}
func (r *NativeAgentRunnerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var planned nativeAgentRunnerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &planned)...)
	if resp.Diagnostics.HasError() {
		return
	}
	config, d := nativeAgentInput(ctx, planned, true)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	backend, err := nativeAgentBackend(ctx, planned.Backend)
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Native Runner state backend is invalid.")
		return
	}
	var description *string
	if !planned.Description.IsNull() {
		value := planned.Description.ValueString()
		description = &value
	}
	response, err := r.client.CreateRunnerWithResponse(ctx, r.org, cp.RunnerCreateBody{Id: planned.ID.ValueString(), Description: description, RunnerConfiguration: config, StateStorageConfiguration: backend})
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Native Runner Create failed; inspect Core before retrying, an effect may have occurred.")
		return
	}
	if response == nil || response.StatusCode() != http.StatusCreated {
		resp.Diagnostics.AddError(PO_API_ERR, "Native Runner Create was refused; inspect input, permissions or an existing registration before retrying. Existing registrations require explicit import.")
		return
	}
	actual, d := nativeAgentFromAPI(ctx, response.JSON201, r.org, planned.ID.ValueString(), types.StringNull())
	resp.Diagnostics.Append(d...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &actual)...)
	}
}
func (r *NativeAgentRunnerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	resp.State = req.State
	var previous nativeAgentRunnerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !nativeAgentTrackedUUID(previous.UUID) {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Tracked native Runner UUID is missing or invalid; inspect state before retrying. Registration is never automatically adopted.")
		return
	}
	actual, d := readNativeAgent(ctx, r.client, r.org, previous.ID.ValueString(), previous.UUID)
	resp.Diagnostics.Append(d...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &actual)...)
	}
}
func (r *NativeAgentRunnerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.State = req.State
	var planned, previous nativeAgentRunnerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &planned)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !nativeAgentTrackedUUID(previous.UUID) {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Tracked native Runner UUID is missing or invalid; inspect state before retrying. Registration is never automatically adopted.")
		return
	}
	_, d := nativeAgentInput(ctx, planned, false)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, changed, d := nativeAgentPatch(ctx, previous, planned)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	actual, d := readNativeAgent(ctx, r.client, r.org, previous.ID.ValueString(), previous.UUID)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if changed {
		actual, d = r.applyNativeAgentPatch(ctx, previous, body)
		resp.Diagnostics.Append(d...)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	equal, d := nativeAgentModelsEqual(ctx, actual, planned)
	resp.Diagnostics.Append(d...)
	if !equal && !resp.Diagnostics.HasError() {
		resp.Diagnostics.AddError("Native Runner changed outside the reviewed plan", "Refresh and replan before retrying. The requested subset may already have committed. Prior state and UUID are retained; unchanged inventory was not replaced.")
	}
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &actual)...)
	}
}
func (r *NativeAgentRunnerResource) applyNativeAgentPatch(ctx context.Context, previous nativeAgentRunnerModel, body cp.RunnerUpdateBody) (nativeAgentRunnerModel, diag.Diagnostics) {
	var d diag.Diagnostics
	response, err := r.client.UpdateRunnerWithResponse(ctx, r.org, previous.ID.ValueString(), body)
	if err != nil {
		d.AddError(PO_CLIENT_ERR, "Native Runner Update failed; refresh before retrying because an effect may have occurred.")
		return nativeAgentRunnerModel{}, d
	}
	if response == nil || response.StatusCode() != http.StatusOK {
		d.AddError(PO_API_ERR, "Native Runner Update was refused; inspect the registration and permissions. Existing state is retained.")
		return nativeAgentRunnerModel{}, d
	}
	return nativeAgentFromAPI(ctx, response.JSON200, r.org, previous.ID.ValueString(), previous.UUID)
}
func (r *NativeAgentRunnerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.State = req.State
	var previous nativeAgentRunnerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !nativeAgentTrackedUUID(previous.UUID) {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Tracked native Runner UUID is missing or invalid; inspect state before retrying. Registration is never automatically adopted.")
		return
	}
	_, d := readNativeAgent(ctx, r.client, r.org, previous.ID.ValueString(), previous.UUID)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	response, err := r.client.DeleteRunnerWithResponse(ctx, r.org, previous.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Native Runner Delete failed; existing registration state and UUID are retained.")
		return
	}
	status := 0
	if response != nil {
		status = response.StatusCode()
	}
	if status == http.StatusConflict {
		resp.Diagnostics.AddError("Native Runner registration is retained", "Core refuses deletion to preserve registration identity and history. Terraform state is retained; decommission/erasure is unavailable.")
		return
	}
	resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unexpected native Runner Delete response (HTTP%d). Inspect registration lifetime before retrying; state and UUID are retained.", status))
}
func (r *NativeAgentRunnerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if len(req.ID) > 128 || !nativeRunnerIDPattern.MatchString(req.ID) {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Import requires a Native v2 Runner ID of 2–128 characters matching the Core Runner pattern, in the configured organization.")
		return
	}
	actual, d := readNativeAgent(ctx, r.client, r.org, req.ID, types.StringNull())
	resp.Diagnostics.Append(d...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &actual)...)
	}
}
