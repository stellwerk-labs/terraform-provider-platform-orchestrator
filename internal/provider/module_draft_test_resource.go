package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
)

var _ resource.Resource = &ModuleDraftTestResource{}
var _ resource.ResourceWithImportState = &ModuleDraftTestResource{}

func NewModuleDraftTestResource() resource.Resource { return &ModuleDraftTestResource{} }

type ModuleDraftTestResource struct {
	cpClient cp.ClientWithResponsesInterface
	orgID    string
}
type ModuleDraftTestResourceModel struct {
	ID                      types.String `tfsdk:"id"`
	CaptureKey              types.String `tfsdk:"capture_key"`
	ProjectID               types.String `tfsdk:"project_id"`
	EnvironmentID           types.String `tfsdk:"env_id"`
	ModuleID                types.String `tfsdk:"module_id"`
	DraftID                 types.String `tfsdk:"draft_id"`
	ExpectedRevision        types.Int64  `tfsdk:"expected_resource_version"`
	Reason                  types.String `tfsdk:"reason"`
	ModuleUUID              types.String `tfsdk:"module_uuid"`
	ProjectUUID             types.String `tfsdk:"project_uuid"`
	EnvironmentUUID         types.String `tfsdk:"environment_uuid"`
	ResourceTypeVersionUUID types.String `tfsdk:"resource_type_version_uuid"`
	ResourceTypeVersion     types.Int64  `tfsdk:"resource_type_version"`
}

func (r *ModuleDraftTestResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_module_draft_test"
}
func (r *ModuleDraftTestResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{MarkdownDescription: "Captures one exact reviewed Module Draft revision for one Environment. This retains immutable test material without publishing a Module Version, reserving SemVer or executing a deployment. Use its module_uuid and id in a deployment's draft_test_snapshots map. Capture and publication remain separate actions; destroying this resource only forgets the reference and retains test history.", Attributes: map[string]schema.Attribute{
		"id":                         schema.StringAttribute{Computed: true, MarkdownDescription: "Immutable executable test snapshot UUID, distinct from Draft and published Version UUIDs."},
		"capture_key":                schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "Stable identity for this capture intent and its retries. Use a new key when starting another test session or recreating its Environment."},
		"project_id":                 schema.StringAttribute{Required: true, PlanModifiers: replace},
		"env_id":                     schema.StringAttribute{Required: true, PlanModifiers: replace},
		"module_id":                  schema.StringAttribute{Required: true, PlanModifiers: replace},
		"draft_id":                   schema.StringAttribute{Required: true, PlanModifiers: replace},
		"expected_resource_version":  schema.Int64Attribute{Required: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}, MarkdownDescription: "Exact reviewed authoring revision to capture. Save and review edits before changing this value; the server rejects stale revisions."},
		"reason":                     schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "Audited reason for capturing this exact test."},
		"module_uuid":                schema.StringAttribute{Computed: true},
		"project_uuid":               schema.StringAttribute{Computed: true},
		"environment_uuid":           schema.StringAttribute{Computed: true},
		"resource_type_version_uuid": schema.StringAttribute{Computed: true},
		"resource_type_version":      schema.Int64Attribute{Computed: true},
	}}
}
func (r *ModuleDraftTestResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
func applyDraftTestState(state *ModuleDraftTestResourceModel, snapshot cp.ModuleDraftTestSnapshot) {
	state.ID = types.StringValue(snapshot.Id.String())
	state.ModuleUUID = types.StringValue(snapshot.ModuleUuid.String())
	state.ProjectUUID = types.StringValue(snapshot.ProjectUuid.String())
	state.EnvironmentUUID = types.StringValue(snapshot.EnvironmentUuid.String())
	state.ResourceTypeVersionUUID = types.StringValue(snapshot.ResourceTypeVersionUuid.String())
	state.ResourceTypeVersion = types.Int64Value(snapshot.ResourceTypeVersion)
	state.DraftID = types.StringValue(snapshot.DraftId.String())
	state.ExpectedRevision = types.Int64Value(snapshot.DraftRevision)
	state.Reason = types.StringValue(snapshot.Reason)
}
func draftTestCaptureKey(org string, plan ModuleDraftTestResourceModel) string {
	input, _ := json.Marshal([]any{plan.CaptureKey.ValueString(), plan.ProjectID.ValueString(), plan.EnvironmentID.ValueString(), plan.DraftID.ValueString(), plan.ExpectedRevision.ValueInt64(), plan.Reason.ValueString()})
	return moduleVersionCommandKey(org, plan.ModuleID.ValueString(), string(input), "draft-test-capture")
}
func (r *ModuleDraftTestResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ModuleDraftTestResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	draft, err := uuid.Parse(plan.DraftID.ValueString())
	if err != nil || draft == uuid.Nil || plan.ExpectedRevision.ValueInt64() < 1 || strings.TrimSpace(plan.CaptureKey.ValueString()) == "" {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "A nonzero Draft UUID, positive reviewed resource version and stable capture_key are required")
		return
	}
	result, err := r.cpClient.CaptureModuleDraftTestSnapshotWithResponse(ctx, r.orgID, plan.ProjectID.ValueString(), plan.EnvironmentID.ValueString(), plan.ModuleID.ValueString(), draft, &cp.CaptureModuleDraftTestSnapshotParams{IdempotencyKey: draftTestCaptureKey(r.orgID, plan)}, cp.ModuleDraftTestCaptureBody{ExpectedResourceVersion: plan.ExpectedRevision.ValueInt64(), Reason: plan.Reason.ValueString()})
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, err.Error())
		return
	}
	if result.StatusCode() != http.StatusCreated || result.JSON201 == nil {
		addAPIResponseError(&resp.Diagnostics, "capture reviewed Module Draft test", result.StatusCode(), result.Body)
		return
	}
	applyDraftTestState(&plan, *result.JSON201)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}
func (r *ModuleDraftTestResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ModuleDraftTestResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := uuid.Parse(state.ID.ValueString())
	if err != nil || id == uuid.Nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Invalid test snapshot UUID in state")
		return
	}
	result, err := r.cpClient.GetModuleDraftTestSnapshotWithResponse(ctx, r.orgID, state.ProjectID.ValueString(), state.EnvironmentID.ValueString(), state.ModuleID.ValueString(), id)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, err.Error())
		return
	}
	if result.StatusCode() == http.StatusNotFound {
		resp.Diagnostics.AddWarning("Scoped Module test unavailable", "The snapshot or its original Environment is unavailable. Terraform forgets this reference; it never deletes test history. A replacement capture still requires the exact reviewed Draft revision and current permissions.")
		resp.State.RemoveResource(ctx)
		return
	}
	if result.StatusCode() != http.StatusOK || result.JSON200 == nil {
		addAPIResponseError(&resp.Diagnostics, "read captured Module test", result.StatusCode(), result.Body)
		return
	}
	applyDraftTestState(&state, *result.JSON200)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
func (r *ModuleDraftTestResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(PO_INPUT_ERR, "Captured tests are immutable; replace the resource with a new capture intent")
}
func (r *ModuleDraftTestResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning("Immutable Module test retained", "Terraform removed only its reference. Captured test history and existing deployments remain unchanged.")
}
func (r *ModuleDraftTestResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 5)
	if len(parts) != 5 || parts[0] == "" || parts[1] == "" || parts[2] == "" || strings.TrimSpace(parts[4]) == "" {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Import ID must be <project-id>/<env-id>/<module-id>/<snapshot-uuid>/<capture-key>")
		return
	}
	id, err := uuid.Parse(parts[3])
	if err != nil || id == uuid.Nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Invalid snapshot UUID in import ID")
		return
	}
	for name, value := range map[string]string{"project_id": parts[0], "env_id": parts[1], "module_id": parts[2], "id": id.String(), "capture_key": parts[4]} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(name), value)...)
	}
}
